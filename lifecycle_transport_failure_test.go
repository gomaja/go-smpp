package gosmpp

import (
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gomaja/go-smpp/data"
	"github.com/gomaja/go-smpp/pdu"
)

type lifecycleTransportOutcome struct {
	p       pdu.PDU
	err     error
	written bool
}

type lifecycleTransportFailureConn struct {
	net.Conn
	mode    string
	writes  atomic.Int32
	outcome chan lifecycleTransportOutcome
}

func (c *lifecycleTransportFailureConn) Write(buffer []byte) (int, error) {
	if c.writes.Add(1) == 1 {
		switch c.mode {
		case "partial":
			return len(buffer) / 2, io.ErrUnexpectedEOF
		case "permanent":
			return 0, io.ErrClosedPipe
		case "short":
			return len(buffer) / 2, nil
		case "partial_timeout":
			return len(buffer) / 2, os.ErrDeadlineExceeded
		case "zero_timeout":
			return 0, os.ErrDeadlineExceeded
		}
	} else if c.outcome != nil {
		c.outcome <- lifecycleTransportOutcome{written: true}
	}
	return len(buffer), nil
}

func TestLifecycleResponseWriteFailureBlocksOutboundTraffic(t *testing.T) {
	client, peer := net.Pipe()
	outcome := make(chan lifecycleTransportOutcome, 2)
	raw := &lifecycleTransportFailureConn{Conn: client, mode: "partial", outcome: outcome}
	responseError, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	queued := pdu.NewSubmitSM()
	trx := newTransceivable(NewConnection(raw), Settings{ReadTimeout: time.Hour,
		OnAllPDU: func(request pdu.PDU) (pdu.PDU, bool) { return request.GetResponse(), true },
		OnSubmitError: func(p pdu.PDU, err error) {
			if p.GetHeader().CommandID == data.DELIVER_SM_RESP {
				close(responseError)
				<-release
				return
			}
			outcome <- lifecycleTransportOutcome{p: p, err: err}
		},
	}, nil)
	trx.start()
	t.Cleanup(func() { unblock(); _ = peer.Close(); _ = trx.Close() })
	request := pdu.NewDeliverSM()
	receiveLifecycleWrite(t, peer, receiveLifecycleMarshal(t, request))
	lifecycleWait(t, responseError)
	// The reader cannot start shutdown until its error callback returns. The
	// failed transport itself must reject the independent outbound worker.
	if err := trx.Submit(queued); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-outcome:
		if result.written || raw.writes.Load() != 1 {
			t.Fatalf("outbound PDU followed an incomplete response frame: transport writes=%d, want 1", raw.writes.Load())
		}
		if result.p != queued || !errors.Is(result.err, ErrConnectionClosing) {
			t.Fatalf("queued PDU failure = %T, %v; want queued submit and ErrConnectionClosing", result.p, result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("queued PDU neither failed nor reached the transport")
	}
	unblock()
	if err := trx.Close(); err != nil {
		t.Fatal(err)
	}
	if raw.writes.Load() != 1 {
		t.Fatal("shutdown appended further traffic after the failed response")
	}
}

func TestLifecycleConnectionRejectsPDUsAfterTransportFailure(t *testing.T) {
	for _, timed := range []bool{false, true} {
		method := "WritePDU"
		if timed {
			method = "writePDUWithTimeout"
		}
		for _, mode := range []string{"partial", "permanent", "short", "partial_timeout"} {
			t.Run(method+"/"+mode, func(t *testing.T) {
				client, peer := net.Pipe()
				raw := &lifecycleTransportFailureConn{Conn: client, mode: mode}
				connection := NewConnection(raw)
				t.Cleanup(func() { _ = peer.Close(); _ = connection.Close() })
				write := connection.WritePDU
				if timed {
					write = func(p pdu.PDU) (int, error) { return connection.writePDUWithTimeout(p, time.Second) }
				}
				n, err := write(pdu.NewSubmitSM())
				want := io.ErrUnexpectedEOF
				if mode == "permanent" {
					want = io.ErrClosedPipe
				}
				if mode == "short" {
					want = io.ErrShortWrite
				}
				if mode == "partial_timeout" {
					want = os.ErrDeadlineExceeded
				}
				if !errors.Is(err, want) || (mode != "permanent" && n == 0) {
					t.Fatalf("first write = %d, %v; want preserved bytes and %v", n, err, want)
				}
				n, err = write(pdu.NewSubmitSM())
				if n != 0 || !errors.Is(err, ErrConnectionClosing) || raw.writes.Load() != 1 {
					t.Fatalf("second write used failed transport: n=%d error=%v transport writes=%d; want 0, ErrConnectionClosing, 1", n, err, raw.writes.Load())
				}
			})
		}
	}
}

func TestLifecycleConnectionZeroByteTimeoutLeavesTransportWritable(t *testing.T) {
	for _, timed := range []bool{false, true} {
		method := "WritePDU"
		if timed {
			method = "writePDUWithTimeout"
		}
		t.Run(method, func(t *testing.T) {
			client, peer := net.Pipe()
			raw := &lifecycleTransportFailureConn{Conn: client, mode: "zero_timeout"}
			connection := NewConnection(raw)
			t.Cleanup(func() { _ = peer.Close(); _ = connection.Close() })
			write := connection.WritePDU
			if timed {
				write = func(p pdu.PDU) (int, error) { return connection.writePDUWithTimeout(p, time.Second) }
			}
			if n, err := write(pdu.NewSubmitSM()); n != 0 || !errors.Is(err, os.ErrDeadlineExceeded) {
				t.Fatalf("first write = %d, %v; want zero bytes and timeout", n, err)
			}
			if n, err := write(pdu.NewUnbindResp()); n == 0 || err != nil || raw.writes.Load() != 2 {
				t.Fatalf("response after zero-byte timeout = %d, %v; transport writes=%d", n, err, raw.writes.Load())
			}
		})
	}
}

func TestLifecycleConnectionEncodeFailureLeavesTransportWritable(t *testing.T) {
	for _, timed := range []bool{false, true} {
		method := "WritePDU"
		if timed {
			method = "writePDUWithTimeout"
		}
		t.Run(method, func(t *testing.T) {
			client, peer := net.Pipe()
			raw := &lifecycleTransportFailureConn{Conn: client, mode: "success"}
			connection := NewConnection(raw)
			t.Cleanup(func() { _ = peer.Close(); _ = connection.Close() })
			write := connection.WritePDU
			if timed {
				write = func(p pdu.PDU) (int, error) { return connection.writePDUWithTimeout(p, time.Second) }
			}
			invalid := pdu.NewSubmitSM().(*pdu.SubmitSM)
			invalid.ServiceType = "invalid\x00service"
			n, err := write(invalid)
			if n != 0 || !errors.Is(err, pdu.ErrEncode) || raw.writes.Load() != 0 {
				t.Fatalf("encode rejection reached transport: n=%d error=%v writes=%d", n, err, raw.writes.Load())
			}
			n, err = write(pdu.NewSubmitSM())
			if n == 0 || err != nil || raw.writes.Load() != 1 {
				t.Fatalf("valid write after encode rejection = %d, %v; transport writes=%d", n, err, raw.writes.Load())
			}
		})
	}
}
