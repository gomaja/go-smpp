package gosmpp

import (
	"errors"
	"io"
	"net"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gomaja/go-smpp/data"
	"github.com/gomaja/go-smpp/pdu"
)

// Await the caller's actual join rather than using a delay to assume that it
// blocked. The release gates below keep the target callback alive throughout.
func lifecycleAwaitJoin(t *testing.T, returned <-chan struct{}, frame string) bool {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-returned:
			t.Error("shutdown returned before the target callback finished")
			return false
		default:
		}
		buf := make([]byte, 1<<20)
		stacks := string(buf[:runtime.Stack(buf, true)])
		for _, stack := range strings.Split(stacks, "\n\n") {
			if strings.Contains(stack, "[chan receive]") && strings.Contains(stack, frame) {
				return true
			}
		}
		runtime.Gosched()
	}
	t.Error("shutdown did not enter its join")
	return false
}

func TestLifecycleCloseFromOtherSessionCallbackJoins(t *testing.T) {
	connB, cleanupB := newPipePair()
	defer cleanupB()
	entered, release, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	b, err := NewSession(lifecycleConnector{connect: func() (*Connection, error) { return connB, nil }}, Settings{
		ReadTimeout: time.Hour, OnClosed: func(State) { close(entered); <-release },
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	clientA, peerA := net.Pipe()
	defer func() { _ = peerA.Close() }()
	a, err := NewSession(lifecycleConnector{connect: func() (*Connection, error) { return NewConnection(clientA), nil }}, Settings{
		ReadTimeout: time.Hour, OnPDU: func(pdu.PDU, bool) { _ = b.Close(); close(returned) },
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { unblock(); _ = a.Close(); _ = b.Close() }()
	writeWire(t, peerA, pdu.NewEnquireLinkResp())
	lifecycleWait(t, entered)
	lifecycleAwaitJoin(t, returned, "(*Session).Close(")
	unblock()
	lifecycleWait(t, returned)
}

func TestSessionLifecycleCloseFromOwnCallback(t *testing.T) {
	client, peer := net.Pipe()
	defer func() { _ = peer.Close() }()
	done := make(chan struct{})
	var s *Session
	var err error
	s, err = NewSession(lifecycleConnector{connect: func() (*Connection, error) { return NewConnection(client), nil }}, Settings{
		ReadTimeout: time.Hour, OnPDU: func(pdu.PDU, bool) { _ = s.Close(); close(done) },
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	writeWire(t, peer, pdu.NewEnquireLinkResp())
	lifecycleWait(t, done)
	_ = s.Close()
}

func TestLifecycleCloseFromOtherConnectionCallbackJoins(t *testing.T) {
	for _, direction := range []string{"transceiver", "transmitter", "receiver"} {
		t.Run(direction, func(t *testing.T) {
			conn, cleanup := newPipePair()
			defer cleanup()
			entered, release, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			defer unblock()
			settings := Settings{ReadTimeout: time.Hour, OnClosed: func(State) { close(entered); <-release }}
			var closeTarget func() error
			frame := "(*transceivable).closing("
			switch direction {
			case "transceiver":
				v := newTransceivable(conn, settings, nil)
				v.start()
				closeTarget = v.Close
			case "transmitter":
				v := newTransmittable(conn, settings, nil)
				v.start()
				closeTarget = func() error { return v.close(ExplicitClosing) }
				frame = "(*transmittable).close("
			case "receiver":
				v := newReceivable(conn, settings, nil)
				v.start()
				closeTarget = func() error { return v.close(ExplicitClosing) }
				frame = "(*receivable).close("
			}
			client, peer := net.Pipe()
			defer func() { _ = peer.Close() }()
			a := newTransceivable(NewConnection(client), Settings{ReadTimeout: time.Hour,
				OnPDU: func(pdu.PDU, bool) { _ = closeTarget(); close(returned) },
			}, nil)
			a.start()
			defer func() { unblock(); _ = a.Close(); _ = closeTarget() }()
			writeWire(t, peer, pdu.NewEnquireLinkResp())
			lifecycleWait(t, entered)
			lifecycleAwaitJoin(t, returned, frame)
			unblock()
			lifecycleWait(t, returned)
		})
	}
}

func TestServerLifecycleStopFromOtherServerCallbackJoins(t *testing.T) {
	entered, release, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	b, addr := startTestServer(t, ServerSettings{})
	if err := b.StartSystemID(SystemIDConfig{SystemID: "sys", Password: "pw",
		OnPDU: func(_ ServerSessionInfo, p pdu.PDU) (pdu.PDU, bool) {
			close(entered)
			<-release
			return p.GetResponse(), false
		},
	}); err != nil {
		t.Fatal(err)
	}
	esme, _ := bind(t, addr, "sys", "pw", pdu.Transceiver)
	writeWire(t, esme, pdu.NewSubmitSM())
	lifecycleWait(t, entered)
	a := NewServer(ServerSettings{OnError: func(error) { b.Stop(); close(returned) }})
	defer func() { unblock(); a.Stop() }()
	go a.reportError(io.EOF)
	lifecycleAwaitJoin(t, returned, "(*Server).Stop(")
	unblock()
	lifecycleWait(t, returned)
}

func TestServerLifecycleStopFromOtherSessionCallbackJoins(t *testing.T) {
	for _, accountOnly := range []bool{false, true} {
		name := "server"
		if accountOnly {
			name = "account"
		}
		t.Run(name, func(t *testing.T) {
			entered, release, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			defer unblock()
			srv, addr := startTestServer(t, ServerSettings{})
			if err := srv.StartSystemID(SystemIDConfig{SystemID: "sys", Password: "pw",
				OnPDU: func(_ ServerSessionInfo, p pdu.PDU) (pdu.PDU, bool) {
					close(entered)
					<-release
					return p.GetResponse(), false
				},
			}); err != nil {
				t.Fatal(err)
			}
			esme, _ := bind(t, addr, "sys", "pw", pdu.Transceiver)
			writeWire(t, esme, pdu.NewSubmitSM())
			lifecycleWait(t, entered)
			client, peer := net.Pipe()
			defer func() { _ = peer.Close() }()
			c, err := NewSession(lifecycleConnector{connect: func() (*Connection, error) { return NewConnection(client), nil }}, Settings{
				ReadTimeout: time.Hour, OnPDU: func(pdu.PDU, bool) {
					if accountOnly {
						srv.StopSystemID("sys")
					} else {
						srv.Stop()
					}
					close(returned)
				},
			}, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { unblock(); _ = c.Close() }()
			writeWire(t, peer, pdu.NewEnquireLinkResp())
			frame := "(*Server).Stop("
			if accountOnly {
				frame = "(*Server).StopSystemID("
			}
			lifecycleAwaitJoin(t, returned, frame)
			unblock()
			lifecycleWait(t, returned)
		})
	}
}

func TestSessionLifecycleCloseSendsUnbind(t *testing.T) {
	client, peer := net.Pipe()
	defer func() { _ = peer.Close() }()
	got := make(chan []data.CommandIDType, 1)
	go func() {
		var ids []data.CommandIDType
		for {
			p, err := pdu.Parse(peer)
			if err != nil {
				got <- ids
				return
			}
			ids = append(ids, p.GetHeader().CommandID)
		}
	}()
	s, err := NewSession(lifecycleConnector{connect: func() (*Connection, error) { return NewConnection(client), nil }}, Settings{ReadTimeout: time.Hour}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case ids := <-got:
		if len(ids) != 1 || ids[0] != data.UNBIND {
			t.Fatalf("peer received %v before EOF; want one unbind", ids)
		}
	case <-time.After(time.Second):
		t.Fatal("peer did not observe EOF")
	}
}

func TestLifecycleTransmitterCloseSendsUnbind(t *testing.T) {
	client, peer := net.Pipe()
	defer func() { _ = peer.Close() }()
	got := make(chan pdu.PDU, 1)
	go func() { p, _ := pdu.Parse(peer); got <- p }()
	writer := newTransmittable(NewConnection(client), Settings{}, nil)
	writer.start()
	if err := writer.close(ExplicitClosing); err != nil {
		t.Fatal(err)
	}
	select {
	case p := <-got:
		if p == nil || p.GetHeader().CommandID != data.UNBIND {
			t.Fatalf("peer received %v, want unbind", p)
		}
	case <-time.After(time.Second):
		t.Fatal("peer received no shutdown PDU")
	}
}

func TestSessionLifecycleExplicitCloseNotifiesWithAutoRebind(t *testing.T) {
	conn, cleanup := newPipePair()
	defer cleanup()
	var states []State
	s, err := NewSession(lifecycleConnector{connect: func() (*Connection, error) { return conn, nil }}, Settings{
		ReadTimeout: time.Hour, OnClosed: func(state State) { states = append(states, state) },
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if len(states) != 1 || states[0] != ExplicitClosing {
		t.Fatalf("OnClosed states %v, want ExplicitClosing", states)
	}
}

func TestSessionLifecycleCloseBoundsIdleUnbind(t *testing.T) {
	client, peer := net.Pipe()
	defer func() { _ = peer.Close() }()
	s, err := NewSession(lifecycleConnector{connect: func() (*Connection, error) { return NewConnection(client), nil }}, Settings{ReadTimeout: time.Hour}, 0)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = s.Close(); close(done) }()
	lifecycleWait(t, done)
	if _, err := peer.Write([]byte{1}); err != io.ErrClosedPipe {
		t.Fatalf("peer write after close: %v", err)
	}
}

func TestLifecycleUnbindSkipsActiveWrite(t *testing.T) {
	client, peer := net.Pipe()
	defer func() { _ = peer.Close() }()
	entered := make(chan struct{})
	conn := NewConnection(&lifecycleWriteConn{Conn: client, entered: entered})
	written, unbound := make(chan struct{}), make(chan struct{})
	go func() { _, _ = conn.WritePDU(pdu.NewEnquireLink()); close(written) }()
	lifecycleWait(t, entered)
	go func() { conn.tryUnbind(); close(unbound) }()
	select {
	case <-unbound:
	case <-time.After(time.Second):
		t.Error("unbind queued behind an active socket write")
	}
	_ = conn.Close()
	lifecycleWait(t, written)
	lifecycleWait(t, unbound)
}

type lifecycleCountingWritesConn struct {
	net.Conn
	writes atomic.Int32
}

func (c *lifecycleCountingWritesConn) Write(b []byte) (int, error) {
	c.writes.Add(1)
	return len(b), nil
}

func TestLifecycleShutdownRejectsWriterWaitingForTransport(t *testing.T) {
	for _, timed := range []bool{false, true} {
		name := "WritePDU"
		if timed {
			name = "writePDUWithTimeout"
		}
		t.Run(name, func(t *testing.T) {
			client, peer := net.Pipe()
			defer func() { _ = peer.Close() }()
			raw := &lifecycleCountingWritesConn{Conn: client}
			conn := NewConnection(raw)
			defer func() { _ = conn.Close() }()
			conn.writeMu.Lock()
			result := make(chan error, 1)
			go func() {
				var err error
				if timed {
					_, err = conn.writePDUWithTimeout(pdu.NewSubmitSM(), time.Second)
				} else {
					_, err = conn.WritePDU(pdu.NewSubmitSM())
				}
				result <- err
			}()
			conn.tryUnbind()
			conn.writeMu.Unlock()
			select {
			case err := <-result:
				if !errors.Is(err, ErrConnectionClosing) || raw.writes.Load() != 0 {
					t.Fatalf("writer crossed shutdown: error=%v socket writes=%d", err, raw.writes.Load())
				}
			case <-time.After(time.Second):
				t.Fatal("waiting writer did not finish")
			}
		})
	}
}

func TestServerLifecycleIntentionalCloseReportsOnlyUnsentSubmissions(t *testing.T) {
	client, peer := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = peer.Close() }()
	errs := make(chan error, 3)
	srv := NewServer(ServerSettings{OnError: func(err error) { errs <- err }})
	session := newServerSession(client, srv)
	session.closing.Store(true)
	response := pdu.NewEnquireLink().GetResponse()
	session.reportResponseError(response, ErrConnectionClosing)
	session.reportIOError(ErrConnectionClosing)
	if len(errs) != 0 {
		t.Error("intentional shutdown reported a response or receive error")
	}
	request := pdu.NewDeliverSM()
	session.reportSubmitError(request, ErrConnectionClosing)
	select {
	case err := <-errs:
		var submit *SubmitError
		if !errors.As(err, &submit) || submit.PDU != request || !errors.Is(err, ErrConnectionClosing) {
			t.Fatalf("unsent submission error: %v", err)
		}
	default:
		t.Fatal("intentional shutdown dropped an unsent submission")
	}
}
