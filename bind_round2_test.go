package gosmpp

import (
	"context"
	"errors"
	"net"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/gomaja/go-smpp/data"
	"github.com/gomaja/go-smpp/pdu"
)

type bindLateConnection struct {
	net.Conn
	closed chan struct{}
	once   sync.Once
}

func (c *bindLateConnection) Close() error {
	c.once.Do(func() { close(c.closed) })
	return c.Conn.Close()
}

func TestBindLifecycleCancellationClosesLateCustomDial(t *testing.T) {
	baseline := runtime.NumGoroutine()
	client, peer := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = peer.Close() }()
	late := &bindLateConnection{Conn: client, closed: make(chan struct{})}
	entered, release := make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	connector := TRXConnector(DialerFunc(func(context.Context, string) (net.Conn, error) {
		close(entered)
		<-release
		return late, nil
	}), Auth{})
	results := make(chan bindConnectResult, 1)
	go func() {
		conn, err := connector.(contextConnector).ConnectContext(ctx)
		results <- bindConnectResult{conn: conn, err: err}
	}()
	lifecycleWait(t, entered)
	cancel()
	select {
	case result := <-results:
		if !errors.Is(result.err, context.Canceled) {
			t.Errorf("connect error = %v, want context cancellation", result.err)
		}
		close(release)
	case <-time.After(time.Second):
		t.Error("cancellation waited for a custom dialer that ignored the context")
		close(release)
		if result := waitBindResult(t, results); !errors.Is(result.err, context.Canceled) {
			t.Errorf("late connect error = %v, want context cancellation", result.err)
		}
	}
	lifecycleWait(t, late.closed)
	deadline := time.Now().Add(time.Second)
	for runtime.NumGoroutine() > baseline && time.Now().Before(deadline) {
		runtime.Gosched()
	}
	if got := runtime.NumGoroutine(); got > baseline {
		t.Errorf("completed custom dial left workers: baseline %d, got %d", baseline, got)
	}
}

func TestBindLifecycleDialErrorClosesReturnedConnection(t *testing.T) {
	client, peer := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = peer.Close() }()
	connection := &bindLateConnection{Conn: client, closed: make(chan struct{})}
	dialError := errors.New("dial failed after creating a connection")
	connector := TRXConnector(DialerFunc(func(context.Context, string) (net.Conn, error) {
		return connection, dialError
	}), Auth{})
	conn, err := connector.Connect()
	if conn != nil || !errors.Is(err, dialError) {
		t.Errorf("Connect = (%v, %v), want nil connection and dial error", conn, err)
	}
	select {
	case <-connection.closed:
	default:
		t.Error("a dial error leaked the connection returned with it")
	}
}

type bindOwnedDialer struct {
	entered  chan struct{}
	canceled chan struct{}
	release  chan struct{}
	finished chan struct{}
}

func (d *bindOwnedDialer) builtinDialer() {}

func (d *bindOwnedDialer) DialContext(ctx context.Context, _ string) (net.Conn, error) {
	defer close(d.finished)
	close(d.entered)
	<-ctx.Done()
	close(d.canceled)
	<-d.release
	return nil, ctx.Err()
}

func TestBindLifecycleCancellationJoinsOwnedDialer(t *testing.T) {
	dialer := &bindOwnedDialer{entered: make(chan struct{}), canceled: make(chan struct{}), release: make(chan struct{}), finished: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	connector := TRXConnector(dialer, Auth{})
	results := make(chan bindConnectResult, 1)
	returned := make(chan struct{})
	go func() {
		conn, err := connector.(contextConnector).ConnectContext(ctx)
		results <- bindConnectResult{conn: conn, err: err}
		close(returned)
	}()
	lifecycleWait(t, dialer.entered)
	cancel()
	lifecycleWait(t, dialer.canceled)
	lifecycleAwaitJoin(t, returned, "connectContext(")
	close(dialer.release)
	if result := waitBindResult(t, results); !errors.Is(result.err, context.Canceled) {
		t.Errorf("connect error = %v, want context cancellation", result.err)
	}
	lifecycleWait(t, dialer.finished)
}

func TestBindLifecycleGenericNackNullSequencePreservesStatus(t *testing.T) {
	for _, sequence := range []string{"null", "matching", "unrelated"} {
		t.Run(sequence, func(t *testing.T) {
			result := exchangeBindResponse(t, func(request *pdu.BindRequest) pdu.PDU {
				nack := pdu.NewGenericNack()
				nack.SetCommandStatus(data.ESME_RINVCMDLEN)
				switch sequence {
				case "null":
					nack.SetSequenceNumber(0)
				case "matching":
					nack.SetSequenceNumber(request.GetSequenceNumber())
				case "unrelated":
					nack.SetSequenceNumber(request.GetSequenceNumber() + 1)
				}
				return nack
			})
			var bindError BindError
			if sequence == "unrelated" {
				if result.err == nil || errors.As(result.err, &bindError) {
					t.Errorf("unrelated nack error = %v, want sequence mismatch", result.err)
				}
				return
			}
			if !errors.As(result.err, &bindError) || bindError.CommandStatus != data.ESME_RINVCMDLEN {
				t.Errorf("bind error = %v (%T), want BindError carrying ESME_RINVCMDLEN", result.err, result.err)
			}
		})
	}
}

func TestBindLifecycleAnswersEnquireLinkBeforeBindResponse(t *testing.T) {
	client, peer := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = peer.Close() }()
	peerDone := make(chan error, 1)
	go func() {
		request, err := pdu.Parse(peer)
		if err != nil {
			peerDone <- err
			return
		}
		connection := NewConnection(peer)
		for _, sequence := range []int32{100, 101} {
			link := pdu.NewEnquireLink()
			link.SetSequenceNumber(sequence)
			if _, err = connection.WritePDU(link); err != nil {
				peerDone <- err
				return
			}
			response, parseErr := pdu.Parse(peer)
			if parseErr != nil {
				peerDone <- parseErr
				return
			}
			if response.GetHeader().CommandID != data.ENQUIRE_LINK_RESP || response.GetSequenceNumber() != sequence || !response.IsOk() {
				peerDone <- errors.New("enquire_link during bind was not answered correctly")
				return
			}
		}
		response := request.GetResponse().(*pdu.BindResp)
		response.SystemID = "smsc"
		_, err = connection.WritePDU(response)
		peerDone <- err
	}()
	results := make(chan bindConnectResult, 1)
	go func() {
		conn, err := connect(DialerFunc(func(context.Context, string) (net.Conn, error) { return client, nil }), "synthetic", pdu.NewBindRequest(pdu.Transceiver))
		results <- bindConnectResult{conn: conn, err: err}
	}()
	result := waitBindResult(t, results)
	if result.err != nil {
		t.Errorf("bind with enquire_link failed: %v", result.err)
	} else if result.conn.systemID != "smsc" {
		t.Errorf("bound system ID = %q, want smsc", result.conn.systemID)
	}
	if err := <-peerDone; err != nil {
		t.Errorf("peer bind exchange: %v", err)
	}
}
