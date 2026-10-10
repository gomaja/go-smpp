package gosmpp

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// Hand-written SMPP v3.4 bind_transceiver: sys, pw, empty system_type,
// interface_version 0x34, unknown TON/NPI, empty address_range, sequence 0x345.
const openBindRequestHex = "0000001c000000090000000000000345737973007077000034000000"
const openBindResponseHex = "0000001a800000090000000000000345736d7363000210000134"

func openBindBytes(t *testing.T, wire string) []byte {
	t.Helper()
	b, err := hex.DecodeString(wire)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func openBindWrite(t *testing.T, conn net.Conn, wire string) {
	t.Helper()
	b := openBindBytes(t, wire)
	n, err := conn.Write(b)
	if err != nil || n != len(b) {
		t.Fatalf("write raw frame: %d/%d bytes, %v", n, len(b), err)
	}
}

func openBindRead(t *testing.T, conn net.Conn, wire string) {
	t.Helper()
	want := openBindBytes(t, wire)
	var header [16]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		t.Fatalf("read response header: %v", err)
	}
	length := binary.BigEndian.Uint32(header[:4])
	if length < 16 || length > 1024 {
		t.Fatalf("invalid response length %d", length)
	}
	got := make([]byte, int(length))
	copy(got, header[:])
	if _, err := io.ReadFull(conn, got[16:]); err != nil {
		t.Fatalf("read response body: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("response bytes: got %x, want %x", got, want)
	}
}

type openBindDeadlineConn struct {
	net.Conn
	mu        sync.Mutex
	deadlines []time.Time
	writeErr  error
}

func (c *openBindDeadlineConn) SetDeadline(deadline time.Time) error {
	c.mu.Lock()
	c.deadlines = append(c.deadlines, deadline)
	c.mu.Unlock()
	return c.Conn.SetDeadline(deadline)
}

func (c *openBindDeadlineConn) Write(b []byte) (int, error) {
	if c.writeErr != nil {
		return 0, c.writeErr
	}
	return c.Conn.Write(b)
}

func openBindPipe(t *testing.T, timeout time.Duration, writeErr error) (net.Conn, *serverSession, *openBindDeadlineConn, <-chan error) {
	t.Helper()
	serverConn, peer := net.Pipe()
	observed := &openBindDeadlineConn{Conn: serverConn, writeErr: writeErr}
	srv := NewServer(ServerSettings{SystemID: "smsc", BindTimeout: timeout})
	if err := srv.StartSystemID(SystemIDConfig{SystemID: "sys", Password: "pw"}); err != nil {
		t.Fatal(err)
	}
	session := newServerSession(observed, srv)
	done := make(chan error, 1)
	t.Cleanup(func() { _ = peer.Close(); _ = serverConn.Close() })
	if err := peer.SetDeadline(time.Now().Add(testBindWait)); err != nil {
		t.Fatal(err)
	}
	go func() {
		err := session.handleBind()
		done <- err
		_ = serverConn.Close()
	}()
	return peer, session, observed, done
}

func openBindResult(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(testBindWait):
		t.Fatal("bind wait did not finish")
		return nil
	}
}

func TestServerOpenBindEnquireLinkThenBind(t *testing.T) {
	peer, session, _, done := openBindPipe(t, time.Second, nil)
	// SMPP v5 §2.4 Table 2-1 permits enquiry in Open; v3.4 §4.11
	// requires a success response with the request's sequence.
	openBindWrite(t, peer, "00000010000000150000000000012345")
	openBindRead(t, peer, "00000010800000150000000000012345")
	if got := currentSessions(t, session.server, "sys"); got != 0 {
		t.Fatalf("enquiry reserved an account slot: %d", got)
	}
	openBindWrite(t, peer, openBindRequestHex)
	openBindRead(t, peer, openBindResponseHex)
	if err := openBindResult(t, done); err != nil {
		t.Fatalf("bind after enquiry: %v", err)
	}
	if session.systemID != "sys" {
		t.Fatalf("bound account = %q", session.systemID)
	}
}

func TestServerOpenBindGenericNackThenBind(t *testing.T) {
	for _, nack := range []string{
		"00000010800000000000000300000000",
		"00000010800000000000000300000345",
		"00000010800000000000000300000027",
	} {
		t.Run(nack, func(t *testing.T) {
			peer, _, _, done := openBindPipe(t, time.Second, nil)
			// Pipeline nack and bind: the first response must be bind_resp,
			// proving nack neither terminates the wait nor gets a response.
			openBindWrite(t, peer, nack+openBindRequestHex)
			openBindRead(t, peer, openBindResponseHex)
			if err := openBindResult(t, done); err != nil {
				t.Fatalf("bind after generic_nack: %v", err)
			}
		})
	}
}

func TestServerOpenBindRejectsOtherFirstPDU(t *testing.T) {
	for _, wire := range []string{
		"00000010000000060000000000000019", // unbind
		"00000010800000150000000000000019", // enquire_link_resp
	} {
		t.Run(wire, func(t *testing.T) {
			peer, session, _, done := openBindPipe(t, time.Second, nil)
			openBindWrite(t, peer, wire)
			var bindErr *ServerBindError
			var transportErr net.Error
			if err := openBindResult(t, done); !errors.As(err, &bindErr) || bindErr.BindDecoded || errors.As(err, &transportErr) {
				t.Fatalf("wrong-first PDU rejection: %v", err)
			}
			var b [1]byte
			if n, err := peer.Read(b[:]); n != 0 || err == nil {
				t.Fatalf("wrong-first PDU got a response: %x, %v", b[:n], err)
			}
			if got := currentSessions(t, session.server, "sys"); got != 0 {
				t.Fatalf("wrong-first PDU reserved slot: %d", got)
			}
		})
	}
}

func TestServerOpenBindRetainsDeadline(t *testing.T) {
	const timeout = 200 * time.Millisecond
	started := time.Now()
	peer, _, observed, done := openBindPipe(t, timeout, nil)
	var firstExchangeCompleted time.Time
	for range 3 {
		openBindWrite(t, peer, "00000010000000150000000000000029")
		openBindRead(t, peer, "00000010800000150000000000000029")
		if firstExchangeCompleted.IsZero() {
			firstExchangeCompleted = time.Now()
		}
	}
	var timeoutErr net.Error
	if err := openBindResult(t, done); !errors.As(err, &timeoutErr) || !timeoutErr.Timeout() {
		t.Fatalf("expected original bind deadline timeout: %v", err)
	}
	observed.mu.Lock()
	defer observed.mu.Unlock()
	if len(observed.deadlines) != 1 {
		t.Fatalf("bind deadline reset during enquiry: %v", observed.deadlines)
	}
	deadline := observed.deadlines[0]
	if deadline.Before(started.Add(timeout)) || deadline.After(firstExchangeCompleted.Add(timeout)) {
		t.Fatalf("bind deadline = %v, started = %v", deadline, started)
	}
}

func TestServerOpenBindEnquireLinkWriteFailure(t *testing.T) {
	want := errors.New("synthetic Open enquiry write failure")
	peer, session, _, done := openBindPipe(t, time.Second, want)
	openBindWrite(t, peer, "00000010000000150000000000000029")
	err := openBindResult(t, done)
	var bindErr *ServerBindError
	if !errors.Is(err, want) || !errors.As(err, &bindErr) || bindErr.BindDecoded || bindErr.ResponseSent {
		t.Fatalf("enquiry write failure lost or incorrectly marked bound: %v", err)
	}
	if got := currentSessions(t, session.server, "sys"); got != 0 {
		t.Fatalf("failed enquiry reserved a slot: %d", got)
	}
}

func TestServerOpenBindEnquireLinkWriteTimeout(t *testing.T) {
	peer, session, _, done := openBindPipe(t, 100*time.Millisecond, nil)
	openBindWrite(t, peer, "00000010000000150000000000000029")
	// The peer never reads the response. The original bind deadline must
	// bound this write as well as subsequent reads.
	var timeoutErr net.Error
	var bindErr *ServerBindError
	err := openBindResult(t, done)
	if !errors.As(err, &timeoutErr) || !timeoutErr.Timeout() || !errors.As(err, &bindErr) || bindErr.BindDecoded {
		t.Fatalf("enquiry response did not honor bind deadline: %v", err)
	}
	if got := currentSessions(t, session.server, "sys"); got != 0 {
		t.Fatalf("timed-out enquiry reserved a slot: %d", got)
	}
}

// A real TCP exchange also serves the packet-capture harness: the first enquiry
// must be answered before the peer sends bind_transceiver.
func TestServerOpenBindEnquireLinkTCP(t *testing.T) {
	srv, addr := startTestServer(t, ServerSettings{SystemID: "smsc", BindTimeout: time.Second})
	if err := srv.StartSystemID(SystemIDConfig{SystemID: "sys", Password: "pw"}); err != nil {
		t.Fatal(err)
	}
	peer, err := net.DialTimeout("tcp", addr, testBindWait)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = peer.Close() })
	if err := peer.SetDeadline(time.Now().Add(testBindWait)); err != nil {
		t.Fatal(err)
	}
	openBindWrite(t, peer, "00000010000000150000000000012345")
	openBindRead(t, peer, "00000010800000150000000000012345")
	if sessions := srv.GetSystemIDStatus("sys"); len(sessions) != 0 {
		t.Fatalf("Open enquiry published session: %v", sessions)
	}
	openBindWrite(t, peer, openBindRequestHex)
	openBindRead(t, peer, openBindResponseHex)
	openBindWrite(t, peer, "00000010000000150000000000000789")
	openBindRead(t, peer, "00000010800000150000000000000789")
	waitForSessions(t, srv, "sys", 1)
}
