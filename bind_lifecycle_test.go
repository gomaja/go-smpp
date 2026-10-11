package gosmpp

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gomaja/go-smpp/data"
	"github.com/gomaja/go-smpp/pdu"
)

// bindDeadlineProbe shortens a requested default deadline so the test exercises
// both blocked I/O paths without waiting for the production timeout.
type bindDeadlineProbe struct {
	net.Conn
	deadlines chan time.Time
}

type bindIOProbe struct {
	net.Conn
	readStarted  chan struct{}
	writeStarted chan struct{}
	readOnce     sync.Once
	writeOnce    sync.Once
}

func (c *bindIOProbe) Read(b []byte) (int, error) {
	c.readOnce.Do(func() { close(c.readStarted) })
	return c.Conn.Read(b)
}

func (c *bindIOProbe) Write(b []byte) (int, error) {
	c.writeOnce.Do(func() { close(c.writeStarted) })
	return c.Conn.Write(b)
}

func (c *bindDeadlineProbe) SetDeadline(deadline time.Time) error {
	c.deadlines <- deadline
	if !deadline.IsZero() {
		deadline = time.Now().Add(20 * time.Millisecond)
	}
	return c.Conn.SetDeadline(deadline)
}

type bindConnectResult struct {
	conn *Connection
	err  error
}

func waitBindResult(t *testing.T, results <-chan bindConnectResult) bindConnectResult {
	t.Helper()
	select {
	case result := <-results:
		return result
	case <-time.After(time.Second):
		t.Fatal("bind exchange did not finish")
		return bindConnectResult{}
	}
}

func waitBindIO(t *testing.T, started <-chan struct{}) {
	t.Helper()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("bind did not enter I/O")
	}
}

func TestBindLifecycleDeadlineCoversReadAndWrite(t *testing.T) {
	for _, readBind := range []bool{false, true} {
		name := "write"
		if readBind {
			name = "read"
		}
		t.Run(name, func(t *testing.T) {
			client, peer := net.Pipe()
			probe := &bindDeadlineProbe{Conn: client, deadlines: make(chan time.Time, 2)}
			defer func() { _ = client.Close() }()
			defer func() { _ = peer.Close() }()
			peerDone := make(chan error, 1)
			if readBind {
				go func() {
					_, err := pdu.Parse(peer)
					peerDone <- err
				}()
			}
			results := make(chan bindConnectResult, 1)
			go func() {
				conn, err := connect(DialerFunc(func(context.Context, string) (net.Conn, error) { return probe, nil }), "synthetic", pdu.NewBindRequest(pdu.Transceiver))
				results <- bindConnectResult{conn: conn, err: err}
			}()
			var result bindConnectResult
			select {
			case result = <-results:
			case <-time.After(time.Second):
				t.Error("bind exchange has no deadline and remains blocked")
				_ = peer.Close()
				result = waitBindResult(t, results)
			}
			select {
			case deadline := <-probe.deadlines:
				if remaining := time.Until(deadline); remaining < 25*time.Second || remaining > DefaultBindTimeout {
					t.Errorf("bind deadline = %v from now, want default %v", remaining, DefaultBindTimeout)
				}
			default:
				t.Error("bind exchange never set a read/write deadline")
			}
			if !errors.Is(result.err, os.ErrDeadlineExceeded) {
				t.Errorf("bind error = %v, want deadline exceeded", result.err)
			}
			if readBind {
				if err := <-peerDone; err != nil {
					t.Errorf("peer did not receive the bind: %v", err)
				}
			}
		})
	}
}

func TestBindLifecycleRequiresMatchingResponse(t *testing.T) {
	tests := []struct {
		name      string
		response  func(*pdu.BindRequest) pdu.PDU
		wantError string
	}{
		{name: "wrong command", response: func(request *pdu.BindRequest) pdu.PDU {
			response := pdu.NewBindReceiverResp()
			response.SetSequenceNumber(request.GetSequenceNumber())
			return response
		}, wantError: "unexpected bind response command ID"},
		{name: "wrong sequence", response: func(request *pdu.BindRequest) pdu.PDU {
			response := request.GetResponse()
			response.SetSequenceNumber(request.GetSequenceNumber() + 1)
			return response
		}, wantError: "unexpected bind response sequence number"},
		{name: "wrong command and sequence", response: func(request *pdu.BindRequest) pdu.PDU {
			response := pdu.NewBindReceiverResp()
			response.SetSequenceNumber(request.GetSequenceNumber() + 1)
			return response
		}, wantError: "unexpected bind response sequence number"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := exchangeBindResponse(t, test.response)
			if result.err == nil || !strings.Contains(result.err.Error(), test.wantError) {
				t.Errorf("bind error = %v, want %q", result.err, test.wantError)
			}
		})
	}
}

func TestBindLifecyclePreservesFailureStatus(t *testing.T) {
	responses := []struct {
		name string
		make func(*pdu.BindRequest) pdu.PDU
	}{
		{name: "bind response", make: func(request *pdu.BindRequest) pdu.PDU { return request.GetResponse() }},
		{name: "generic nack", make: func(*pdu.BindRequest) pdu.PDU { return pdu.NewGenericNack() }},
		{name: "other failed response", make: func(*pdu.BindRequest) pdu.PDU { return pdu.NewEnquireLinkResp() }},
	}
	for _, response := range responses {
		t.Run(response.name, func(t *testing.T) {
			result := exchangeBindResponse(t, func(request *pdu.BindRequest) pdu.PDU {
				failure := response.make(request)
				failure.SetSequenceNumber(request.GetSequenceNumber())
				failure.SetCommandStatus(data.ESME_RBINDFAIL)
				return failure
			})
			var bindError BindError
			if !errors.As(result.err, &bindError) || bindError.CommandStatus != data.ESME_RBINDFAIL {
				t.Errorf("bind error = %v, want BindError with ESME_RBINDFAIL", result.err)
			}
		})
	}
}

func exchangeBindResponse(t *testing.T, response func(*pdu.BindRequest) pdu.PDU) bindConnectResult {
	t.Helper()
	client, peer := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = peer.Close() }()
	peerDone := make(chan error, 1)
	go func() {
		request, err := pdu.Parse(peer)
		if err == nil {
			_, err = NewConnection(peer).WritePDU(response(request.(*pdu.BindRequest)))
		}
		peerDone <- err
	}()
	results := make(chan bindConnectResult, 1)
	go func() {
		conn, err := connect(DialerFunc(func(context.Context, string) (net.Conn, error) { return client, nil }), "synthetic", pdu.NewBindRequest(pdu.Transceiver))
		results <- bindConnectResult{conn: conn, err: err}
	}()
	result := waitBindResult(t, results)
	if err := <-peerDone; err != nil {
		t.Errorf("peer bind exchange: %v", err)
	}
	return result
}

func TestBindLifecycleConnectContextSupported(t *testing.T) {
	connector := TRXConnector(DialerFunc(func(context.Context, string) (net.Conn, error) { return nil, errors.New("must not dial") }), Auth{})
	contextConnector, ok := connector.(interface {
		ConnectContext(context.Context) (*Connection, error)
	})
	if !ok {
		t.Fatal("connector cannot cancel an in-flight bind")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := contextConnector.ConnectContext(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("bind error = %v, want context cancellation", err)
	}
}

func TestBindLifecycleConfiguredTimeout(t *testing.T) {
	constructors := []struct {
		name string
		make func(Dialer, Auth, ...connectorOption) Connector
	}{
		{name: "TX", make: TXConnector},
		{name: "RX", make: RXConnector},
		{name: "TRX", make: TRXConnector},
	}
	for _, constructor := range constructors {
		t.Run(constructor.name, func(t *testing.T) {
			client, peer := net.Pipe()
			defer func() { _ = client.Close() }()
			defer func() { _ = peer.Close() }()
			peerDone := make(chan error, 1)
			go func() {
				_, err := pdu.Parse(peer)
				peerDone <- err
			}()
			connector := constructor.make(DialerFunc(func(context.Context, string) (net.Conn, error) { return client, nil }), Auth{}, WithBindTimeout(20*time.Millisecond))
			results := make(chan bindConnectResult, 1)
			go func() {
				conn, err := connector.Connect()
				results <- bindConnectResult{conn: conn, err: err}
			}()
			result := waitBindResult(t, results)
			if !errors.Is(result.err, os.ErrDeadlineExceeded) {
				t.Errorf("bind error = %v, want deadline exceeded", result.err)
			}
			if err := <-peerDone; err != nil {
				t.Errorf("peer bind: %v", err)
			}
		})
	}
}

func TestBindLifecycleCancellationInterruptsIO(t *testing.T) {
	for _, readBind := range []bool{false, true} {
		name := "write"
		if readBind {
			name = "read"
		}
		t.Run(name, func(t *testing.T) {
			client, peer := net.Pipe()
			defer func() { _ = client.Close() }()
			defer func() { _ = peer.Close() }()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			probe := &bindIOProbe{Conn: client, readStarted: make(chan struct{}), writeStarted: make(chan struct{})}
			connector := TRXConnector(DialerFunc(func(context.Context, string) (net.Conn, error) {
				return probe, nil
			}), Auth{})
			contextConnector := connector.(interface {
				ConnectContext(context.Context) (*Connection, error)
			})
			peerDone := make(chan error, 1)
			if readBind {
				go func() {
					_, err := pdu.Parse(peer)
					peerDone <- err
				}()
			}
			results := make(chan bindConnectResult, 1)
			go func() {
				conn, err := contextConnector.ConnectContext(ctx)
				results <- bindConnectResult{conn: conn, err: err}
			}()
			if readBind {
				if err := <-peerDone; err != nil {
					t.Fatalf("peer bind: %v", err)
				}
				waitBindIO(t, probe.readStarted)
			} else {
				waitBindIO(t, probe.writeStarted)
			}
			cancel()
			if result := waitBindResult(t, results); !errors.Is(result.err, context.Canceled) {
				t.Errorf("bind error = %v, want context cancellation", result.err)
			}
		})
	}
}

func TestBindLifecycleSuccessfulExchangeClearsDeadlineAndCancellation(t *testing.T) {
	client, peer := net.Pipe()
	probe := &bindDeadlineProbe{Conn: client, deadlines: make(chan time.Time, 2)}
	defer func() { _ = client.Close() }()
	defer func() { _ = peer.Close() }()
	peerDone := make(chan error, 1)
	go func() {
		request, err := pdu.Parse(peer)
		if err == nil {
			response := request.GetResponse().(*pdu.BindResp)
			response.SystemID = "synthetic-smsc"
			_, err = NewConnection(peer).WritePDU(response)
		}
		peerDone <- err
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	connector := TRXConnector(DialerFunc(func(context.Context, string) (net.Conn, error) { return probe, nil }), Auth{})
	contextConnector := connector.(interface {
		ConnectContext(context.Context) (*Connection, error)
	})
	conn, err := contextConnector.ConnectContext(ctx)
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	if conn.systemID != "synthetic-smsc" {
		t.Errorf("system ID = %q", conn.systemID)
	}
	if err := <-peerDone; err != nil {
		t.Fatalf("peer bind: %v", err)
	}
	if deadline := <-probe.deadlines; deadline.IsZero() {
		t.Error("bind deadline was disabled")
	}
	if deadline := <-probe.deadlines; !deadline.IsZero() {
		t.Errorf("successful bind retained deadline %v", deadline)
	}
	cancel()
	go func() {
		_, err := pdu.Parse(peer)
		peerDone <- err
	}()
	if _, err := conn.WritePDU(pdu.NewEnquireLink()); err != nil {
		t.Errorf("completed bind closed on later context cancellation: %v", err)
	}
	if err := <-peerDone; err != nil {
		t.Errorf("peer enquire_link: %v", err)
	}
}
