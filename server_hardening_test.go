package gosmpp

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"math/big"
	"net"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gomaja/go-smpp/data"
	"github.com/gomaja/go-smpp/pdu"
)

// pipeListener exercises the accept loop without requiring TCP sockets.
type pipeListener struct {
	conns    chan net.Conn
	done     chan struct{}
	once     sync.Once
	failures int
}

func newPipeListener() *pipeListener {
	return &pipeListener{conns: make(chan net.Conn), done: make(chan struct{})}
}
func (l *pipeListener) Accept() (net.Conn, error) {
	if l.failures > 0 {
		l.failures--
		return nil, errors.New("synthetic accept failure")
	}
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}
func (l *pipeListener) Close() error   { l.once.Do(func() { close(l.done) }); return nil }
func (l *pipeListener) Addr() net.Addr { return unboundAddr{} }
func pipeServer(t *testing.T, settings ServerSettings, failures int) (*Server, *pipeListener) {
	t.Helper()
	srv := NewServer(settings)
	l := newPipeListener()
	l.failures = failures
	srv.listener = l
	if settings.TLSConfig != nil {
		srv.listener = tls.NewListener(l, settings.TLSConfig)
	}
	srv.started = true
	srv.active.Store(true)
	srv.wg.Add(1)
	go srv.acceptLoop()
	t.Cleanup(srv.Stop)
	return srv, l
}
func pipeDial(t *testing.T, l *pipeListener) net.Conn {
	t.Helper()
	c, s := net.Pipe()
	select {
	case l.conns <- s:
	case <-time.After(time.Second):
		_ = c.Close()
		_ = s.Close()
		t.Fatal("accept loop stopped")
	}
	t.Cleanup(func() { _ = c.Close() })
	_ = c.SetDeadline(time.Now().Add(time.Second))
	return c
}
func pipeAccount(t *testing.T, srv *Server, bt pdu.BindingType, read time.Duration) {
	t.Helper()
	if err := srv.StartSystemID(SystemIDConfig{SystemID: "sys", Password: "pw", BindingTypes: []pdu.BindingType{bt}, ReadTimeout: read, WriteTimeout: time.Second}); err != nil {
		t.Fatal(err)
	}
}
func writeWire(t *testing.T, c net.Conn, p pdu.PDU) {
	t.Helper()
	b := pdu.NewBuffer(nil)
	p.Marshal(b)
	if _, err := c.Write(b.Bytes()); err != nil {
		t.Fatal(err)
	}
}
func readWire(t *testing.T, c net.Conn) []byte {
	t.Helper()
	var h [16]byte
	if _, err := io.ReadFull(c, h[:]); err != nil {
		t.Fatal(err)
	}
	n := binary.BigEndian.Uint32(h[:4])
	if n < 16 || n > 65536 {
		t.Fatalf("bad length %d", n)
	}
	b := make([]byte, n)
	copy(b, h[:])
	if _, err := io.ReadFull(c, b[16:]); err != nil {
		t.Fatal(err)
	}
	return b
}
func bindPipe(t *testing.T, c net.Conn, bt pdu.BindingType, version byte) []byte {
	t.Helper()
	r := pdu.NewBindRequest(bt)
	r.SystemID = "sys"
	r.Password = "pw"
	r.InterfaceVersion = version
	r.SetSequenceNumber(72)
	writeWire(t, c, r)
	return readWire(t, c)
}
func await(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition did not settle")
		}
		time.Sleep(time.Millisecond)
	}
}
func TestServerRegistrationRequiresRunningIO(t *testing.T) {
	srv := NewServer(ServerSettings{})
	pipeAccount(t, srv, pdu.Transceiver, time.Second)
	c, peer := net.Pipe()
	defer func() { _ = peer.Close() }()
	s := newServerSession(c, srv)
	s.systemID = "sys"
	s.account = srv.credStore["sys"]
	srv.addSession("sys", s)
	if len(srv.GetSystemIDStatus("sys")) != 0 {
		t.Fatal("session visible without running transceivable")
	}
	s.close()
}

// gateConn pauses a successful bind write or deadline handoff. Closing its
// transport remains effective while the connection goroutine is paused.
type gateConn struct {
	net.Conn
	entered   chan struct{}
	release   chan struct{}
	writeGate bool
	once      sync.Once
}

func (c *gateConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	if c.writeGate {
		c.once.Do(func() { close(c.entered); <-c.release })
	}
	return n, err
}
func (c *gateConn) SetDeadline(d time.Time) error {
	if !c.writeGate && d.IsZero() {
		c.once.Do(func() { close(c.entered); <-c.release })
	}
	return c.Conn.SetDeadline(d)
}
func gatedDial(t *testing.T, l *pipeListener, writeGate bool) (net.Conn, *gateConn) {
	t.Helper()
	c, s := net.Pipe()
	g := &gateConn{Conn: s, entered: make(chan struct{}), release: make(chan struct{}), writeGate: writeGate}
	l.conns <- g
	t.Cleanup(func() { _ = c.Close() })
	_ = c.SetDeadline(time.Now().Add(time.Second))
	return c, g
}
func waitSignal(t *testing.T, c <-chan struct{}) {
	t.Helper()
	select {
	case <-c:
	case <-time.After(time.Second):
		t.Fatal("signal timed out")
	}
}

// abortGateConn pauses the shutdown transport close before serverSession.close
// marks the session closed. That exposes removal-to-abort registration races.
type abortGateConn struct {
	*gateConn
	closeEntered chan struct{}
	closeRelease chan struct{}
	closeCalls   atomic.Int32
}

func (c *abortGateConn) Close() error {
	if c.closeCalls.Add(1) == 1 {
		close(c.closeEntered)
		<-c.closeRelease
	}
	return c.Conn.Close()
}

func TestServerRegistrationRechecksAccount(t *testing.T) {
	serverTLS, clientTLS := testTLSConfigs(t)
	srv, l := pipeServer(t, ServerSettings{TLSConfig: serverTLS}, 0)
	pipeAccount(t, srv, pdu.Transceiver, 0)
	c, peer := net.Pipe()
	g := &abortGateConn{gateConn: &gateConn{Conn: peer, entered: make(chan struct{}), release: make(chan struct{})}, closeEntered: make(chan struct{}), closeRelease: make(chan struct{})}
	var releaseBind, releaseClose sync.Once
	t.Cleanup(func() {
		releaseBind.Do(func() { close(g.release) })
		releaseClose.Do(func() { close(g.closeRelease) })
	})
	l.conns <- g
	client := tls.Client(c, clientTLS)
	defer func() { _ = c.Close() }()
	_ = client.SetDeadline(time.Now().Add(time.Second))
	bindPipe(t, client, pdu.Transceiver, 0x34)
	waitSignal(t, g.entered)
	srv.mu.Lock()
	var session *serverSession
	for s := range srv.connections {
		session = s
	}
	srv.mu.Unlock()
	if session == nil {
		t.Fatal("reserved connection missing")
	}
	stopped := make(chan struct{})
	go func() { srv.StopSystemID("sys"); close(stopped) }()
	waitSignal(t, g.closeEntered)
	// The account is removed but abort has not yet marked the session closed.
	pipeAccount(t, srv, pdu.Transceiver, 0)
	releaseBind.Do(func() { close(g.release) })
	await(t, func() bool { return session.closed.Load() || len(srv.GetSystemIDStatus("sys")) != 0 })
	if len(srv.GetSystemIDStatus("sys")) != 0 {
		t.Fatal("stopped account acquired a session before abort")
	}
	// Read TLS close_notify so the owner can finish while abort remains paused.
	assertConnectionClosed(t, client)
	waitSignal(t, session.finished)
	releaseClose.Do(func() { close(g.closeRelease) })
	waitSignal(t, stopped)
}

func TestServerSlotBelongsToOriginalAccount(t *testing.T) {
	srv, l := pipeServer(t, ServerSettings{}, 0)
	pipeAccount(t, srv, pdu.Transceiver, time.Second)
	srv.mu.Lock()
	old := srv.credStore["sys"]
	srv.mu.Unlock()
	c, g := gatedDial(t, l, true)
	bindPipe(t, c, pdu.Transceiver, 0x34)
	waitSignal(t, g.entered)
	srv.StopSystemID("sys")
	pipeAccount(t, srv, pdu.Transceiver, time.Second)
	bindPipe(t, pipeDial(t, l), pdu.Transceiver, 0x34)
	await(t, func() bool { return len(srv.GetSystemIDStatus("sys")) == 1 })
	close(g.release)
	await(t, func() bool { srv.mu.Lock(); defer srv.mu.Unlock(); return old.currentSession == 0 })
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if srv.credStore["sys"].currentSession != 1 {
		t.Fatalf("new account count=%d, want 1", srv.credStore["sys"].currentSession)
	}
}
func TestServerHousekeepingWire(t *testing.T) {
	for _, kind := range []string{"enquire", "unbind", "bind", "fallback"} {
		t.Run(kind, func(t *testing.T) {
			srv, l := pipeServer(t, ServerSettings{}, 0)
			pipeAccount(t, srv, pdu.Transceiver, time.Second)
			c := pipeDial(t, l)
			bindPipe(t, c, pdu.Transceiver, 0x34)
			var req pdu.PDU
			switch kind {
			case "enquire":
				req = pdu.NewEnquireLink()
			case "unbind":
				req = pdu.NewUnbind()
			case "bind":
				req = pdu.NewBindRequest(pdu.Receiver)
			default:
				req = pdu.NewSubmitSM()
			}
			req.SetSequenceNumber(123)
			writeWire(t, c, req)
			raw := readWire(t, c)
			if binary.BigEndian.Uint32(raw[4:8]) != uint32(req.GetHeader().CommandID)|0x80000000 || binary.BigEndian.Uint32(raw[12:16]) != 123 {
				t.Fatalf("incorrect response %x", raw)
			}
			status := uint32(0)
			if kind == "bind" {
				status = uint32(data.ESME_RALYBND)
			}
			if kind == "fallback" {
				status = uint32(data.ESME_RSYSERR)
			}
			if binary.BigEndian.Uint32(raw[8:12]) != status {
				t.Fatalf("incorrect status %x", raw)
			}
			if kind == "bind" && len(raw) != 16 {
				t.Fatalf("second bind response length %d", len(raw))
			}
		})
	}
}
func TestServerOwnIdentifierWire(t *testing.T) {
	srv, l := pipeServer(t, ServerSettings{}, 0)
	pipeAccount(t, srv, pdu.Transceiver, time.Second)
	raw := bindPipe(t, pipeDial(t, l), pdu.Transceiver, 0x34)
	if raw[16] != 0 {
		t.Fatalf("bind response echoes ESME ID: %x", raw[16:])
	}
}
func TestServerBindVersionWire(t *testing.T) {
	for _, bt := range []pdu.BindingType{pdu.Receiver, pdu.Transmitter, pdu.Transceiver} {
		for _, v := range []byte{0x33, 0x34} {
			t.Run(bt.String()+string([]byte{v}), func(t *testing.T) {
				srv, l := pipeServer(t, ServerSettings{}, 0)
				pipeAccount(t, srv, bt, time.Second)
				raw := bindPipe(t, pipeDial(t, l), bt, v)
				end := bytes.IndexByte(raw[16:], 0) + 17
				want := []byte(nil)
				if v >= 0x34 {
					want = []byte{2, 16, 0, 1, 0x34}
				}
				if !bytes.Equal(raw[end:], want) {
					t.Fatalf("version %x TLV = %x, want %x", v, raw[end:], want)
				}
			})
		}
	}
}
func TestServerErrorBindHeaderOnly(t *testing.T) {
	srv, l := pipeServer(t, ServerSettings{}, 0)
	pipeAccount(t, srv, pdu.Transceiver, time.Second)
	c := pipeDial(t, l)
	r := pdu.NewBindRequest(pdu.Transceiver)
	r.SystemID = "sys"
	r.Password = "wrong"
	writeWire(t, c, r)
	raw := readWire(t, c)
	if len(raw) != 16 {
		t.Fatalf("error bind command_length=%d, want 16", len(raw))
	}
}

// Timing benchmarks cannot reliably prove constant-time comparison. Check the
// security primitive as well as testing credential acceptance on the wire.
func TestServerPasswordUsesConstantTimeCompare(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	f, err := parser.ParseFile(token.NewFileSet(), "server.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		c, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		s, ok := c.Fun.(*ast.SelectorExpr)
		if ok && s.Sel.Name == "ConstantTimeCompare" {
			found = true
		}
		return true
	})
	if !found {
		t.Fatal("password validation does not use ConstantTimeCompare")
	}
}
func TestServerZeroReadTimeout(t *testing.T) {
	srv, l := pipeServer(t, ServerSettings{}, 0)
	pipeAccount(t, srv, pdu.Transceiver, 0)
	c := pipeDial(t, l)
	bindPipe(t, c, pdu.Transceiver, 0x34)
	writeWire(t, c, pdu.NewEnquireLink())
	readWire(t, c)
}
func TestServerStopClosesBoundAndHandshaking(t *testing.T) {
	srv, l := pipeServer(t, ServerSettings{BindTimeout: time.Second}, 0)
	pipeAccount(t, srv, pdu.Transceiver, time.Second)
	bound := pipeDial(t, l)
	bindPipe(t, bound, pdu.Transceiver, 0x34)
	await(t, func() bool { return len(srv.GetSystemIDStatus("sys")) == 1 })
	pending := pipeDial(t, l)
	done := make(chan struct{})
	go func() { srv.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		_ = pending.Close()
		t.Fatal("Stop waited for bind timeout")
	}
	assertConnectionClosed(t, bound)
}
func TestServerAcceptRetriesNonTimeout(t *testing.T) {
	errs := make(chan error, 10)
	srv, l := pipeServer(t, ServerSettings{OnError: func(err error) { errs <- err }}, 3)
	pipeAccount(t, srv, pdu.Transceiver, time.Second)
	bindPipe(t, pipeDial(t, l), pdu.Transceiver, 0x34)
	if len(errs) != 3 {
		t.Fatalf("reported %d accept errors, want 3", len(errs))
	}
}

func TestServerSubmitSkipsTransmitter(t *testing.T) {
	srv, l := pipeServer(t, ServerSettings{}, 0)
	pipeAccount(t, srv, pdu.Transmitter, time.Second)
	bindPipe(t, pipeDial(t, l), pdu.Transmitter, 0x34)
	await(t, func() bool { return len(srv.GetSystemIDStatus("sys")) == 1 })
	if err := srv.SubmitToSystemID(context.Background(), "sys", pdu.NewDeliverSM()); !errors.Is(err, ErrNoLiveSession) {
		t.Fatalf("transmitter selected: %v", err)
	}
}
func TestServerEmptyBindingListAllowsAll(t *testing.T) {
	// Zero-value binding configuration must allow TX, RX and TRX.
	for _, bt := range []pdu.BindingType{pdu.Transmitter, pdu.Receiver, pdu.Transceiver} {
		t.Run(bt.String(), func(t *testing.T) {
			srv, l := pipeServer(t, ServerSettings{}, 0)
			if err := srv.StartSystemID(SystemIDConfig{SystemID: "sys", Password: "pw", ReadTimeout: time.Second, WriteTimeout: time.Second}); err != nil {
				t.Fatal(err)
			}
			raw := bindPipe(t, pipeDial(t, l), bt, 0x34)
			if binary.BigEndian.Uint32(raw[8:12]) != 0 {
				t.Fatalf("default account rejected %v", bt)
			}
		})
	}
}

func TestServerConfigValidation(t *testing.T) {
	cases := []struct {
		name   string
		config SystemIDConfig
	}{
		{"emptyID", SystemIDConfig{}},
		{"negativeMax", SystemIDConfig{SystemID: "sys", MaxSessions: -1}},
		{"equalKeepAlive", SystemIDConfig{SystemID: "sys", ReadTimeout: time.Second, EnquireLink: time.Second}},
		{"longKeepAlive", SystemIDConfig{SystemID: "sys", ReadTimeout: time.Second, EnquireLink: 2 * time.Second}},
		{"unknownBinding", SystemIDConfig{SystemID: "sys", BindingTypes: []pdu.BindingType{99}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := NewServer(ServerSettings{})
			if err := srv.StartSystemID(tc.config); err == nil {
				t.Fatal("invalid account accepted")
			}
		})
	}
	srv := NewServer(ServerSettings{})
	allowed := []pdu.BindingType{pdu.Receiver}
	cfg := SystemIDConfig{SystemID: "sys", Password: "credentials-longer-than-spec", BindingTypes: allowed, EnquireLink: time.Second}
	if err := srv.StartSystemID(cfg); err != nil {
		t.Fatal(err)
	}
	allowed[0] = pdu.Transmitter
	if srv.credStore["sys"].config.BindingTypes[0] != pdu.Receiver {
		t.Fatal("caller can mutate binding allow-list")
	}
	if err := srv.StartSystemID(cfg); err == nil {
		t.Fatal("duplicate account accepted")
	}
	for _, id := range []string{strings.Repeat("x", 16), "bad\x00id"} {
		invalid := NewServer(ServerSettings{SystemID: id})
		if err := invalid.Start(); err == nil {
			invalid.Stop()
			t.Fatal("invalid SMSC ID accepted")
		}
	}
}

func TestServerStartCannotRepeat(t *testing.T) {
	srv, _ := pipeServer(t, ServerSettings{}, 0)
	if err := srv.Start(); err == nil {
		t.Fatal("second Start succeeded")
	}
	srv.Stop()
	if err := srv.Start(); err == nil {
		t.Fatal("Start after Stop succeeded")
	}
	fresh := NewServer(ServerSettings{})
	fresh.Stop()
	if err := fresh.Start(); err == nil {
		t.Fatal("Start after Stop before Start succeeded")
	}
	if fresh.Addr() != nil {
		t.Fatal("unstarted server has address")
	}
}

func TestServerSubmitAfterBindHasRunningIO(t *testing.T) {
	srv, l := pipeServer(t, ServerSettings{}, 0)
	pipeAccount(t, srv, pdu.Transceiver, 0)
	c := pipeDial(t, l)
	bindPipe(t, c, pdu.Transceiver, 0x34)
	await(t, func() bool { return len(srv.GetSystemIDStatus("sys")) == 1 })
	srv.mu.Lock()
	for s := range srv.sessions["sys"] {
		if s.trx.Load() == nil {
			t.Error("session has no I/O")
		}
		select {
		case <-s.trx.Load().started:
		default:
			t.Error("transceivable not started")
		}
	}
	srv.mu.Unlock()
	d := pdu.NewDeliverSM()
	d.SetSequenceNumber(219)
	if err := srv.SubmitToSystemID(context.Background(), "sys", d); err != nil {
		t.Fatal(err)
	}
	raw := readWire(t, c)
	if binary.BigEndian.Uint32(raw[4:8]) != uint32(data.DELIVER_SM) || binary.BigEndian.Uint32(raw[12:]) != 219 {
		t.Fatalf("incorrect delivery %x", raw)
	}
}

func TestServerHousekeepingWithHandler(t *testing.T) {
	srv, l := pipeServer(t, ServerSettings{}, 0)
	calls := make(chan pdu.PDU, 10)
	err := srv.StartSystemID(SystemIDConfig{SystemID: "sys", Password: "pw", WriteTimeout: time.Second, OnPDU: func(_ ServerSessionInfo, p pdu.PDU) (pdu.PDU, bool) { calls <- p; return p.GetResponse(), false }})
	if err != nil {
		t.Fatal(err)
	}
	c := pipeDial(t, l)
	bindPipe(t, c, pdu.Transceiver, 0x34)
	for _, req := range []pdu.PDU{pdu.NewEnquireLink(), pdu.NewBindRequest(pdu.Receiver), pdu.NewBindRequest(pdu.Transmitter), pdu.NewBindRequest(pdu.Transceiver)} {
		req.SetSequenceNumber(111)
		writeWire(t, c, req)
		raw := readWire(t, c)
		if binary.BigEndian.Uint32(raw[12:]) != 111 {
			t.Fatalf("wrong sequence: %x", raw)
		}
		if _, ok := req.(*pdu.BindRequest); ok && (len(raw) != 16 || binary.BigEndian.Uint32(raw[8:12]) != uint32(data.ESME_RALYBND)) {
			t.Fatalf("bad repeated bind %x", raw)
		}
	}
	select {
	case <-calls:
		t.Fatal("housekeeping reached application")
	default:
	}
	writeWire(t, c, pdu.NewSubmitSM())
	readWire(t, c)
	waitPDU(t, calls)
	writeWire(t, c, pdu.NewDeliverSMResp())
	waitPDU(t, calls)
	writeWire(t, c, pdu.NewUnbind())
	raw := readWire(t, c)
	if binary.BigEndian.Uint32(raw[4:8]) != uint32(int64(data.UNBIND_RESP)&0xffffffff) {
		t.Fatalf("wrong unbind response %x", raw)
	}
	_, err = c.Read(make([]byte, 1))
	if !errors.Is(err, io.EOF) {
		t.Fatalf("unbind did not close after response: %v", err)
	}
}
func waitPDU(t *testing.T, c <-chan pdu.PDU) {
	t.Helper()
	select {
	case <-c:
	case <-time.After(time.Second):
		t.Fatal("handler not invoked")
	}
}

func TestServerNilHandlerDropsResponses(t *testing.T) {
	srv, l := pipeServer(t, ServerSettings{}, 0)
	pipeAccount(t, srv, pdu.Transceiver, 0)
	c := pipeDial(t, l)
	bindPipe(t, c, pdu.Transceiver, 0x34)
	writeWire(t, c, pdu.NewDeliverSMResp())
	req := pdu.NewEnquireLink()
	req.SetSequenceNumber(333)
	writeWire(t, c, req)
	raw := readWire(t, c)
	if binary.BigEndian.Uint32(raw[4:8]) != uint32(int64(data.ENQUIRE_LINK_RESP)&0xffffffff) || binary.BigEndian.Uint32(raw[12:]) != 333 {
		t.Fatalf("response was not dropped: %x", raw)
	}
}

func TestServerErrorCallbacksHaveContext(t *testing.T) {
	errs := make(chan error, 10)
	srv, l := pipeServer(t, ServerSettings{OnError: func(err error) { errs <- err }}, 0)
	pipeAccount(t, srv, pdu.Transceiver, 0)
	c := pipeDial(t, l)
	bindPipe(t, c, pdu.Transceiver, 0x34)
	await(t, func() bool { return len(srv.GetSystemIDStatus("sys")) == 1 })
	_ = c.Close()
	select {
	case err := <-errs:
		if !strings.Contains(err.Error(), `system ID "sys" remote pipe`) || strings.Contains(err.Error(), "pw") {
			t.Fatalf("unsafe or missing context: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("read error not reported")
	}
}

func TestServerConcurrentLifecycle(t *testing.T) {
	baseline := runtime.NumGoroutine()
	for range 10 {
		srv, l := pipeServer(t, ServerSettings{}, 0)
		pipeAccount(t, srv, pdu.Transceiver, 0)
		srv.mu.Lock()
		accounts := []*credentials{srv.credStore["sys"]}
		srv.mu.Unlock()
		clients := make(chan net.Conn, 8)
		var wg sync.WaitGroup
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				c, peer := net.Pipe()
				_ = c.SetDeadline(time.Now().Add(time.Second))
				select {
				case l.conns <- peer:
				case <-time.After(time.Second):
					_ = c.Close()
					_ = peer.Close()
					return
				}
				req := pdu.NewBindRequest(pdu.Transceiver)
				req.SystemID, req.Password = "sys", "pw"
				b := pdu.NewBuffer(nil)
				req.Marshal(b)
				if _, err := c.Write(b.Bytes()); err == nil {
					if resp, err := pdu.Parse(c); err == nil && resp.IsOk() {
						clients <- c
						return
					}
				}
				_ = c.Close()
			}()
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 8 {
				srv.StopSystemID("sys")
				if err := srv.StartSystemID(SystemIDConfig{SystemID: "sys", Password: "pw"}); err == nil {
					srv.mu.Lock()
					accounts = append(accounts, srv.credStore["sys"])
					srv.mu.Unlock()
				}
			}
		}()
		wg.Wait()
		// Ensure there is a completed live bind before checking the quiet state.
		live := pipeDial(t, l)
		bindPipe(t, live, pdu.Transceiver, 0x34)
		await(t, func() bool {
			srv.mu.Lock()
			defer srv.mu.Unlock()
			for s := range srv.connections {
				if s.closed.Load() {
					return false
				}
				if _, ok := srv.sessions[s.systemID][s]; !ok {
					return false
				}
			}
			return len(srv.sessions["sys"]) > 0
		})
		checkServerAccountSlots(t, srv, accounts)
		close(clients)
		var disconnect sync.WaitGroup
		for c := range clients {
			disconnect.Add(1)
			go func() { defer disconnect.Done(); _ = c.Close() }()
		}
		// A second wave races new handshakes with global shutdown, after the
		// first wave's quiet-state invariant has been checked.
		for range 8 {
			disconnect.Add(1)
			go func() {
				defer disconnect.Done()
				c, peer := net.Pipe()
				defer func() { _ = c.Close() }()
				_ = c.SetDeadline(time.Now().Add(time.Second))
				select {
				case l.conns <- peer:
				case <-l.done:
					_ = peer.Close()
					return
				}
				req := pdu.NewBindRequest(pdu.Transceiver)
				req.SystemID, req.Password = "sys", "pw"
				b := pdu.NewBuffer(nil)
				req.Marshal(b)
				if _, err := c.Write(b.Bytes()); err == nil {
					_, _ = pdu.Parse(c)
				}
			}()
		}
		disconnect.Add(2)
		go func() { defer disconnect.Done(); srv.StopSystemID("sys") }()
		go func() { defer disconnect.Done(); srv.Stop() }()
		disconnect.Wait()
		srv.Stop()
		checkServerAccountSlots(t, srv, accounts)
		srv.mu.Lock()
		if len(srv.connections) != 0 || len(srv.sessions) != 0 {
			t.Error("connections survived Stop")
		}
		srv.mu.Unlock()
	}
	await(t, func() bool { return runtime.NumGoroutine() <= baseline })
}

func checkServerAccountSlots(t *testing.T, srv *Server, accounts []*credentials) {
	t.Helper()
	srv.mu.Lock()
	defer srv.mu.Unlock()
	registered := make(map[*credentials]int)
	for id, sessions := range srv.sessions {
		for s := range sessions {
			if s.account == nil || srv.credStore[id] != s.account {
				t.Errorf("orphaned registered account for %s", id)
			}
			registered[s.account]++
		}
	}
	for _, account := range accounts {
		if account.currentSession != registered[account] {
			t.Errorf("account %s slots=%d sessions=%d", account.config.SystemID, account.currentSession, registered[account])
		}
	}
}

func FuzzServerFirstPDU(f *testing.F) {
	req := pdu.NewBindRequest(pdu.Transceiver)
	req.SystemID = "sys"
	req.Password = "pw"
	b := pdu.NewBuffer(nil)
	req.Marshal(b)
	f.Add(b.Bytes())
	f.Add(b.Bytes()[:18])
	f.Add([]byte{0x7f, 0xff, 0xff, 0xff, 0, 0, 0, 9, 0, 0, 0, 0, 0, 0, 0, 1})
	b = pdu.NewBuffer(nil)
	pdu.NewEnquireLink().Marshal(b)
	f.Add(b.Bytes())
	f.Fuzz(func(t *testing.T, raw []byte) {
		srv, l := pipeServer(t, ServerSettings{BindTimeout: 100 * time.Millisecond}, 0)
		pipeAccount(t, srv, pdu.Transceiver, 0)
		c := pipeDial(t, l)
		finished := make(chan struct{})
		go func() { defer close(finished); _, _ = c.Write(raw); _ = c.Close() }()
		waitSignal(t, finished)
		valid := pipeDial(t, l)
		response := bindPipe(t, valid, pdu.Transceiver, 0x34)
		if binary.BigEndian.Uint32(response[8:12]) != 0 {
			t.Fatalf("server unusable after input %x", raw)
		}
		_ = valid.Close()
		srv.Stop()
	})
}

func TestServerRegistrationRejectsReplacedAccount(t *testing.T) {
	srv := NewServer(ServerSettings{})
	pipeAccount(t, srv, pdu.Transceiver, 0)
	c, peer := net.Pipe()
	s := newServerSession(c, srv)
	s.systemID = "sys"
	s.account = srv.credStore["sys"]
	s.config = s.account.config
	s.bindingType = pdu.Transceiver
	if err := s.startIO(); err != nil {
		t.Fatal(err)
	}
	defer func() { s.close(); _ = peer.Close(); _ = s.trx.Load().Close() }()
	srv.StopSystemID("sys")
	pipeAccount(t, srv, pdu.Transceiver, 0)
	if srv.addSession("sys", s) {
		t.Fatal("registration accepted a replaced account")
	}
}

func TestServerVersionGatesAllOptionalParameters(t *testing.T) {
	for _, version := range []byte{0x33, 0x34} {
		t.Run(fmt.Sprintf("%x", version), func(t *testing.T) {
			srv, l := pipeServer(t, ServerSettings{}, 0)
			cfg := SystemIDConfig{SystemID: "sys", Password: "pw", WriteTimeout: time.Second, OnPDU: func(_ ServerSessionInfo, p pdu.PDU) (pdu.PDU, bool) {
				r := p.GetResponse()
				r.RegisterOptionalParam(pdu.NewFieldUint8(pdu.TagMoreMessagesToSend, 1))
				return r, false
			}}
			if err := srv.StartSystemID(cfg); err != nil {
				t.Fatal(err)
			}
			c := pipeDial(t, l)
			bindPipe(t, c, pdu.Transceiver, version)
			await(t, func() bool { return len(srv.GetSystemIDStatus("sys")) == 1 })
			request := pdu.NewSubmitSM()
			writeWire(t, c, request)
			raw := readWire(t, c)
			parsed, err := pdu.Parse(bytes.NewReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			response := parsed.(*pdu.SubmitSMResp)
			if (len(response.OptionalParameters) > 0) != (version >= 0x34) {
				t.Fatalf("v%x application response TLVs: %x", version, raw)
			}
			delivery := pdu.NewDeliverSM().(*pdu.DeliverSM)
			delivery.RegisterOptionalParam(pdu.NewFieldUint8(pdu.TagMoreMessagesToSend, 1))
			err = srv.SubmitToSystemID(context.Background(), "sys", delivery)
			if version < 0x34 {
				var versionErr *UnsupportedOptionalParametersError
				if !errors.As(err, &versionErr) {
					t.Fatalf("old peer delivery error: %v", err)
				}
				assertNoWire(t, c)
				if len(delivery.OptionalParameters) != 1 {
					t.Fatal("server mutated caller's PDU")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			raw = readWire(t, c)
			parsed, err = pdu.Parse(bytes.NewReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			if (len(parsed.(*pdu.DeliverSM).OptionalParameters) > 0) != (version >= 0x34) {
				t.Fatalf("v%x delivery TLVs: %x", version, raw)
			}
			if len(delivery.OptionalParameters) != 1 {
				t.Fatal("server mutated caller's PDU")
			}
		})
	}
}

func TestSessionStatusAfterPeerDisconnect(t *testing.T) {
	c, peer := net.Pipe()
	session := &Session{}
	closed := make(chan struct{})
	var once sync.Once
	trx := newTransceivable(NewConnection(c), Settings{ReadTimeout: time.Second, WriteTimeout: time.Second, OnClosed: func(State) { once.Do(func() { close(closed) }) }}, nil)
	session.trx.Store(trx)
	trx.start()
	_ = peer.Close()
	waitSignal(t, closed)
	defer func() { _ = trx.Close() }()
	if session.IsAlive() || session.SessionStatus() != SessionClosed {
		t.Fatal("disconnected session still reports active")
	}
}

func TestServerOwnNonEmptyIdentifierWire(t *testing.T) {
	for _, id := range []string{"smsc", strings.Repeat("x", 15)} {
		t.Run(id, func(t *testing.T) {
			srv, l := pipeServer(t, ServerSettings{SystemID: id}, 0)
			pipeAccount(t, srv, pdu.Transceiver, 0)
			raw := bindPipe(t, pipeDial(t, l), pdu.Transceiver, 0x34)
			if !bytes.Equal(raw[16:len(raw)-5], append([]byte(id), 0)) {
				t.Fatalf("SMSC identifier incorrect: %x", raw)
			}
		})
	}
}

func TestServerSubmitErrorHasContext(t *testing.T) {
	errs := make(chan error, 10)
	srv, l := pipeServer(t, ServerSettings{OnError: func(err error) { errs <- err }}, 0)
	if err := srv.StartSystemID(SystemIDConfig{SystemID: "sys", Password: "pw", WriteTimeout: 20 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	c := pipeDial(t, l)
	bindPipe(t, c, pdu.Transceiver, 0x34)
	await(t, func() bool { return len(srv.GetSystemIDStatus("sys")) == 1 })
	if err := srv.SubmitToSystemID(context.Background(), "sys", pdu.NewDeliverSM()); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errs:
		var netErr net.Error
		if !errors.As(err, &netErr) || !netErr.Timeout() || !strings.Contains(err.Error(), `system ID "sys" remote pipe`) {
			t.Fatalf("incorrect submit error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("submit error not reported")
	}
	await(t, func() bool { return len(srv.GetSystemIDStatus("sys")) == 0 })
}

type shortWriteConn struct{ net.Conn }

func (shortWriteConn) Write(b []byte) (int, error) { return len(b) - 1, nil }
func TestConnectionReportsShortPDUWrite(t *testing.T) {
	c := NewConnection(shortWriteConn{})
	if _, err := c.WritePDU(pdu.NewEnquireLink()); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short write was accepted: %v", err)
	}
}

type deadlineErrorConn struct{ net.Conn }

var errTestDeadline = errors.New("synthetic deadline failure")

func (deadlineErrorConn) SetWriteDeadline(time.Time) error { return errTestDeadline }
func TestServerResponseDeadlineError(t *testing.T) {
	errs := make(chan error, 10)
	srv, l := pipeServer(t, ServerSettings{OnError: func(err error) { errs <- err }}, 0)
	pipeAccount(t, srv, pdu.Transceiver, 0)
	c, peer := net.Pipe()
	l.conns <- deadlineErrorConn{peer}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(time.Second))
	bindPipe(t, c, pdu.Transceiver, 0x34)
	writeWire(t, c, pdu.NewEnquireLink())
	select {
	case err := <-errs:
		if !errors.Is(err, errTestDeadline) {
			t.Fatalf("wrong error: %v", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("response deadline error was discarded")
	}
}

type observeDeadlineConn struct {
	net.Conn
	deadlines chan time.Time
	writes    chan struct{}
}

func (c *observeDeadlineConn) SetWriteDeadline(d time.Time) error {
	c.deadlines <- d
	return c.Conn.SetWriteDeadline(d)
}
func (c *observeDeadlineConn) Write(b []byte) (int, error) {
	select {
	case c.writes <- struct{}{}:
	default:
	}
	return c.Conn.Write(b)
}
func TestServerDeadlineSetupSerializedWithWrite(t *testing.T) {
	srv, l := pipeServer(t, ServerSettings{}, 0)
	received := make(chan struct{})
	if err := srv.StartSystemID(SystemIDConfig{SystemID: "sys", Password: "pw", WriteTimeout: time.Second, OnPDU: func(_ ServerSessionInfo, p pdu.PDU) (pdu.PDU, bool) { close(received); return p.GetResponse(), false }}); err != nil {
		t.Fatal(err)
	}
	c, peer := net.Pipe()
	observed := &observeDeadlineConn{Conn: peer, deadlines: make(chan time.Time, 10), writes: make(chan struct{}, 10)}
	l.conns <- observed
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(time.Second))
	bindPipe(t, c, pdu.Transceiver, 0x34)
	waitSignal(t, observed.writes) // bind response
	await(t, func() bool { return len(srv.GetSystemIDStatus("sys")) == 1 })
	if err := srv.SubmitToSystemID(context.Background(), "sys", pdu.NewDeliverSM()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-observed.deadlines:
	case <-time.After(time.Second):
		t.Fatal("delivery deadline was not set")
	}
	waitSignal(t, observed.writes)
	writeWire(t, c, pdu.NewSubmitSM())
	waitSignal(t, received)
	// A response waiting for the active delivery write must not reset that
	// delivery's deadline. Await the forbidden event with a bounded deadline.
	select {
	case <-observed.deadlines:
		t.Fatal("response reset the active delivery's deadline")
	case <-time.After(50 * time.Millisecond):
	}
	readWire(t, c)
	readWire(t, c)
}

func testTLSConfigs(t *testing.T) (*tls.Config, *tls.Config) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), DNSNames: []string{"localhost"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, public, private)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(certificate)
	return &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: private}}, MinVersion: tls.VersionTLS13}, &tls.Config{ServerName: "localhost", RootCAs: roots, MinVersion: tls.VersionTLS13}
}
func TestServerTLSBindAndStop(t *testing.T) {
	serverConfig, clientConfig := testTLSConfigs(t)
	srv, l := pipeServer(t, ServerSettings{TLSConfig: serverConfig}, 0)
	pipeAccount(t, srv, pdu.Transceiver, 0)
	raw := pipeDial(t, l)
	client := tls.Client(raw, clientConfig)
	if err := client.Handshake(); err != nil {
		t.Fatal(err)
	}
	response := bindPipe(t, client, pdu.Transceiver, 0x34)
	if binary.BigEndian.Uint32(response[8:12]) != 0 {
		t.Fatalf("TLS bind rejected: %x", response)
	}
	await(t, func() bool { return len(srv.GetSystemIDStatus("sys")) == 1 })
	stopped := make(chan struct{})
	go func() { srv.Stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(200 * time.Millisecond):
		_ = raw.Close()
		t.Fatal("TLS close_notify blocked Stop")
	}
}

func TestServerMalformedBoundPDUClosesWithoutWriteDeadline(t *testing.T) {
	srv, l := pipeServer(t, ServerSettings{}, 0)
	if err := srv.StartSystemID(SystemIDConfig{SystemID: "sys", Password: "pw", MaxSessions: 1}); err != nil {
		t.Fatal(err)
	}
	c := pipeDial(t, l)
	bindPipe(t, c, pdu.Transceiver, 0x34)
	await(t, func() bool { return len(srv.GetSystemIDStatus("sys")) == 1 })
	raw := make([]byte, 16)
	binary.BigEndian.PutUint32(raw[:4], 15)
	if _, err := c.Write(raw); err != nil {
		t.Fatal(err)
	}
	await(t, func() bool {
		srv.mu.Lock()
		defer srv.mu.Unlock()
		return srv.credStore["sys"].currentSession == 0 && len(srv.sessions["sys"]) == 0
	})
}

func TestServerRegistrationRequiresStartedTransceivable(t *testing.T) {
	srv := NewServer(ServerSettings{})
	pipeAccount(t, srv, pdu.Transceiver, 0)
	c, peer := net.Pipe()
	defer func() { _ = peer.Close() }()
	s := newServerSession(c, srv)
	s.systemID = "sys"
	s.account = srv.credStore["sys"]
	trx := newTransceivable(NewConnection(c), Settings{}, nil)
	s.trx.Store(trx)
	defer func() { s.close(); _ = trx.Close() }()
	if srv.addSession("sys", s) {
		t.Fatal("unstarted transceivable registered")
	}
}

func TestServerLongESMECredentials(t *testing.T) {
	srv, l := pipeServer(t, ServerSettings{}, 0)
	cfg := SystemIDConfig{SystemID: strings.Repeat("s", 32), Password: strings.Repeat("p", 32)}
	if err := srv.StartSystemID(cfg); err != nil {
		t.Fatal(err)
	}
	c := pipeDial(t, l)
	request := pdu.NewBindRequest(pdu.Transceiver)
	request.SystemID = cfg.SystemID
	request.Password = cfg.Password
	writeWire(t, c, request)
	raw := readWire(t, c)
	if binary.BigEndian.Uint32(raw[8:12]) != 0 {
		t.Fatalf("long ESME credentials rejected: %x", raw)
	}
}

func TestServerConcurrentSlotCap(t *testing.T) {
	srv, l := pipeServer(t, ServerSettings{}, 0)
	const limit = 4
	if err := srv.StartSystemID(SystemIDConfig{SystemID: "sys", Password: "pw", MaxSessions: limit}); err != nil {
		t.Fatal(err)
	}
	var clients, attempted sync.WaitGroup
	release := make(chan struct{})
	results := make(chan uint32, 12)
	clients.Add(12)
	attempted.Add(12)
	for range 12 {
		go func() {
			defer clients.Done()
			c, s := net.Pipe()
			defer func() { _ = c.Close() }()
			_ = c.SetDeadline(time.Now().Add(time.Second))
			l.conns <- s
			request := pdu.NewBindRequest(pdu.Transceiver)
			request.SystemID = "sys"
			request.Password = "pw"
			b := pdu.NewBuffer(nil)
			request.Marshal(b)
			status := uint32(data.ESME_RSYSERR)
			if _, err := c.Write(b.Bytes()); err == nil {
				if response, err := pdu.Parse(c); err == nil {
					status = uint32(response.GetHeader().CommandStatus)
				}
			}
			results <- status
			attempted.Done()
			<-release
		}()
	}
	attempted.Wait()
	close(results)
	successes := 0
	for status := range results {
		if status == 0 {
			successes++
		} else if status != uint32(data.ESME_RBINDFAIL) {
			t.Errorf("unexpected rejection %x", status)
		}
	}
	await(t, func() bool { return len(srv.GetSystemIDStatus("sys")) == successes })
	srv.mu.Lock()
	slots := srv.credStore["sys"].currentSession
	srv.mu.Unlock()
	close(release)
	clients.Wait()
	if successes != limit || slots != limit {
		t.Fatalf("cap admitted %d, reserved %d, want %d", successes, slots, limit)
	}
	await(t, func() bool { srv.mu.Lock(); defer srv.mu.Unlock(); return srv.credStore["sys"].currentSession == 0 })
}
