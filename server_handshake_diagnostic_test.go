package gosmpp

import (
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gomaja/go-smpp/pdu"
)

func TestServerHandshakeDiagnosticsAreAggregated(t *testing.T) {
	for _, tc := range []struct {
		name, wire, counts string
		wantReports        int
		wantBind           bool
	}{
		{"unknown_cap", strings.Repeat(handshakeUnknown, 9), "enquire_link=0 generic_nack=0 unknown_command=9", 2, false},
		{"mixed_cap", strings.Repeat(handshakeUnknown, 6) + strings.Repeat(handshakeEnquiry, 2) + handshakeIgnoredNack, "enquire_link=2 generic_nack=1 unknown_command=6", 2, false},
		{"successful_bind", strings.Repeat(handshakeUnknown, 3) + strings.Repeat(handshakeEnquiry, 2) + strings.Repeat(handshakeIgnoredNack, 2) + openBindRequestHex, "enquire_link=2 generic_nack=2 unknown_command=3", 2, true},
		{"enquiry_cap", strings.Repeat(handshakeEnquiry, 9), "enquire_link=9 generic_nack=0 unknown_command=0", 1, false},
		{"nack_cap", strings.Repeat(handshakeIgnoredNack, 9), "enquire_link=0 generic_nack=9 unknown_command=0", 1, false},
		{"permitted_controls", strings.Repeat(handshakeEnquiry, 8) + openBindRequestHex, "", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := handshakeSession(openBindBytes(t, tc.wire), nil)
			var reported []error
			s.server.settings.OnError = func(err error) { reported = append(reported, err) }
			err := s.handleBind()
			if (err == nil) != tc.wantBind {
				t.Fatalf("bind result: %v, want bound=%t", err, tc.wantBind)
			}
			if len(reported) != tc.wantReports {
				t.Fatalf("handshake diagnostic count=%d, want %d; %v", len(reported), tc.wantReports, reported)
			}
			if tc.wantReports == 0 {
				return
			}
			if tc.wantReports == 2 {
				var decoded *pdu.DecodeError
				if !errors.As(reported[0], &decoded) || decoded.Header.SequenceNumber != 0x55 {
					t.Fatalf("first diagnostic lost decode error or sequence: %v", reported[0])
				}
			}
			summary := reported[len(reported)-1]
			if !strings.Contains(summary.Error(), tc.counts) {
				t.Fatalf("aggregate counts: %v, want %s", summary, tc.counts)
			}
			if !tc.wantBind && !errors.Is(summary, err) {
				t.Fatalf("aggregate lost terminal bind error: %v, terminal %v", summary, err)
			}
			if !tc.wantBind && !strings.Contains(summary.Error(), "limit") {
				t.Fatalf("aggregate hid terminal error text: %v", summary)
			}
		})
	}
}

func TestServerHandshakeDiagnosticSuppressesShutdownSummary(t *testing.T) {
	for _, shutdown := range []string{"server", "connection"} {
		t.Run(shutdown, func(t *testing.T) {
			s, _ := handshakeSession(openBindBytes(t, handshakeUnknown+openBindRequestHex), nil)
			reports := 0
			s.server.settings.OnError = func(error) {
				reports++
				if shutdown == "server" {
					s.server.Stop()
				} else {
					s.closing.Store(true)
				}
			}
			_ = s.handleBind()
			if reports != 1 {
				t.Fatalf("handshake called OnError %d times after its first callback initiated %s shutdown", reports, shutdown)
			}
		})
	}
}

func TestServerHandshakeBindWithoutControlsHasNoAggregate(t *testing.T) {
	for _, wire := range []string{openBindRequestHex, "00000010000000090000000000000055"} {
		s, _ := handshakeSession(openBindBytes(t, wire), nil)
		reports := 0
		s.server.settings.OnError = func(error) { reports++ }
		_ = s.handleBind()
		if reports != 0 {
			t.Fatalf("bind without control PDUs produced %d aggregate diagnostics", reports)
		}
	}
}

func TestServerHandshakeDiagnosticWriteFailureIsAggregated(t *testing.T) {
	want := errors.New("synthetic handshake write failure")
	s, _ := handshakeSession(openBindBytes(t, handshakeUnknown), want)
	var reported []error
	s.server.settings.OnError = func(err error) { reported = append(reported, err) }
	err := s.handleBind()
	if !errors.Is(err, want) || len(reported) != 1 || !errors.Is(reported[0], want) {
		t.Fatalf("write failure diagnostic: %v, reports %v", err, reported)
	}
	if !strings.Contains(reported[0].Error(), "unknown_command=1") {
		t.Fatalf("write failure lost control count: %v", reported[0])
	}
}

func TestServerHandshakeDiagnosticCapTCP(t *testing.T) {
	var mu sync.Mutex
	var reported []error
	srv, addr := startTestServer(t, ServerSettings{SystemID: "smsc", OnError: func(err error) {
		mu.Lock()
		reported = append(reported, err)
		mu.Unlock()
	}})
	peer, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = peer.Close() })
	if err := peer.SetDeadline(time.Now().Add(testBindWait)); err != nil {
		t.Fatal(err)
	}
	openBindWrite(t, peer, strings.Repeat(handshakeUnknown, 9))
	for range 8 {
		openBindRead(t, peer, handshakeNack)
	}
	var extra [1]byte
	if n, err := peer.Read(extra[:]); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("ninth command did not close without a response: n=%d, err=%v", n, err)
	}
	// Stop joins the connection worker, including the end-of-handshake diagnostic.
	srv.Stop()
	mu.Lock()
	defer mu.Unlock()
	if len(reported) != 2 {
		t.Fatalf("nine unknown commands caused %d OnError calls, want first plus aggregate; %v", len(reported), reported)
	}
	if !strings.Contains(reported[1].Error(), "unknown_command=9") {
		t.Fatalf("aggregate omitted the rejected ninth command: %v", reported[1])
	}
}
