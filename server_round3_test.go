package gosmpp

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gomaja/go-smpp/data"
	"github.com/gomaja/go-smpp/pdu"
)

func TestServerReceiverDataSMWithoutHandler(t *testing.T) {
	srv, l := pipeServer(t, ServerSettings{}, 0)
	pipeAccount(t, srv, pdu.Receiver, 0)
	c := pipeDial(t, l)
	bindPipe(t, c, pdu.Receiver, 0x34)
	req := pdu.NewDataSM()
	req.SetSequenceNumber(703)
	writeWire(t, c, req)
	raw := readWire(t, c)
	commandID := req.GetResponse().GetHeader().CommandID
	if len(raw) != 16 || binary.BigEndian.Uint32(raw[:4]) != 16 || binary.BigEndian.Uint32(raw[4:8]) != uint32(commandID) || binary.BigEndian.Uint32(raw[8:12]) != uint32(data.ESME_RINVBNDSTS) || binary.BigEndian.Uint32(raw[12:]) != 703 {
		t.Fatalf("invalid RX data_sm response: %x", raw)
	}
}

func TestServerUnbindResponseWriteFailureReported(t *testing.T) {
	errs := make(chan error, 10)
	srv, l := pipeServer(t, ServerSettings{OnError: func(err error) { errs <- err }}, 0)
	pipeAccount(t, srv, pdu.Transceiver, 0)
	c, peer := net.Pipe()
	l.conns <- deadlineErrorConn{peer}
	t.Cleanup(func() { _ = c.Close() })
	if err := c.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	bindPipe(t, c, pdu.Transceiver, 0x34)
	trx := testServerTransceiver(t, srv)
	writeWire(t, c, pdu.NewUnbind())
	awaitServerIOExit(t, srv, trx)
	if len(errs) != 1 {
		t.Fatalf("failed unbind_resp reported %d errors, want 1", len(errs))
	}
	if err := <-errs; !errors.Is(err, errTestDeadline) {
		t.Fatalf("wrong unbind_resp error: %v", err)
	}
}

type unbindWriteGateConn struct {
	net.Conn
	entered chan struct{}
	release chan struct{}
}

func (c *unbindWriteGateConn) Write(b []byte) (int, error) {
	commandID := data.UNBIND_RESP
	if len(b) >= 16 && binary.BigEndian.Uint32(b[4:8]) == uint32(commandID) {
		close(c.entered)
		<-c.release
	}
	return c.Conn.Write(b)
}

func TestServerUnbindClosesAfterResponseWrite(t *testing.T) {
	srv, l := pipeServer(t, ServerSettings{}, 0)
	pipeAccount(t, srv, pdu.Transceiver, 0)
	c, peer := net.Pipe()
	g := &unbindWriteGateConn{Conn: peer, entered: make(chan struct{}), release: make(chan struct{})}
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(g.release) }) })
	t.Cleanup(func() { _ = c.Close() })
	l.conns <- g
	if err := c.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	bindPipe(t, c, pdu.Transceiver, 0x34)
	trx := testServerTransceiver(t, srv)
	srv.mu.Lock()
	var session *serverSession
	for s := range srv.sessions["sys"] {
		session = s
	}
	srv.mu.Unlock()
	writeWire(t, c, pdu.NewUnbind())
	waitSignal(t, g.entered)
	if session.closing.Load() || session.closed.Load() {
		t.Fatal("session marked closing before unbind_resp write completed")
	}
	release.Do(func() { close(g.release) })
	readWire(t, c)
	assertConnectionClosed(t, c)
	awaitServerIOExit(t, srv, trx)
}

func TestServerUnbindDoesNotHideDeliveryTimeout(t *testing.T) {
	errs := make(chan error, 10)
	srv, l := pipeServer(t, ServerSettings{OnError: func(err error) { errs <- err }}, 0)
	if err := srv.StartSystemID(SystemIDConfig{SystemID: "sys", Password: "pw", WriteTimeout: 100 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	c, peer := net.Pipe()
	observed := &observeDeadlineConn{Conn: peer, deadlines: make(chan time.Time, 10), writes: make(chan struct{}, 10)}
	l.conns <- observed
	t.Cleanup(func() { _ = c.Close() })
	if err := c.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	bindPipe(t, c, pdu.Transceiver, 0x34)
	waitSignal(t, observed.writes) // bind_resp
	trx := testServerTransceiver(t, srv)
	if err := srv.SubmitToSystemID(context.Background(), "sys", pdu.NewDeliverSM()); err != nil {
		t.Fatal(err)
	}
	waitSignal(t, observed.writes) // Delivery is stuck because the peer does not read.
	writeWire(t, c, pdu.NewUnbind())
	awaitServerIOExit(t, srv, trx)
	if len(errs) != 1 {
		t.Fatalf("delivery timeout during unbind reported %d errors, want 1", len(errs))
	}
	var netErr net.Error
	if err := <-errs; !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("expected delivery write timeout: %v", err)
	}
}

func TestServerDeliveryFailureReportedAfterUnbind(t *testing.T) {
	errs := make(chan error, 10)
	srv, l := pipeServer(t, ServerSettings{OnError: func(err error) { errs <- err }}, 0)
	if err := srv.StartSystemID(SystemIDConfig{SystemID: "sys", Password: "pw", WriteTimeout: 50 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	c := pipeDial(t, l)
	bindPipe(t, c, pdu.Transceiver, 0x34)
	trx := testServerTransceiver(t, srv)
	reportEntered, reportRelease := make(chan struct{}), make(chan struct{})
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(reportRelease) }) })
	report := trx.out.settings.OnSubmitError
	// Delay notification after the failed write released the connection lock.
	// Installing before Submit orders this assignment before the daemon reads it.
	trx.out.settings.OnSubmitError = func(p pdu.PDU, err error) {
		close(reportEntered)
		<-reportRelease
		report(p, err)
	}
	if err := srv.SubmitToSystemID(context.Background(), "sys", pdu.NewDeliverSM()); err != nil {
		t.Fatal(err)
	}
	waitSignal(t, reportEntered)
	writeWire(t, c, pdu.NewUnbind())
	readWire(t, c)
	assertConnectionClosed(t, c)
	release.Do(func() { close(reportRelease) })
	awaitServerIOExit(t, srv, trx)
	if len(errs) != 1 {
		t.Fatalf("delivery failure preceding unbind closure reported %d errors, want 1", len(errs))
	}
	var netErr net.Error
	if err := <-errs; !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("expected original delivery timeout: %v", err)
	}
}

func TestServerWriteFailureReportedOnceTCP(t *testing.T) {
	errs := make(chan error, 10)
	srv, addr := startTestServer(t, ServerSettings{OnError: func(err error) { errs <- err }})
	if err := srv.StartSystemID(SystemIDConfig{SystemID: "sys", Password: "pw", WriteTimeout: 50 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	c, resp := bind(t, addr, "sys", "pw", pdu.Transceiver)
	if resp.CommandStatus != data.ESME_ROK {
		t.Fatal(resp.CommandStatus)
	}
	if err := c.(*net.TCPConn).SetReadBuffer(4096); err != nil {
		t.Fatal(err)
	}
	trx := testServerTransceiver(t, srv)
	// Keep the peer open without reading, filling the socket buffers until a
	// real write timeout closes the session and interrupts its TCP reader.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for trx.isAlive() {
		d := pdu.NewDeliverSM().(*pdu.DeliverSM)
		if err := d.Message.SetMessageWithEncoding(strings.Repeat("x", 150), data.ASCII); err != nil {
			t.Fatal(err)
		}
		err := srv.SubmitToSystemID(ctx, "sys", d)
		if err != nil {
			if ctx.Err() != nil {
				t.Fatalf("socket did not reach a write timeout before the overall deadline: %v", err)
			}
			break
		}
	}
	awaitServerIOExit(t, srv, trx)
	var timeouts int
	seen := make(map[int32]bool)
	for len(errs) > 0 {
		err := <-errs
		var submit *SubmitError
		if !errors.As(err, &submit) || submit.PDU == nil || seen[submit.PDU.GetSequenceNumber()] {
			t.Fatalf("missing or repeated failed PDU: %v", err)
		}
		seen[submit.PDU.GetSequenceNumber()] = true
		if errors.Is(err, ErrConnectionClosing) {
			continue
		}
		var netErr net.Error
		if !errors.As(err, &netErr) || !netErr.Timeout() || !strings.Contains(err.Error(), "write tcp") {
			t.Fatalf("expected TCP write timeout: %v", err)
		}
		timeouts++
	}
	if timeouts != 1 {
		t.Fatalf("TCP write timeouts reported %d times, want 1", timeouts)
	}
}

func testServerTransceiver(t *testing.T, srv *Server) *transceivable {
	t.Helper()
	waitForSessions(t, srv, "sys", 1)
	srv.mu.Lock()
	defer srv.mu.Unlock()
	for s := range srv.sessions["sys"] {
		return s.trx.Load()
	}
	t.Fatal("bound session missing")
	return nil
}

// awaitServerIOExit verifies transport shutdown and completion of both workers.
func awaitServerIOExit(t *testing.T, srv *Server, trx *transceivable) {
	t.Helper()
	await(t, func() bool { srv.mu.Lock(); defer srv.mu.Unlock(); return len(srv.connections) == 0 })
	finished := make(chan struct{})
	go func() {
		trx.in.wg.Wait()
		trx.out.wg.Wait()
		close(finished)
	}()
	waitSignal(t, finished)
}
