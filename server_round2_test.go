package gosmpp

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gomaja/go-smpp/data"
	"github.com/gomaja/go-smpp/pdu"
)

func TestServerInboundBindState(t *testing.T) {
	// SMPP v3.4 §§2.5–2.7 clarify the directions in §2.3 Table 2-1;
	// v5 §2.4 Table 2-1 confirms data_sm on TX/TRX and its response on RX/TRX.
	cases := []struct {
		name        string
		newPDU      func() pdu.PDU
		tx, rx, trx bool
	}{
		{"submit_sm", pdu.NewSubmitSM, true, false, true},
		{"submit_multi", pdu.NewSubmitMulti, true, false, true},
		{"query_sm", pdu.NewQuerySM, true, false, true},
		{"cancel_sm", pdu.NewCancelSM, true, false, true},
		{"replace_sm", pdu.NewReplaceSM, true, false, true},
		{"data_sm", pdu.NewDataSM, true, false, true},
		{"data_sm_resp", pdu.NewDataSMResp, false, true, true},
		{"deliver_sm_resp", pdu.NewDeliverSMResp, false, true, true},
		{"enquire_link_resp", pdu.NewEnquireLinkResp, true, true, true},
		{"unbind_resp", pdu.NewUnbindResp, true, true, true},
		{"generic_nack", pdu.NewGenericNack, true, true, true},
		{"submit_sm_resp", pdu.NewSubmitSMResp, false, false, false},
		{"submit_multi_resp", pdu.NewSubmitMultiResp, false, false, false},
		{"query_sm_resp", pdu.NewQuerySMResp, false, false, false},
		{"cancel_sm_resp", pdu.NewCancelSMResp, false, false, false},
		{"replace_sm_resp", pdu.NewReplaceSMResp, false, false, false},
		{"deliver_sm", pdu.NewDeliverSM, false, false, false},
		{"outbind", pdu.NewOutbind, false, false, false},
		{"alert_notification", pdu.NewAlertNotification, false, false, false},
	}
	for _, tc := range cases {
		for i, bt := range []pdu.BindingType{pdu.Transmitter, pdu.Receiver, pdu.Transceiver} {
			t.Run(tc.name+"/"+bt.String(), func(t *testing.T) {
				allowed := []bool{tc.tx, tc.rx, tc.trx}[i]
				srv, l := pipeServer(t, ServerSettings{}, 0)
				calls := make(chan pdu.PDU, 1)
				if err := srv.StartSystemID(SystemIDConfig{SystemID: "sys", Password: "pw", OnPDU: func(_ ServerSessionInfo, p pdu.PDU) (pdu.PDU, bool) { calls <- p; return nil, false }}); err != nil {
					t.Fatal(err)
				}
				c := pipeDial(t, l)
				bindPipe(t, c, bt, 0x34)
				req := tc.newPDU()
				req.SetSequenceNumber(701)
				writeWire(t, c, req)
				if !allowed && req.CanResponse() {
					raw := readWire(t, c)
					if len(raw) != 16 || binary.BigEndian.Uint32(raw[8:12]) != uint32(data.ESME_RINVBNDSTS) || binary.BigEndian.Uint32(raw[4:8]) != uint32(req.GetResponse().GetHeader().CommandID) || binary.BigEndian.Uint32(raw[12:]) != 701 {
						t.Fatalf("invalid-state response: %x", raw)
					}
				}
				// Enquire_link acts as an in-order processing barrier.
				writeWire(t, c, pdu.NewEnquireLink())
				readWire(t, c)
				if (len(calls) == 1) != allowed {
					t.Fatalf("handler called=%v, allowed=%v", len(calls) == 1, allowed)
				}
			})
		}
	}
}

func TestServerReceiverInvalidRequestWithoutHandler(t *testing.T) {
	srv, l := pipeServer(t, ServerSettings{}, 0)
	pipeAccount(t, srv, pdu.Receiver, 0)
	c := pipeDial(t, l)
	bindPipe(t, c, pdu.Receiver, 0x34)
	writeWire(t, c, pdu.NewSubmitSM())
	raw := readWire(t, c)
	if len(raw) != 16 || binary.BigEndian.Uint32(raw[8:12]) != uint32(data.ESME_RINVBNDSTS) {
		t.Fatalf("invalid RX submit response: %x", raw)
	}
}

func payloadDelivery() pdu.PDU {
	p := pdu.NewDeliverSM()
	p.RegisterOptionalParam(pdu.NewField(pdu.TagMessagePayload, []byte("payload")))
	return p
}

func TestServerOptionalDeliveryRequiresCompatibleSession(t *testing.T) {
	srv, l := pipeServer(t, ServerSettings{}, 0)
	pipeAccount(t, srv, pdu.Transceiver, 0)
	c := pipeDial(t, l)
	bindPipe(t, c, pdu.Transceiver, 0x33)
	await(t, func() bool { return len(srv.GetSystemIDStatus("sys")) == 1 })
	var versionErr *UnsupportedOptionalParametersError
	if err := srv.SubmitToSystemID(context.Background(), "sys", payloadDelivery()); !errors.As(err, &versionErr) || versionErr.Stripped || versionErr.SystemID != "sys" || versionErr.InterfaceVersion != 0x33 {
		t.Fatalf("optional delivery version error: %v", err)
	}
	assertNoWire(t, c)
}

func TestServerOptionalDeliveryPrefersCompatibleSession(t *testing.T) {
	srv, l := pipeServer(t, ServerSettings{}, 0)
	pipeAccount(t, srv, pdu.Transceiver, 0)
	old := pipeDial(t, l)
	bindPipe(t, old, pdu.Transceiver, 0x33)
	modern := pipeDial(t, l)
	bindPipe(t, modern, pdu.Transceiver, 0x34)
	await(t, func() bool { return len(srv.GetSystemIDStatus("sys")) == 2 })
	for range 20 {
		if err := srv.SubmitToSystemID(context.Background(), "sys", payloadDelivery()); err != nil {
			t.Fatal(err)
		}
		raw := readWire(t, modern)
		decoded, err := pdu.Parse(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		if len(decoded.(*pdu.DeliverSM).OptionalParameters) != 1 {
			t.Fatal("optional payload lost")
		}
	}
	assertNoWire(t, old)
}

func TestServerOldPeerResponseStrippingReported(t *testing.T) {
	errs := make(chan error, 10)
	srv, l := pipeServer(t, ServerSettings{OnError: func(err error) { errs <- err }}, 0)
	if err := srv.StartSystemID(SystemIDConfig{SystemID: "sys", Password: "pw", OnPDU: func(_ ServerSessionInfo, p pdu.PDU) (pdu.PDU, bool) {
		r := p.GetResponse()
		r.RegisterOptionalParam(pdu.NewField(pdu.TagAdditionalStatusInfoText, []byte("detail\x00")))
		return r, false
	}}); err != nil {
		t.Fatal(err)
	}
	c := pipeDial(t, l)
	bindPipe(t, c, pdu.Transceiver, 0x33)
	writeWire(t, c, pdu.NewSubmitSM())
	raw := readWire(t, c)
	if len(raw) != 17 {
		t.Fatalf("TLVs not stripped: %x", raw)
	}
	select {
	case <-errs:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("stripped response TLVs were not reported")
	}
}

func assertNoWire(t *testing.T, c net.Conn) {
	t.Helper()
	if err := c.SetReadDeadline(time.Now().Add(20 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	var b [1]byte
	_, err := c.Read(b[:])
	var ne net.Error
	if !errors.As(err, &ne) || !ne.Timeout() {
		t.Fatalf("unexpected wire data or close: %v", err)
	}
}

func TestServerSubmitContextCancellation(t *testing.T) {
	srv, l := pipeServer(t, ServerSettings{}, 0)
	if err := srv.StartSystemID(SystemIDConfig{SystemID: "sys", Password: "pw"}); err != nil {
		t.Fatal(err)
	}
	c := pipeDial(t, l)
	bindPipe(t, c, pdu.Transceiver, 0x34)
	await(t, func() bool { return len(srv.GetSystemIDStatus("sys")) == 1 })
	api, ok := any(srv).(interface {
		SubmitToSystemID(context.Context, string, pdu.PDU) error
	})
	if !ok {
		t.Fatal("server has no context-aware submit")
	}
	if err := api.SubmitToSystemID(context.Background(), "sys", pdu.NewDeliverSM()); err != nil {
		t.Fatal(err)
	}
	if err := api.SubmitToSystemID(context.Background(), "sys", pdu.NewDeliverSM()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := api.SubmitToSystemID(ctx, "sys", pdu.NewDeliverSM()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked queue cancellation: %v", err)
	}
	// Cancelled input must not be queued even if capacity becomes available.
	readWire(t, c)
	readWire(t, c)
	if err := api.SubmitToSystemID(ctx, "sys", pdu.NewDeliverSM()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired context accepted: %v", err)
	}
	assertNoWire(t, c)
}

func TestServerTLSRequiresCertificateSource(t *testing.T) {
	srv := NewServer(ServerSettings{Address: "127.0.0.1:0", TLSConfig: &tls.Config{}})
	defer srv.Stop()
	if err := srv.Start(); err == nil {
		t.Fatal("empty TLS config accepted")
	}
	if srv.Addr() != nil {
		t.Fatal("invalid TLS config opened listener")
	}
	for _, cfg := range []*tls.Config{
		{GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return nil, errors.New("test") }},
		{GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) { return nil, errors.New("test") }},
	} {
		s := NewServer(ServerSettings{Address: "127.0.0.1:0", TLSConfig: cfg})
		if err := s.Start(); err != nil {
			t.Fatal(err)
		}
		s.Stop()
	}
}

func TestServerCallbackStopDocumentation(t *testing.T) {
	b, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, doc := range []string{"OnError callbacks may call Stop or StopSystemID to initiate shutdown", "ServerPDUHandler callbacks may call Stop or StopSystemID to initiate shutdown", "an external call joins it"} {
		if !strings.Contains(string(b), doc) {
			t.Fatalf("missing callback shutdown contract: %s", doc)
		}
	}
}

func TestServerIntentionalShutdownDoesNotReportWriteError(t *testing.T) {
	errs := make(chan error, 10)
	srv, l := pipeServer(t, ServerSettings{OnError: func(err error) { errs <- err }}, 0)
	if err := srv.StartSystemID(SystemIDConfig{SystemID: "sys", Password: "pw", OnPDU: func(_ ServerSessionInfo, p pdu.PDU) (pdu.PDU, bool) {
		srv.StopSystemID("sys")
		return p.GetResponse(), false
	}}); err != nil {
		t.Fatal(err)
	}
	c := pipeDial(t, l)
	bindPipe(t, c, pdu.Transceiver, 0x34)
	writeWire(t, c, pdu.NewSubmitSM())
	await(t, func() bool { srv.mu.Lock(); defer srv.mu.Unlock(); return len(srv.connections) == 0 })
	select {
	case err := <-errs:
		t.Fatalf("intentional shutdown reported: %v", err)
	default:
	}
}

func TestServerIntentionalShutdownDoesNotReportStartupError(t *testing.T) {
	errs := make(chan error, 10)
	srv, l := pipeServer(t, ServerSettings{OnError: func(err error) { errs <- err }}, 0)
	pipeAccount(t, srv, pdu.Transceiver, 0)
	c, g := gatedDial(t, l, false)
	bindPipe(t, c, pdu.Transceiver, 0x34)
	waitSignal(t, g.entered)
	stopped := make(chan struct{})
	go func() { srv.StopSystemID("sys"); close(stopped) }()
	await(t, func() bool { srv.mu.Lock(); defer srv.mu.Unlock(); return srv.credStore["sys"] == nil })
	close(g.release)
	waitSignal(t, stopped)
	await(t, func() bool { srv.mu.Lock(); defer srv.mu.Unlock(); return len(srv.connections) == 0 })
	select {
	case err := <-errs:
		t.Fatalf("intentional startup shutdown reported: %v", err)
	default:
	}
}

func TestServerZeroValue(t *testing.T) {
	for _, method := range []string{"Start", "StartSystemID", "Stop", "StopSystemID", "Addr", "SystemIDs", "GetSystemIDStatus"} {
		t.Run(method, func(t *testing.T) {
			defer func() {
				if p := recover(); p != nil {
					t.Fatalf("zero Server panicked: %v", p)
				}
			}()
			var srv Server
			switch method {
			case "Start":
				if err := srv.Start(); err == nil {
					t.Fatal("zero Server Start accepted")
				}
			case "StartSystemID":
				if err := srv.StartSystemID(SystemIDConfig{SystemID: "sys"}); err == nil {
					t.Fatal("zero Server account accepted")
				}
			case "Stop":
				srv.Stop()
			case "StopSystemID":
				srv.StopSystemID("sys")
			case "Addr":
				if srv.Addr() != nil {
					t.Fatal("zero Server has address")
				}
			case "SystemIDs":
				if len(srv.SystemIDs()) != 0 {
					t.Fatal("zero Server has sessions")
				}
			case "GetSystemIDStatus":
				if len(srv.GetSystemIDStatus("sys")) != 0 {
					t.Fatal("zero Server has status")
				}
			}
		})
	}
}

func TestServerRejectsNilPDU(t *testing.T) {
	srv, l := pipeServer(t, ServerSettings{}, 0)
	pipeAccount(t, srv, pdu.Transceiver, 0)
	c := pipeDial(t, l)
	bindPipe(t, c, pdu.Transceiver, 0x34)
	await(t, func() bool { return len(srv.GetSystemIDStatus("sys")) == 1 })
	if err := srv.SubmitToSystemID(context.Background(), "sys", nil); err == nil {
		t.Fatal("nil PDU accepted")
	}
	var typedNil *pdu.DeliverSM
	if err := srv.SubmitToSystemID(context.Background(), "sys", typedNil); err == nil {
		t.Fatal("typed nil PDU accepted")
	}
}

func TestServerZeroValueSubmit(t *testing.T) {
	var srv Server
	if err := srv.SubmitToSystemID(context.Background(), "sys", pdu.NewDeliverSM()); !errors.Is(err, ErrServerNotInitialized) {
		t.Fatalf("zero server submit: %v", err)
	}
}

func TestServerHandlerSessionInfo(t *testing.T) {
	for _, bt := range []pdu.BindingType{pdu.Transmitter, pdu.Receiver, pdu.Transceiver} {
		t.Run(bt.String(), func(t *testing.T) {
			srv, l := pipeServer(t, ServerSettings{}, 0)
			info := make(chan ServerSessionInfo, 1)
			if err := srv.StartSystemID(SystemIDConfig{SystemID: "sys", Password: "pw", OnPDU: func(s ServerSessionInfo, p pdu.PDU) (pdu.PDU, bool) { info <- s; return p.GetResponse(), false }}); err != nil {
				t.Fatal(err)
			}
			c := pipeDial(t, l)
			bindPipe(t, c, bt, 0x33)
			writeWire(t, c, pdu.NewEnquireLinkResp())
			writeWire(t, c, pdu.NewEnquireLink())
			readWire(t, c)
			select {
			case s := <-info:
				if s.SystemID != "sys" || s.BindingType != bt || s.InterfaceVersion != 0x33 || s.LocalAddr.String() != c.RemoteAddr().String() || s.RemoteAddr.String() != c.LocalAddr().String() {
					t.Fatalf("incorrect session metadata: %+v", s)
				}
			case <-time.After(time.Second):
				t.Fatal("handler metadata missing")
			}
		})
	}
}

func TestServerStatusBindingType(t *testing.T) {
	for _, bt := range []pdu.BindingType{pdu.Transmitter, pdu.Receiver, pdu.Transceiver} {
		t.Run(bt.String(), func(t *testing.T) {
			srv, l := pipeServer(t, ServerSettings{}, 0)
			pipeAccount(t, srv, bt, 0)
			bindPipe(t, pipeDial(t, l), bt, 0x34)
			await(t, func() bool { return len(srv.GetSystemIDStatus("sys")) == 1 })
			if got := srv.GetSystemIDStatus("sys")[0].BindingType; got != bt {
				t.Fatalf("binding type=%v, want %v", got, bt)
			}
		})
	}
}

func assertConnectionClosed(t *testing.T, c net.Conn) {
	t.Helper()
	if err := c.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); err != nil && !errors.Is(err, io.ErrClosedPipe) && !errors.Is(err, net.ErrClosed) {
		t.Fatal(err)
	}
	var b [1]byte
	_, err := c.Read(b[:])
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		t.Fatalf("connection still open (timeout): %v", err)
	}
	if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) && !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("expected EOF/closed connection, got %v", err)
	}
}

func TestServerStopSystemIDClosesConnections(t *testing.T) {
	srv, l := pipeServer(t, ServerSettings{}, 0)
	pipeAccount(t, srv, pdu.Transceiver, 0)
	c := pipeDial(t, l)
	bindPipe(t, c, pdu.Transceiver, 0x34)
	await(t, func() bool { return len(srv.GetSystemIDStatus("sys")) == 1 })
	srv.mu.Lock()
	account := srv.credStore["sys"]
	srv.mu.Unlock()
	srv.StopSystemID("sys")
	assertConnectionClosed(t, c)
	await(t, func() bool {
		srv.mu.Lock()
		defer srv.mu.Unlock()
		return account.currentSession == 0 && len(srv.connections) == 0
	})
}

func TestServerPasswordsEqual(t *testing.T) {
	tests := []struct {
		want, got string
		equal     bool
	}{
		{"secret", "secret", true},
		{"", "", true},
		{"secret", "secreT", false},
		{"secret", "secre", false},
		{"secret", "secrets", false},
		{"secret", "", false},
		{"", "secret", false},
	}
	for _, tc := range tests {
		if got := passwordsEqual(tc.want, tc.got); got != tc.equal {
			t.Errorf("passwordsEqual(%q, %q) = %v, want %v", tc.want, tc.got, got, tc.equal)
		}
	}
}
