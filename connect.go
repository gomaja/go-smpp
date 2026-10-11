package gosmpp

import (
	"context"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/gomaja/go-smpp/data"
	"github.com/gomaja/go-smpp/pdu"
)

var (
	// NonTLSDialer opens a TCP connection and cancels dialing with ctx.
	NonTLSDialer Dialer = networkDialer{}
)

// Dialer opens a connection using ctx to cancel dialing or TLS handshaking.
// Custom implementations should honor cancellation. If an implementation
// ignores it, the connector returns promptly and closes a late connection, but
// the dial function's goroutine cannot finish until that function returns.
type Dialer interface {
	DialContext(ctx context.Context, addr string) (net.Conn, error)
}

// DialerFunc adapts a context-aware dial function to Dialer.
type DialerFunc func(ctx context.Context, addr string) (net.Conn, error)

func (f DialerFunc) DialContext(ctx context.Context, addr string) (net.Conn, error) {
	return f(ctx, addr)
}

type networkDialer struct{}

func (networkDialer) DialContext(ctx context.Context, addr string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, "tcp", addr)
}

// Built-in dialers honor cancellation, so their caller joins the actual dial.
// The private marker distinguishes this ownership without function identity.
func (networkDialer) builtinDialer() {}

// Auth represents basic authentication to SMSC.
type Auth struct {
	// SMSC is SMSC address.
	SMSC       string
	SystemID   string
	Password   string
	SystemType string
}

type BindError struct {
	CommandStatus data.CommandStatusType
}

func (err BindError) Error() string {
	return fmt.Sprintf("binding error (%s): %s", err.CommandStatus, err.CommandStatus.Desc())
}

func newBindRequest(s Auth, bindingType pdu.BindingType, addressRange pdu.AddressRange) (bindReq *pdu.BindRequest) {
	bindReq = pdu.NewBindRequest(bindingType)
	bindReq.SystemID = s.SystemID
	bindReq.Password = s.Password
	bindReq.SystemType = s.SystemType
	bindReq.AddressRange = addressRange
	return
}

// Connector is connection factory interface.
type Connector interface {
	Connect() (conn *Connection, err error)
	GetBindType() pdu.BindingType
}

type connector struct {
	dialer       Dialer
	auth         Auth
	bindingType  pdu.BindingType
	addressRange pdu.AddressRange
	bindTimeout  time.Duration
}

func (c *connector) GetBindType() pdu.BindingType {
	return c.bindingType
}

func (c *connector) Connect() (conn *Connection, err error) {
	return c.ConnectContext(context.Background())
}

// ConnectContext authenticates a connection and permits cancellation of both
// dialing and bind I/O. A late connection from a canceled dial is closed.
func (c *connector) ConnectContext(ctx context.Context) (*Connection, error) {
	return connectContext(ctx, c.dialer, c.auth.SMSC, newBindRequest(c.auth, c.bindingType, c.addressRange), c.bindTimeout)
}

func connect(dialer Dialer, addr string, bindReq *pdu.BindRequest) (c *Connection, err error) {
	return connectContext(context.Background(), dialer, addr, bindReq, DefaultBindTimeout)
}

func connectContext(ctx context.Context, dialer Dialer, addr string, bindReq *pdu.BindRequest, timeout time.Duration) (c *Connection, err error) {
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	conn, err := dialConnection(ctx, dialer, addr)
	if err != nil {
		return
	}

	c = NewConnection(conn)
	cancelDone := make(chan struct{})
	stopCancellation := context.AfterFunc(ctx, func() {
		_ = conn.Close()
		close(cancelDone)
	})
	defer func() {
		// AfterFunc's stop does not join an already-started callback. Join it
		// here so it cannot close a successfully returned connection later.
		if !stopCancellation() {
			<-cancelDone
		}
		if contextErr := ctx.Err(); contextErr != nil {
			err = contextErr
		}
		if err != nil {
			_ = conn.Close()
		}
	}()
	if timeout <= 0 {
		timeout = DefaultBindTimeout
	}
	deadline := time.Now().Add(timeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err = conn.SetDeadline(deadline); err != nil {
		return
	}

	// Send the bind request. It carries the password, so it is encoded here
	// rather than through WritePDU: an encoding failure is reported from its
	// cause alone, never with the request attached, where logging the error
	// (%#v, a reflection-based or JSON logger) would print the credential.
	buf := pdu.NewBuffer(nil)
	bindReq.Marshal(buf)
	if err = buf.Err(); err != nil {
		err = fmt.Errorf("%w: bind request: %w", pdu.ErrEncode, err)
		return
	}
	var n int
	if n, err = c.Write(buf.Bytes()); err == nil && n != buf.Len() {
		err = io.ErrShortWrite
	}
	if err != nil {
		return
	}

	var response pdu.PDU
	for {
		if response, err = pdu.Parse(c); err != nil {
			return
		}
		// SMPP v5 §2.4 Table 2-1 permits enquire_link in the OPEN state.
		// Reply while retaining the original bind exchange deadline.
		if response.GetHeader().CommandID != data.ENQUIRE_LINK {
			break
		}
		if _, err = c.WritePDU(response.GetResponse()); err != nil {
			return
		}
	}
	// SMPP v3.4 §4.3.1 permits a NULL sequence when the original PDU
	// cannot be decoded. During bind this still carries the failure status.
	if response.IsGNack() && (response.GetSequenceNumber() == 0 || response.GetSequenceNumber() == bindReq.GetSequenceNumber()) {
		return c, BindError{CommandStatus: response.GetHeader().CommandStatus}
	}
	// SMPP v3.4 §§3.2.1, 5.1.4: responses preserve the request's sequence.
	if response.GetSequenceNumber() != bindReq.GetSequenceNumber() {
		return c, fmt.Errorf("unexpected bind response sequence number %d, want %d", response.GetSequenceNumber(), bindReq.GetSequenceNumber())
	}
	// SMPP v3.4 §4.3.1: generic_nack rejects the original request. Preserve
	// all failure statuses instead of waiting for a successful bind response.
	if response.IsGNack() || response.GetHeader().CommandStatus != data.ESME_ROK {
		return c, BindError{CommandStatus: response.GetHeader().CommandStatus}
	}
	expectedCommand := bindReq.GetResponse().GetHeader().CommandID
	// SMPP v3.4 §§4.1.2, 4.1.4, 4.1.6: each bind has its own response.
	resp, ok := response.(*pdu.BindResp)
	if !ok || response.GetHeader().CommandID != expectedCommand {
		return c, fmt.Errorf("unexpected bind response command ID %s, want %s", response.GetHeader().CommandID, expectedCommand)
	}
	c.systemID = resp.SystemID
	err = conn.SetDeadline(time.Time{})

	return
}

func dialConnection(ctx context.Context, dialer Dialer, addr string) (net.Conn, error) {
	if _, builtin := dialer.(interface{ builtinDialer() }); builtin {
		return dialer.DialContext(ctx, addr)
	}
	type result struct {
		conn net.Conn
		err  error
	}
	results := make(chan result)
	go func() {
		conn, err := dialer.DialContext(ctx, addr)
		select {
		case results <- result{conn: conn, err: err}:
		case <-ctx.Done():
			if conn != nil {
				_ = conn.Close()
			}
		}
	}()
	select {
	case r := <-results:
		if contextErr := ctx.Err(); contextErr != nil {
			if r.conn != nil {
				_ = r.conn.Close()
			}
			return nil, contextErr
		}
		if r.err != nil && r.conn != nil {
			_ = r.conn.Close()
		}
		return r.conn, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// TXConnector returns a Transmitter (TX) connector.
func TXConnector(dialer Dialer, auth Auth, opts ...connectorOption) Connector {
	c := &connector{
		dialer:      dialer,
		auth:        auth,
		bindingType: pdu.Transmitter,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// RXConnector returns a Receiver (RX) connector.
func RXConnector(dialer Dialer, auth Auth, opts ...connectorOption) Connector {
	c := &connector{
		dialer:      dialer,
		auth:        auth,
		bindingType: pdu.Receiver,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// TRXConnector returns a Transceiver (TRX) connector.
func TRXConnector(dialer Dialer, auth Auth, opts ...connectorOption) Connector {
	c := &connector{
		dialer:      dialer,
		auth:        auth,
		bindingType: pdu.Transceiver,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

type connectorOption func(c *connector)

func WithAddressRange(addressRange pdu.AddressRange) connectorOption {
	return func(c *connector) {
		c.addressRange = addressRange
	}
}

// WithBindTimeout bounds writing the bind request and reading its response.
// A non-positive timeout uses DefaultBindTimeout, shared with the server.
// Dialing is controlled by the Dialer and is outside this timeout.
func WithBindTimeout(timeout time.Duration) connectorOption {
	return func(c *connector) {
		c.bindTimeout = timeout
	}
}
