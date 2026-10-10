package gosmpp

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gomaja/go-smpp/pdu"
)

// These frames are independent of the PDU encoder under test.
const handshakeUnknown = "00000014000077770000000000000055deadbeef"
const handshakeNack = "00000010800000000000000300000055"
const handshakeEnquiry = "00000010000000150000000000000066"
const handshakeEnquiryResponse = "00000010800000150000000000000066"
const handshakeIgnoredNack = "00000010800000000000000300000077"

type handshakeConn struct {
	*bytes.Reader
	written   bytes.Buffer
	writeErr  error
	deadlines []time.Time
}

func (c *handshakeConn) Write(b []byte) (int, error) {
	if c.writeErr != nil {
		return 0, c.writeErr
	}
	return c.written.Write(b)
}
func (c *handshakeConn) Close() error { return nil }
func (c *handshakeConn) LocalAddr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 2775}
}
func (c *handshakeConn) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 32775}
}
func (c *handshakeConn) SetDeadline(d time.Time) error {
	c.deadlines = append(c.deadlines, d)
	return nil
}
func (c *handshakeConn) SetReadDeadline(time.Time) error { return nil }
func (c *handshakeConn) SetWriteDeadline(d time.Time) error {
	c.deadlines = append(c.deadlines, d)
	return nil
}

func (c *openBindDeadlineConn) SetWriteDeadline(d time.Time) error {
	c.mu.Lock()
	c.deadlines = append(c.deadlines, d)
	c.mu.Unlock()
	return c.Conn.SetWriteDeadline(d)
}

func handshakeSession(wire []byte, writeErr error) (*serverSession, *handshakeConn) {
	c := &handshakeConn{Reader: bytes.NewReader(wire), writeErr: writeErr}
	srv := NewServer(ServerSettings{SystemID: "smsc", BindTimeout: time.Second})
	if err := srv.StartSystemID(SystemIDConfig{SystemID: "sys", Password: "pw"}); err != nil {
		panic(err)
	}
	return newServerSession(c, srv), c
}

func handshakeRun(t *testing.T, wire, want string, writeErr error) (*serverSession, *handshakeConn, error) {
	t.Helper()
	s, c := handshakeSession(openBindBytes(t, wire), writeErr)
	err := s.handleBind()
	if got := hex.EncodeToString(c.written.Bytes()); got != want {
		t.Fatalf("response bytes: got %s, want %s (bind error %v)", got, want, err)
	}
	if len(c.deadlines) != 1 {
		t.Fatalf("bind deadline reset %d times", len(c.deadlines))
	}
	return s, c, err
}

func TestServerHandshakeUnknownThenBind(t *testing.T) {
	s, _, err := handshakeRun(t, handshakeUnknown+openBindRequestHex, handshakeNack+openBindResponseHex, nil)
	if err != nil || s.systemID != "sys" {
		t.Fatalf("bind after unknown: %v, system ID %q", err, s.systemID)
	}
}

func TestServerHandshakeUnknownDiagnostics(t *testing.T) {
	for _, failure := range []error{nil, errors.New("diagnostic write failure")} {
		s, _ := handshakeSession(openBindBytes(t, handshakeUnknown+openBindRequestHex), failure)
		var reported []error
		s.server.settings.OnError = func(err error) { reported = append(reported, err) }
		err := s.handleBind()
		if failure != nil {
			if len(reported) != 1 || !errors.Is(reported[0], failure) || !errors.Is(err, failure) {
				t.Fatalf("write failure report duplicated: %v, %v", reported, err)
			}
			continue
		}
		if err != nil || len(reported) != 2 {
			t.Fatalf("unknown diagnostic count %d, bind error %v", len(reported), err)
		}
		var decoded *pdu.DecodeError
		var bindErr *ServerBindError
		if !errors.As(reported[0], &decoded) || decoded.Header.SequenceNumber != 0x55 || !errors.As(reported[0], &bindErr) || bindErr.BindDecoded || !bindErr.ResponseSent {
			t.Fatalf("unknown diagnostic metadata: %#v", reported[0])
		}
	}
}

func TestServerHandshakeUnknownDiagnosticStopTCP(t *testing.T) {
	var srv *Server
	returned := make(chan struct{})
	srv, addr := startTestServer(t, ServerSettings{OnError: func(error) { srv.Stop(); close(returned) }})
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.SetDeadline(time.Now().Add(testBindWait)); err != nil {
		t.Fatal(err)
	}
	openBindWrite(t, conn, handshakeUnknown)
	openBindRead(t, conn, handshakeNack)
	select {
	case <-returned:
	case <-time.After(testBindWait):
		t.Fatal("unknown diagnostic Stop did not return")
	}
	srv.Stop()
}

func TestServerHandshakeMalformedBind(t *testing.T) {
	for _, cmd := range []uint32{1, 2, 9} {
		t.Run(string(rune('0'+cmd)), func(t *testing.T) {
			frame := make([]byte, 16)
			binary.BigEndian.PutUint32(frame[:4], 16)
			binary.BigEndian.PutUint32(frame[4:8], cmd)
			binary.BigEndian.PutUint32(frame[12:], 0x55)
			want := append([]byte(nil), frame...)
			binary.BigEndian.PutUint32(want[4:8], cmd|0x80000000)
			binary.BigEndian.PutUint32(want[8:12], 2)
			s, _, err := handshakeRun(t, hex.EncodeToString(frame), hex.EncodeToString(want), nil)
			var bindErr *ServerBindError
			if !errors.As(err, &bindErr) || bindErr.BindDecoded || !bindErr.ResponseSent || bindErr.CommandStatus != 2 {
				t.Fatalf("malformed bind result: %#v", err)
			}
			if currentSessions(t, s.server, "sys") != 0 {
				t.Fatal("malformed bind reserved account")
			}
		})
	}
	// Fully received mandatory fields followed by a truncated optional value:
	// preserve the decoder's optional-stream status rather than hardcoding length.
	s, _, err := handshakeRun(t, "0000002000000009000000000000034573797300707700003400000002100002", "0000001080000009000000c000000345", nil)
	if err == nil || currentSessions(t, s.server, "sys") != 0 {
		t.Fatalf("malformed bind TLV accepted: %v", err)
	}
}

func TestServerHandshakeFramingErrors(t *testing.T) {
	for _, tc := range []struct{ name, wire, want string }{
		{"short_length", "0000000f000000090000000000000055", "00000010800000000000000200000055"},
		{"oversized_length", "7fffffff000000090000000000000055", "00000010800000000000000200000055"},
		{"incomplete_header", "0000001c00000009", "00000010800000000000000200000000"},
		{"incomplete_sequence", "0000001c0000000900000000123456", "00000010800000000000000200000000"},
		{"incomplete_body", "0000001c0000000900000000000000557379", "00000010800000000000000200000055"},
		{"idle_eof", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, err := handshakeRun(t, tc.wire, tc.want, nil)
			if err == nil {
				t.Fatal("framing error accepted")
			}
			if currentSessions(t, s.server, "sys") != 0 {
				t.Fatal("framing error reserved account")
			}
		})
	}
}

func TestServerHandshakeUnknownWriteDeadline(t *testing.T) {
	peer, _, observed, done := openBindPipe(t, 100*time.Millisecond, nil)
	openBindWrite(t, peer, handshakeUnknown)
	// Do not read the nack: the original bind deadline also bounds this write.
	var timeout net.Error
	var bindErr *ServerBindError
	err := openBindResult(t, done)
	if !errors.As(err, &timeout) || !timeout.Timeout() || !errors.As(err, &bindErr) || bindErr.ResponseSent {
		t.Fatalf("unknown response deadline: %#v", err)
	}
	observed.mu.Lock()
	defer observed.mu.Unlock()
	if len(observed.deadlines) != 1 {
		t.Fatalf("unknown response changed bind deadline: %v", observed.deadlines)
	}
}

func TestServerHandshakeResponseWriteFailure(t *testing.T) {
	failure := errors.New("handshake response write failed")
	for _, wire := range []string{handshakeUnknown, "00000010000000090000000000000055", "0000000f000000090000000000000055"} {
		t.Run(wire, func(t *testing.T) {
			s, c, err := handshakeRun(t, wire+openBindRequestHex, "", failure)
			var bindErr *ServerBindError
			if !errors.Is(err, failure) || !errors.As(err, &bindErr) || bindErr.ResponseSent {
				t.Fatalf("response failure lost: %#v", err)
			}
			if currentSessions(t, s.server, "sys") != 0 {
				t.Fatal("write failure reserved account")
			}
			if c.Len() < len(openBindBytes(t, openBindRequestHex)) {
				t.Fatal("write failure continued parsing bind")
			}
		})
	}
}

func TestServerHandshakeControlLimit(t *testing.T) {
	for _, tc := range []struct{ name, control, response string }{
		{"enquiries", handshakeEnquiry, handshakeEnquiryResponse},
		{"unknowns", handshakeUnknown, handshakeNack},
		{"nacks", handshakeIgnoredNack, ""},
		{"mixed", handshakeEnquiry + handshakeUnknown + handshakeIgnoredNack, handshakeEnquiryResponse + handshakeNack},
	} {
		t.Run(tc.name, func(t *testing.T) {
			controls, responses := strings.Repeat(tc.control, 8), strings.Repeat(tc.response, 8)
			if tc.name == "mixed" {
				controls = strings.Repeat(tc.control, 2) + handshakeEnquiry + handshakeUnknown
				responses = strings.Repeat(tc.response, 2) + handshakeEnquiryResponse + handshakeNack
			}
			_, _, err := handshakeRun(t, controls+openBindRequestHex, responses+openBindResponseHex, nil)
			if err != nil {
				t.Fatalf("bind at control limit: %v", err)
			}
			ninth := tc.control
			if tc.name == "mixed" {
				ninth = handshakeEnquiry
			}
			s, c, err := handshakeRun(t, controls+ninth+openBindRequestHex, responses, nil)
			if err == nil || !strings.Contains(err.Error(), "limit") {
				t.Fatalf("ninth control accepted: %v", err)
			}
			if c.Len() == 0 || currentSessions(t, s.server, "sys") != 0 {
				t.Fatal("limit consumed bind or reserved account")
			}
		})
	}
}

func TestServerHandshakeRequestStatusIgnored(t *testing.T) {
	bind := openBindBytes(t, openBindRequestHex)
	binary.BigEndian.PutUint32(bind[8:12], 0xdeadbeef)
	_, _, err := handshakeRun(t, hex.EncodeToString(bind), openBindResponseHex, nil)
	if err != nil {
		t.Fatalf("request status affected bind: %v", err)
	}
	bind[21] = 'x' // wrong password; response reflects validation, not request status.
	_, c, err := handshakeRun(t, hex.EncodeToString(bind), "00000010800000090000000e00000345", nil)
	if err == nil || binary.BigEndian.Uint32(c.written.Bytes()[8:12]) != 14 {
		t.Fatalf("validation status: %v", err)
	}
	enquiry := openBindBytes(t, handshakeEnquiry)
	binary.BigEndian.PutUint32(enquiry[8:12], 0xdeadbeef)
	_, _, err = handshakeRun(t, hex.EncodeToString(enquiry)+openBindRequestHex, handshakeEnquiryResponse+openBindResponseHex, nil)
	if err != nil {
		t.Fatalf("request status affected enquiry: %v", err)
	}
}

func TestServerHandshakeAllocationBound(t *testing.T) {
	wire := openBindBytes(t, strings.Repeat(handshakeEnquiry, 8)+openBindRequestHex)
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	const exchanges = 16
	for range exchanges {
		s, _ := handshakeSession(wire, nil)
		if err := s.handleBind(); err != nil {
			t.Fatal(err)
		}
	}
	runtime.ReadMemStats(&after)
	average := (after.TotalAlloc - before.TotalAlloc) / exchanges
	if average > 256<<10 {
		t.Fatalf("eight-enquiry bind allocated %d bytes/exchange; limit 262144", average)
	}
}

func TestServerHandshakeUnknownPipelineTCP(t *testing.T) {
	srv, addr := startTestServer(t, ServerSettings{SystemID: "smsc"})
	if err := srv.StartSystemID(SystemIDConfig{SystemID: "sys", Password: "pw"}); err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.SetDeadline(time.Now().Add(testBindWait)); err != nil {
		t.Fatal(err)
	}
	// A single write proves bind parsing does not strand buffered post-bind bytes.
	openBindWrite(t, conn, handshakeUnknown+openBindRequestHex+handshakeEnquiry)
	openBindRead(t, conn, handshakeNack)
	openBindRead(t, conn, openBindResponseHex)
	openBindRead(t, conn, handshakeEnquiryResponse)
}

func TestServerHandshakeLimitTCP(t *testing.T) {
	srv, addr := startTestServer(t, ServerSettings{SystemID: "smsc"})
	if err := srv.StartSystemID(SystemIDConfig{SystemID: "sys", Password: "pw"}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, control, response string }{
		{"unknown", handshakeUnknown, handshakeNack},
		{"enquiry", handshakeEnquiry, handshakeEnquiryResponse},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn, err := net.Dial("tcp", addr)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = conn.Close() })
			if err := conn.SetDeadline(time.Now().Add(testBindWait)); err != nil {
				t.Fatal(err)
			}
			openBindWrite(t, conn, strings.Repeat(tc.control, 9))
			for range 8 {
				openBindRead(t, conn, tc.response)
			}
			var b [1]byte
			if n, err := conn.Read(b[:]); n != 0 || err == nil {
				t.Fatalf("ninth control not closed: %d,%v", n, err)
			} else if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
				t.Fatal("limit waited for deadline")
			}
		})
	}
}

func TestServerHandshakeErrorsTCP(t *testing.T) {
	srv, addr := startTestServer(t, ServerSettings{SystemID: "smsc"})
	if err := srv.StartSystemID(SystemIDConfig{SystemID: "sys", Password: "pw"}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, wire, want string }{
		{"mandatory_bind", "00000010000000090000000000000078", "00000010800000090000000200000078"},
		{"invalid_length", "0000000f000000090000000000000078", "00000010800000000000000200000078"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn, err := net.Dial("tcp", addr)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = conn.Close() })
			if err := conn.SetDeadline(time.Now().Add(testBindWait)); err != nil {
				t.Fatal(err)
			}
			openBindWrite(t, conn, tc.wire)
			openBindRead(t, conn, tc.want)
			var b [1]byte
			if n, err := conn.Read(b[:]); n != 0 || err == nil {
				t.Fatalf("error exchange not closed: %d,%v", n, err)
			} else if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
				t.Fatal("error reply did not close")
			}
		})
	}
}

func BenchmarkServerHandshake(b *testing.B) {
	for _, tc := range []struct{ name, wire string }{
		{"bind", openBindRequestHex},
		{"eight_enquiries", strings.Repeat(handshakeEnquiry, 8) + openBindRequestHex},
	} {
		b.Run(tc.name, func(b *testing.B) {
			wire, err := hex.DecodeString(tc.wire)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			for b.Loop() {
				s, _ := handshakeSession(wire, nil)
				if err := s.handleBind(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

var _ net.Conn = (*handshakeConn)(nil)
var _ io.Reader = (*handshakeConn)(nil)
