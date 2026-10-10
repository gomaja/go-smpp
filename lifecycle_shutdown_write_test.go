package gosmpp

import (
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gomaja/go-smpp/pdu"
)

// The shutdown deadline releases an active write with a transport error. The
// read-deadline gate makes that write release the Connection lock before the
// explicit-close path can consider writing unbind.
type lifecycleInterruptedWriteConn struct {
	net.Conn
	connection        *Connection
	entered           chan struct{}
	released          chan struct{}
	releaseOnce       sync.Once
	writes            atomic.Int32
	interruptedWrites atomic.Int32
	partial           bool
}

func (c *lifecycleInterruptedWriteConn) Write(b []byte) (int, error) {
	if c.writes.Add(1) == 1 {
		close(c.entered)
		<-c.released
		if c.partial {
			return len(b) / 2, errors.New("interrupted partial write")
		}
		return 0, errors.New("interrupted transport write")
	}
	return len(b), nil
}

func (c *lifecycleInterruptedWriteConn) SetWriteDeadline(deadline time.Time) error {
	if !deadline.IsZero() && !deadline.After(time.Now()) {
		c.releaseOnce.Do(func() { close(c.released) })
	}
	return c.Conn.SetWriteDeadline(deadline)
}

func (c *lifecycleInterruptedWriteConn) SetReadDeadline(deadline time.Time) error {
	if !deadline.IsZero() && !deadline.After(time.Now()) {
		c.connection.writeMu.Lock()
		c.interruptedWrites.Store(c.writes.Load())
		c.connection.writeMu.Unlock()
	}
	return c.Conn.SetReadDeadline(deadline)
}

func TestLifecycleInterruptedWriteSkipsShutdownUnbind(t *testing.T) {
	for _, partial := range []bool{false, true} {
		name := "zero_bytes"
		if partial {
			name = "partial_frame"
		}
		t.Run(name, func(t *testing.T) {
			client, peer := net.Pipe()
			raw := &lifecycleInterruptedWriteConn{Conn: client, entered: make(chan struct{}), released: make(chan struct{}), partial: partial}
			connection := NewConnection(raw)
			raw.connection = connection
			request := pdu.NewSubmitSM()
			type failure struct {
				p   pdu.PDU
				err error
			}
			reported := make(chan failure, 2)
			trx := newTransceivable(connection, Settings{ReadTimeout: time.Hour,
				OnSubmitError: func(p pdu.PDU, err error) { reported <- failure{p, err} },
			}, nil)
			trx.start()
			t.Cleanup(func() { raw.releaseOnce.Do(func() { close(raw.released) }); _ = peer.Close(); _ = trx.Close() })
			if err := trx.Submit(request); err != nil {
				t.Fatal(err)
			}
			lifecycleWait(t, raw.entered)
			if err := trx.Close(); err != nil {
				t.Fatal(err)
			}
			if writes := raw.interruptedWrites.Load(); writes != 1 {
				t.Fatalf("shutdown write barrier observed %d transport writes, want 1", writes)
			}
			if writes := raw.writes.Load(); writes != 1 {
				t.Fatalf("shutdown appended unbind after an interrupted write: transport writes=%d, want 1", writes)
			}
			select {
			case failure := <-reported:
				if failure.p != request || !errors.Is(failure.err, ErrConnectionClosing) {
					t.Fatalf("interrupted write callback = %T, %v", failure.p, failure.err)
				}
			default:
				t.Fatal("interrupted PDU was not reported")
			}
			select {
			case failure := <-reported:
				t.Fatalf("interrupted PDU completed again: %T, %v", failure.p, failure.err)
			default:
			}
		})
	}
}

type lifecycleSubmitFailureConn struct{ net.Conn }

func (c lifecycleSubmitFailureConn) Write([]byte) (int, error) {
	return 0, errors.New("transport write failed")
}

func TestSessionLifecycleCloseFromSubmitErrorCallback(t *testing.T) {
	client, peer := net.Pipe()
	defer func() { _ = peer.Close() }()
	callbackReturned := make(chan struct{})
	var session *Session
	var err error
	session, err = NewSession(lifecycleConnector{connect: func() (*Connection, error) {
		return NewConnection(lifecycleSubmitFailureConn{client}), nil
	}}, Settings{ReadTimeout: time.Hour,
		OnSubmitError: func(pdu.PDU, error) { _ = session.Close(); close(callbackReturned) },
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	if err := session.Transceiver().Submit(pdu.NewSubmitSM()); err != nil {
		t.Fatal(err)
	}
	lifecycleWait(t, callbackReturned)
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
}

type lifecycleShutdownCountingConn struct {
	net.Conn
	writes atomic.Int32
}

func (c *lifecycleShutdownCountingConn) Write(b []byte) (int, error) {
	c.writes.Add(1)
	return len(b), nil
}

func TestSessionLifecycleEncodeFailureStillSendsShutdownUnbind(t *testing.T) {
	client, peer := net.Pipe()
	defer func() { _ = peer.Close() }()
	raw := &lifecycleShutdownCountingConn{Conn: client}
	reported := make(chan error, 1)
	session, err := NewSession(lifecycleConnector{connect: func() (*Connection, error) {
		return NewConnection(raw), nil
	}}, Settings{ReadTimeout: time.Hour,
		OnSubmitError: func(_ pdu.PDU, err error) { reported <- err },
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	invalid := pdu.NewSubmitSM().(*pdu.SubmitSM)
	invalid.ServiceType = "invalid\x00service"
	if err := session.Transceiver().Submit(invalid); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-reported:
		if !errors.Is(err, pdu.ErrEncode) {
			t.Fatalf("invalid PDU error = %v, want ErrEncode", err)
		}
	case <-time.After(time.Second):
		t.Fatal("encoding failure was not reported")
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if writes := raw.writes.Load(); writes != 1 {
		t.Fatalf("encoding failure prevented the shutdown unbind: transport writes=%d, want 1", writes)
	}
}
