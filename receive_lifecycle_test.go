package gosmpp

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gomaja/go-smpp/data"
	"github.com/gomaja/go-smpp/pdu"
)

const receiveLifecycleWait = 2 * time.Second

func receiveLifecycleAwait(t *testing.T, condition func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(receiveLifecycleWait)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal(message)
		}
		runtime.Gosched()
	}
}

func receiveLifecycleSignal(t *testing.T, signal <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(receiveLifecycleWait):
		t.Fatal(message)
	}
}

func receiveLifecycleNoGoroutineLeak(t *testing.T, baseline int) {
	t.Helper()
	deadline := time.Now().Add(receiveLifecycleWait)
	for runtime.NumGoroutine() > baseline {
		if time.Now().After(deadline) {
			stacks := make([]byte, 64<<10)
			stacks = stacks[:runtime.Stack(stacks, true)]
			t.Fatalf("shutdown leaked goroutines: count %d, baseline %d\n%s", runtime.NumGoroutine(), baseline, stacks)
		}
		runtime.Gosched()
	}
}

func receiveLifecycleWrite(t *testing.T, peer net.Conn, frame []byte) {
	t.Helper()
	if err := peer.SetWriteDeadline(time.Now().Add(receiveLifecycleWait)); err != nil {
		t.Fatal(err)
	}
	if _, err := peer.Write(frame); err != nil {
		t.Fatalf("write inbound frame: %v", err)
	}
}

func receiveLifecycleRead(t *testing.T, peer net.Conn) []byte {
	t.Helper()
	if err := peer.SetReadDeadline(time.Now().Add(receiveLifecycleWait)); err != nil {
		t.Fatal(err)
	}
	var header [16]byte
	if _, err := io.ReadFull(peer, header[:]); err != nil {
		t.Fatalf("receive required response header: %v", err)
	}
	length := binary.BigEndian.Uint32(header[:4])
	if length < 16 || length > data.MAX_PDU_LEN {
		t.Fatalf("invalid response length %d", length)
	}
	frame := make([]byte, length)
	copy(frame, header[:])
	if _, err := io.ReadFull(peer, frame[16:]); err != nil {
		t.Fatal(err)
	}
	return frame
}

func receiveLifecycleMarshal(t *testing.T, p pdu.PDU) []byte {
	t.Helper()
	buffer := pdu.NewBuffer(nil)
	p.Marshal(buffer)
	if err := buffer.Err(); err != nil {
		t.Fatal(err)
	}
	return append([]byte(nil), buffer.Bytes()...)
}

func receiveLifecycleHeader(command data.CommandIDType, sequence int32, body []byte) []byte {
	frame := make([]byte, 16+len(body))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(frame)))
	binary.BigEndian.PutUint32(frame[4:8], uint32(command))
	binary.BigEndian.PutUint32(frame[12:16], uint32(sequence))
	copy(frame[16:], body)
	return frame
}

func receiveLifecycleCheckHeader(t *testing.T, frame []byte, command data.CommandIDType, status data.CommandStatusType, sequence int32) {
	t.Helper()
	if len(frame) != 16 {
		t.Fatalf("error response has %d bytes, want header only", len(frame))
	}
	got := pdu.ParseHeader([16]byte(frame))
	if got.CommandID != command || got.CommandStatus != status || got.SequenceNumber != sequence {
		t.Fatalf("response header = command %#x status %#x sequence %d; want command %#x status %#x sequence %d",
			got.CommandID, got.CommandStatus, got.SequenceNumber, command, status, sequence)
	}
}

// receiveLifecyclePair uses the production client or server receive path. The
// server fixture includes accept, bind, registration and connection cleanup.
func receiveLifecyclePair(t *testing.T, server bool) (net.Conn, *transceivable, <-chan error) {
	t.Helper()
	errorsReceived := make(chan error, 16)
	if !server {
		conn, peer := net.Pipe()
		trx := newTransceivable(NewConnection(conn), Settings{
			OnAllPDU: func(p pdu.PDU) (pdu.PDU, bool) {
				if p.CanResponse() {
					return p.GetResponse(), false
				}
				return nil, false
			},
			OnReceivingError: func(err error) { errorsReceived <- err },
		}, nil)
		trx.start()
		t.Cleanup(func() { _ = peer.Close(); _ = trx.Close() })
		return peer, trx, errorsReceived
	}
	srv, address := startTestServer(t, ServerSettings{OnError: func(err error) { errorsReceived <- err }})
	if err := srv.StartSystemID(SystemIDConfig{SystemID: "sys", Password: "pw"}); err != nil {
		t.Fatal(err)
	}
	peer, response := bind(t, address, "sys", "pw", pdu.Transceiver)
	if response.CommandStatus != data.ESME_ROK {
		t.Fatalf("server bind status = %#x, want success", response.CommandStatus)
	}
	var trx *transceivable
	receiveLifecycleAwait(t, func() bool {
		srv.mu.Lock()
		defer srv.mu.Unlock()
		for session := range srv.sessions["sys"] {
			trx = session.trx.Load()
		}
		return trx != nil
	}, "server session was not registered")
	return peer, trx, errorsReceived
}

func receiveLifecycleContinues(t *testing.T, peer net.Conn) {
	t.Helper()
	request := pdu.NewEnquireLink()
	request.SetSequenceNumber(91)
	receiveLifecycleWrite(t, peer, receiveLifecycleMarshal(t, request))
	receiveLifecycleCheckHeader(t, receiveLifecycleRead(t, peer), data.ENQUIRE_LINK_RESP, data.ESME_ROK, 91)
}

func receiveLifecycleDecodeError(t *testing.T, received <-chan error, status data.CommandStatusType, sequence int32) {
	t.Helper()
	select {
	case err := <-received:
		var decode *pdu.DecodeError
		if !errors.As(err, &decode) || decode.Status != status || decode.Header.SequenceNumber != sequence {
			t.Fatalf("receive error = %v; want DecodeError status %#x sequence %d", err, status, sequence)
		}
	case <-time.After(receiveLifecycleWait):
		t.Fatal("invalid PDU was not reported through the receiving error callback")
	}
}

func TestLifecycleUnknownCommandNackKeepsConnection(t *testing.T) {
	for _, server := range []bool{false, true} {
		name := "client"
		if server {
			name = "server"
		}
		t.Run(name, func(t *testing.T) {
			peer, _, errorsReceived := receiveLifecyclePair(t, server)
			// SMPP v3.4 §§3.3, 4.3.1: an unknown command receives a
			// generic_nack echoing its sequence, and its consumed frame is safe.
			receiveLifecycleWrite(t, peer, receiveLifecycleHeader(0x12345, 42, []byte{1, 2, 3}))
			receiveLifecycleCheckHeader(t, receiveLifecycleRead(t, peer), data.GENERIC_NACK, data.ESME_RINVCMDID, 42)
			receiveLifecycleDecodeError(t, errorsReceived, data.ESME_RINVCMDID, 42)
			receiveLifecycleContinues(t, peer)
		})
	}
}

func TestLifecycleMalformedRequestRespondsAndContinues(t *testing.T) {
	// SMPP v3.4 §2.8 chooses the request's error response; §4.3 uses
	// generic_nack for header errors. v5 §3.2.1.3 permits header-only errors.
	cases := []struct {
		name     string
		request  func() pdu.PDU
		short    bool
		status   data.CommandStatusType
		response data.CommandIDType
	}{
		{"submit_message_length", pdu.NewSubmitSM, true, data.ESME_RINVMSGLEN, data.SUBMIT_SM_RESP},
		{"submit_tlv", pdu.NewSubmitSM, false, data.ESME_RINVOPTPARSTREAM, data.SUBMIT_SM_RESP},
		{"deliver_tlv", pdu.NewDeliverSM, false, data.ESME_RINVOPTPARSTREAM, data.DELIVER_SM_RESP},
		{"data_tlv", pdu.NewDataSM, false, data.ESME_RINVOPTPARSTREAM, data.DATA_SM_RESP},
		{"query_tlv", pdu.NewQuerySM, false, data.ESME_RINVOPTPARSTREAM, data.QUERY_SM_RESP},
		{"cancel_tlv", pdu.NewCancelSM, false, data.ESME_RINVOPTPARSTREAM, data.CANCEL_SM_RESP},
		{"replace_tlv", pdu.NewReplaceSM, false, data.ESME_RINVOPTPARSTREAM, data.REPLACE_SM_RESP},
		{"multi_tlv", pdu.NewSubmitMulti, false, data.ESME_RINVOPTPARSTREAM, data.SUBMIT_MULTI_RESP},
		{"alert_no_response_tlv", pdu.NewAlertNotification, false, data.ESME_RINVOPTPARSTREAM, 0},
	}
	for _, server := range []bool{false, true} {
		name := "client"
		if server {
			name = "server"
		}
		t.Run(name, func(t *testing.T) {
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					request := tc.request()
					request.SetSequenceNumber(43)
					if tc.short {
						message, err := pdu.NewShortMessage("A")
						if err != nil {
							t.Fatal(err)
						}
						request.(*pdu.SubmitSM).Message = message
					}
					frame := receiveLifecycleMarshal(t, request)
					if tc.short {
						frame = frame[:len(frame)-1]
					} else {
						frame = append(frame, 0x04, 0x24, 0x00, 0x02, 0x01)
					}
					binary.BigEndian.PutUint32(frame[:4], uint32(len(frame)))
					_, err := pdu.Parse(bytes.NewReader(frame))
					var decode *pdu.DecodeError
					if !errors.As(err, &decode) || decode.Status != tc.status {
						t.Fatalf("test vector = %v, want DecodeError status %#x", err, tc.status)
					}
					peer, _, errorsReceived := receiveLifecyclePair(t, server)
					receiveLifecycleWrite(t, peer, frame)
					if tc.response != 0 {
						receiveLifecycleCheckHeader(t, receiveLifecycleRead(t, peer), tc.response, tc.status, 43)
					}
					receiveLifecycleDecodeError(t, errorsReceived, tc.status, 43)
					receiveLifecycleContinues(t, peer)
				})
			}
			t.Run("mandatory_body_too_short", func(t *testing.T) {
				peer, _, errorsReceived := receiveLifecyclePair(t, server)
				receiveLifecycleWrite(t, peer, receiveLifecycleHeader(data.SUBMIT_SM, 44, nil))
				receiveLifecycleCheckHeader(t, receiveLifecycleRead(t, peer), data.GENERIC_NACK, data.ESME_RINVCMDLEN, 44)
				receiveLifecycleDecodeError(t, errorsReceived, data.ESME_RINVCMDLEN, 44)
				receiveLifecycleContinues(t, peer)
			})
		})
	}
}

// A read-side gate returns a deterministic incomplete-frame error while keeping
// the write side open, so a framing-error nack can be observed on the wire.
type receiveLifecycleReadLimit struct {
	net.Conn
	mu        sync.Mutex
	remaining int
	active    bool
}

func (c *receiveLifecycleReadLimit) limit(n int) {
	c.mu.Lock()
	c.remaining, c.active = n, true
	c.mu.Unlock()
}

func (c *receiveLifecycleReadLimit) Read(buffer []byte) (int, error) {
	c.mu.Lock()
	if c.active {
		if c.remaining == 0 {
			c.mu.Unlock()
			return 0, io.ErrUnexpectedEOF
		}
		buffer = buffer[:min(len(buffer), c.remaining)]
	}
	c.mu.Unlock()
	n, err := c.Conn.Read(buffer)
	c.mu.Lock()
	if c.active {
		c.remaining -= n
	}
	c.mu.Unlock()
	return n, err
}

func TestLifecycleFramingErrorNackThenClose(t *testing.T) {
	for _, server := range []bool{false, true} {
		name := "client"
		if server {
			name = "server"
		}
		t.Run(name, func(t *testing.T) {
			for _, kind := range []string{"length_too_short", "length_too_long", "incomplete_body"} {
				t.Run(kind, func(t *testing.T) {
					var peer net.Conn
					var limited *receiveLifecycleReadLimit
					var errorsReceived <-chan error
					var trx *transceivable
					if server {
						peer, trx, errorsReceived = receiveLifecyclePair(t, true)
					} else {
						conn, remote := net.Pipe()
						peer = remote
						limited = &receiveLifecycleReadLimit{Conn: conn}
						received := make(chan error, 8)
						errorsReceived = received
						trx = newTransceivable(NewConnection(limited), Settings{OnReceivingError: func(err error) { received <- err }}, nil)
						trx.start()
					}
					t.Cleanup(func() { _ = peer.Close(); _ = trx.Close() })
					frame := receiveLifecycleHeader(data.SUBMIT_SM, 45, nil)
					switch kind {
					case "length_too_short":
						binary.BigEndian.PutUint32(frame[:4], 15)
					case "length_too_long":
						binary.BigEndian.PutUint32(frame[:4], data.MAX_PDU_LEN+1)
					case "incomplete_body":
						binary.BigEndian.PutUint32(frame[:4], 20)
						frame = append(frame, 0, 0)
						if limited != nil {
							limited.limit(len(frame))
						}
					}
					receiveLifecycleWrite(t, peer, frame)
					if server && kind == "incomplete_body" {
						// TCP half-close terminates the incomplete request while
						// preserving the read side for its required negative reply.
						if err := peer.(*net.TCPConn).CloseWrite(); err != nil {
							t.Fatal(err)
						}
					}
					receiveLifecycleCheckHeader(t, receiveLifecycleRead(t, peer), data.GENERIC_NACK, data.ESME_RINVCMDLEN, 45)
					select {
					case err := <-errorsReceived:
						want := pdu.ErrInvalidCommandLength
						if kind == "incomplete_body" {
							want = pdu.ErrIncompletePDU
						}
						if !errors.Is(err, want) {
							t.Fatalf("framing error = %v, want %v", err, want)
						}
					case <-time.After(receiveLifecycleWait):
						t.Fatal("framing error was not reported")
					}
					// net.Pipe rejects deadline changes after the remote has closed;
					// the following read still reports the expected EOF immediately.
					_ = peer.SetReadDeadline(time.Now().Add(receiveLifecycleWait))
					var one [1]byte
					_, err := peer.Read(one[:])
					if !errors.Is(err, io.EOF) {
						t.Fatalf("read after framing nack = %v, want EOF", err)
					}
				})
			}
		})
	}
}

func TestLifecycleMalformedResponseRetiresWindowWithError(t *testing.T) {
	conn, peer := net.Pipe()
	errorsReceived := make(chan error, 8)
	responses := make(chan Response, 2)
	store := NewDefaultStore()
	trx := newTransceivable(NewConnection(conn), Settings{
		OnReceivingError: func(err error) { errorsReceived <- err },
		WindowedRequestTracking: &WindowedRequestTracking{
			MaxWindowSize: 1, StoreAccessTimeOut: time.Second,
			EnableAutoRespond:     true,
			OnExpectedPduResponse: func(response Response) { responses <- response },
		},
	}, store)
	trx.start()
	t.Cleanup(func() { _ = peer.Close(); _ = trx.Close() })
	request := pdu.NewSubmitSM()
	request.SetSequenceNumber(46)
	if err := trx.Submit(request); err != nil {
		t.Fatal(err)
	}
	_ = receiveLifecycleRead(t, peer)
	receiveLifecycleAwait(t, func() bool { size, _ := store.Length(context.Background()); return size == 1 }, "request did not enter the window")
	// A complete submit_sm_resp followed by a truncated optional TLV is a
	// DecodeError rather than a transport failure.
	frame := receiveLifecycleHeader(data.SUBMIT_SM_RESP, 46, []byte{0, 0x04, 0x24, 0, 2, 1})
	receiveLifecycleWrite(t, peer, frame)
	receiveLifecycleDecodeError(t, errorsReceived, data.ESME_RINVOPTPARSTREAM, 46)
	select {
	case response := <-responses:
		if response.OriginalRequest.PDU != request {
			t.Fatal("malformed response did not complete its original request")
		}
		if response.Err == nil {
			t.Fatal("malformed response completion did not carry Response.Err")
		}
		var decode *pdu.DecodeError
		if !errors.As(response.Err, &decode) || decode.Header.SequenceNumber != 46 {
			t.Fatalf("Response.Err = %v, want matching DecodeError", response.Err)
		}
	case <-time.After(receiveLifecycleWait):
		t.Fatal("malformed response did not retire the matching window request")
	}
	size, err := store.Length(context.Background())
	if err != nil || size != 0 {
		t.Fatalf("window after malformed response = %d, %v; want zero", size, err)
	}
	receiveLifecycleContinues(t, peer)
}

func TestServerLifecycleMalformedResponseReportsAndContinues(t *testing.T) {
	peer, _, errorsReceived := receiveLifecyclePair(t, true)
	receiveLifecycleWrite(t, peer, receiveLifecycleHeader(data.DELIVER_SM_RESP, 47, []byte{0, 0x04, 0x24, 0, 2, 1}))
	receiveLifecycleDecodeError(t, errorsReceived, data.ESME_RINVOPTPARSTREAM, 47)
	receiveLifecycleContinues(t, peer)
}

func TestLifecycleMalformedResponseWithoutWindowEntryReportsAndContinues(t *testing.T) {
	conn, peer := net.Pipe()
	errorsReceived := make(chan error, 8)
	var completed atomic.Int32
	store := NewDefaultStore()
	trx := newTransceivable(NewConnection(conn), Settings{
		OnReceivingError: func(err error) { errorsReceived <- err },
		WindowedRequestTracking: &WindowedRequestTracking{
			MaxWindowSize: 1, StoreAccessTimeOut: time.Second, EnableAutoRespond: true,
			OnExpectedPduResponse: func(Response) { completed.Add(1) },
		},
	}, store)
	trx.start()
	t.Cleanup(func() { _ = peer.Close(); _ = trx.Close() })
	receiveLifecycleWrite(t, peer, receiveLifecycleHeader(data.SUBMIT_SM_RESP, 48, []byte{0, 0x04, 0x24, 0, 2, 1}))
	receiveLifecycleDecodeError(t, errorsReceived, data.ESME_RINVOPTPARSTREAM, 48)
	receiveLifecycleContinues(t, peer)
	if completed.Load() != 0 {
		t.Fatal("malformed response completed a request that was absent from the window")
	}
	size, err := store.Length(context.Background())
	if err != nil || size != 0 {
		t.Fatalf("window after unsolicited malformed response = %d, %v; want zero", size, err)
	}
}

type receiveLifecycleWriteGate struct {
	net.Conn
	armed   atomic.Bool
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	err     error
}

func (c *receiveLifecycleWriteGate) Write(buffer []byte) (int, error) {
	if c.armed.Load() {
		c.once.Do(func() { close(c.entered) })
		<-c.release
		return 0, c.err
	}
	return c.Conn.Write(buffer)
}

func receiveLifecyclePDUInError(err error) pdu.PDU {
	var submitError *SubmitError
	if errors.As(err, &submitError) {
		return submitError.PDU
	}
	return nil
}

func TestLifecycleFailedConnectionReportsEveryQueuedPDU(t *testing.T) {
	for _, server := range []bool{false, true} {
		name := "client"
		if server {
			name = "server"
		}
		t.Run(name, func(t *testing.T) {
			conn, peer := net.Pipe()
			failure := errors.New("synthetic permanent write failure")
			gate := &receiveLifecycleWriteGate{Conn: conn, entered: make(chan struct{}), release: make(chan struct{}), err: failure}
			errorsReceived := make(chan struct {
				p   pdu.PDU
				err error
			}, 8)
			var trx *transceivable
			var srv *Server
			if server {
				var listener *pipeListener
				srv, listener = pipeServer(t, ServerSettings{OnError: func(err error) {
					errorsReceived <- struct {
						p   pdu.PDU
						err error
					}{receiveLifecyclePDUInError(err), err}
				}}, 0)
				pipeAccount(t, srv, pdu.Transceiver, 0)
				listener.conns <- gate
				bindPipe(t, peer, pdu.Transceiver, data.SMPP_V34)
				receiveLifecycleAwait(t, func() bool {
					srv.mu.Lock()
					defer srv.mu.Unlock()
					for session := range srv.sessions["sys"] {
						trx = session.trx.Load()
					}
					return trx != nil
				}, "server session was not registered")
			} else {
				trx = newTransceivable(NewConnection(gate), Settings{OnSubmitError: func(p pdu.PDU, err error) {
					errorsReceived <- struct {
						p   pdu.PDU
						err error
					}{p, err}
				}}, nil)
				trx.start()
			}
			t.Cleanup(func() { _ = peer.Close(); _ = trx.Close() })
			gate.armed.Store(true)
			first, queued := pdu.NewDeliverSM(), pdu.NewDeliverSM()
			submit := trx.Submit
			if server {
				submit = func(p pdu.PDU) error { return srv.SubmitToSystemID(context.Background(), "sys", p) }
			}
			if err := submit(first); err != nil {
				t.Fatal(err)
			}
			receiveLifecycleSignal(t, gate.entered, "first PDU did not reach the gated write")
			if err := submit(queued); err != nil {
				t.Fatal(err)
			}
			close(gate.release)
			got := make(map[pdu.PDU]error)
			deadline := time.After(receiveLifecycleWait)
			for len(got) < 2 {
				select {
				case result := <-errorsReceived:
					if result.p == first || result.p == queued {
						if _, duplicate := got[result.p]; duplicate {
							t.Fatalf("PDU %d was reported more than once", result.p.GetSequenceNumber())
						}
						got[result.p] = result.err
					}
				case <-deadline:
					_ = peer.Close()
					_ = trx.Close()
					t.Fatalf("failed connection reported %d of 2 PDUs; queued PDU %d must reach the error callback with the PDU attached", len(got), queued.GetSequenceNumber())
				}
			}
			if !errors.Is(got[first], failure) || !errors.Is(got[queued], ErrConnectionClosing) {
				t.Fatalf("submit errors = first %v, queued %v; want write failure and ErrConnectionClosing", got[first], got[queued])
			}
		})
	}
}

func TestServerLifecycleStopWaitsForActiveHandlerAfterWriteFailure(t *testing.T) {
	for _, accountOnly := range []bool{false, true} {
		name := "Stop"
		if accountOnly {
			name = "StopSystemID"
		}
		t.Run(name, func(t *testing.T) {
			baseline := runtime.NumGoroutine()
			conn, peer := net.Pipe()
			gate := &receiveLifecycleWriteGate{Conn: conn, entered: make(chan struct{}), release: make(chan struct{}), err: errors.New("synthetic write failure")}
			entered, release, handlerDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			srv, listener := pipeServer(t, ServerSettings{}, 0)
			t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
			if err := srv.StartSystemID(SystemIDConfig{SystemID: "sys", Password: "pw", OnPDU: func(_ ServerSessionInfo, _ pdu.PDU) (pdu.PDU, bool) {
				close(entered)
				<-release
				close(handlerDone)
				return nil, false
			}}); err != nil {
				t.Fatal(err)
			}
			listener.conns <- gate
			t.Cleanup(func() { _ = peer.Close() })
			bindPipe(t, peer, pdu.Transceiver, data.SMPP_V34)
			var session *serverSession
			receiveLifecycleAwait(t, func() bool {
				srv.mu.Lock()
				defer srv.mu.Unlock()
				for bound := range srv.sessions["sys"] {
					session = bound
				}
				return session != nil
			}, "server session was not registered")
			receiveLifecycleWrite(t, peer, receiveLifecycleMarshal(t, pdu.NewSubmitSM()))
			receiveLifecycleSignal(t, entered, "handler did not start")
			gate.armed.Store(true)
			if err := srv.SubmitToSystemID(context.Background(), "sys", pdu.NewDeliverSM()); err != nil {
				t.Fatal(err)
			}
			receiveLifecycleSignal(t, gate.entered, "outbound failure did not start")
			close(gate.release)
			trx := session.trx.Load()
			receiveLifecycleAwait(t, func() bool {
				return atomic.LoadInt32(&trx.in.aliveState) == Closed && atomic.LoadInt32(&trx.out.aliveState) == Closed
			}, "asynchronous I/O shutdown did not reach both directions")
			stopped := make(chan struct{})
			go func() {
				if accountOnly {
					srv.StopSystemID("sys")
				} else {
					srv.Stop()
				}
				close(stopped)
			}()
			receiveLifecycleAwait(t, func() bool {
				srv.mu.Lock()
				defer srv.mu.Unlock()
				return srv.credStore["sys"] == nil || srv.stopped
			}, "shutdown did not start")
			select {
			case <-stopped:
				releaseOnce.Do(func() { close(release) })
				receiveLifecycleSignal(t, handlerDone, "handler did not return after release")
				t.Fatal("shutdown returned while the account handler was still running")
			case <-time.After(25 * time.Millisecond):
				// The handler remains blocked at its explicit release barrier.
			}
			releaseOnce.Do(func() { close(release) })
			receiveLifecycleSignal(t, stopped, "shutdown did not finish after the handler returned")
			receiveLifecycleSignal(t, handlerDone, "shutdown finished before the handler returned")
			srv.Stop()
			_ = peer.Close()
			receiveLifecycleNoGoroutineLeak(t, baseline)
		})
	}
}

func TestServerLifecycleConcurrentStopSystemIDJoinsShutdown(t *testing.T) {
	baseline := runtime.NumGoroutine()
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	srv, listener := pipeServer(t, ServerSettings{}, 0)
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	if err := srv.StartSystemID(SystemIDConfig{SystemID: "sys", Password: "pw",
		OnPDU: func(_ ServerSessionInfo, _ pdu.PDU) (pdu.PDU, bool) {
			close(entered)
			<-release
			return nil, false
		}}); err != nil {
		t.Fatal(err)
	}
	peer := pipeDial(t, listener)
	bindPipe(t, peer, pdu.Transceiver, data.SMPP_V34)
	receiveLifecycleAwait(t, func() bool { return len(srv.GetSystemIDStatus("sys")) == 1 }, "server session was not registered")
	receiveLifecycleWrite(t, peer, receiveLifecycleMarshal(t, pdu.NewSubmitSM()))
	receiveLifecycleSignal(t, entered, "handler did not start")
	firstDone := make(chan struct{})
	go func() { srv.StopSystemID("sys"); close(firstDone) }()
	receiveLifecycleAwait(t, func() bool {
		srv.mu.Lock()
		defer srv.mu.Unlock()
		return srv.credStore["sys"] == nil
	}, "first StopSystemID did not remove the account")
	secondDone := make(chan struct{})
	go func() { srv.StopSystemID("sys"); close(secondDone) }()
	select {
	case <-secondDone:
		releaseOnce.Do(func() { close(release) })
		receiveLifecycleSignal(t, firstDone, "first StopSystemID did not finish after release")
		t.Fatal("concurrent StopSystemID returned while the removed account's handler was still running")
	case <-time.After(25 * time.Millisecond):
	}
	releaseOnce.Do(func() { close(release) })
	receiveLifecycleSignal(t, firstDone, "first StopSystemID did not join its handler")
	receiveLifecycleSignal(t, secondDone, "second StopSystemID did not join the existing shutdown")
	srv.Stop()
	_ = peer.Close()
	receiveLifecycleNoGoroutineLeak(t, baseline)
}

func TestServerLifecycleEncodeFailureLeavesSessionUsable(t *testing.T) {
	errorsReceived := make(chan error, 8)
	srv, listener := pipeServer(t, ServerSettings{OnError: func(err error) { errorsReceived <- err }}, 0)
	pipeAccount(t, srv, pdu.Transceiver, 0)
	peer := pipeDial(t, listener)
	bindPipe(t, peer, pdu.Transceiver, data.SMPP_V34)
	receiveLifecycleAwait(t, func() bool { return len(srv.GetSystemIDStatus("sys")) == 1 }, "server session was not registered")
	// An embedded NUL cannot be represented in a C-Octet field. SubmitToSystemID
	// encodes before queuing, so the caller gets the error and nothing is sent.
	invalid := pdu.NewDeliverSM().(*pdu.DeliverSM)
	invalid.ServiceType = "a\x00b"
	invalid.SetSequenceNumber(61)
	err := srv.SubmitToSystemID(context.Background(), "sys", invalid)
	var encodeErr *pdu.EncodeError
	if !errors.Is(err, pdu.ErrEncode) || !errors.As(err, &encodeErr) || encodeErr.PDU != invalid {
		t.Fatalf("encode failure = %v; want ErrEncode carrying the offending PDU", err)
	}
	select {
	case err := <-errorsReceived:
		t.Fatalf("a synchronously rejected PDU was also reported asynchronously: %v", err)
	default:
	}
	// The next successful socket write proves the session stayed bound and no
	// partial bytes of the rejected PDU reached the wire.
	valid := pdu.NewDeliverSM()
	valid.SetSequenceNumber(62)
	if err := srv.SubmitToSystemID(context.Background(), "sys", valid); err != nil {
		t.Fatalf("valid submission after encode failure: %v", err)
	}
	frame := receiveLifecycleRead(t, peer)
	if binary.BigEndian.Uint32(frame[4:8]) != uint32(data.DELIVER_SM) || binary.BigEndian.Uint32(frame[12:16]) != 62 {
		t.Fatalf("write after encode failure = %x, want deliver_sm sequence 62", frame)
	}
	receiveLifecycleContinues(t, peer)
}

func TestServerLifecycleHandlerResponseEncodeFailureLeavesSessionUsable(t *testing.T) {
	errorsReceived := make(chan error, 8)
	invalid := pdu.NewSubmitSMResp().(*pdu.SubmitSMResp)
	invalid.MessageID = "invalid\x00message"
	srv, listener := pipeServer(t, ServerSettings{OnError: func(err error) { errorsReceived <- err }}, 0)
	if err := srv.StartSystemID(SystemIDConfig{SystemID: "sys", Password: "pw",
		OnPDU: func(_ ServerSessionInfo, request pdu.PDU) (pdu.PDU, bool) {
			invalid.SetSequenceNumber(request.GetSequenceNumber())
			return invalid, false
		}}); err != nil {
		t.Fatal(err)
	}
	peer := pipeDial(t, listener)
	bindPipe(t, peer, pdu.Transceiver, data.SMPP_V34)
	receiveLifecycleAwait(t, func() bool { return len(srv.GetSystemIDStatus("sys")) == 1 }, "server session was not registered")
	request := pdu.NewSubmitSM()
	request.SetSequenceNumber(63)
	receiveLifecycleWrite(t, peer, receiveLifecycleMarshal(t, request))
	select {
	case err := <-errorsReceived:
		if !errors.Is(err, pdu.ErrEncode) || receiveLifecyclePDUInError(err) != invalid {
			t.Fatalf("handler response encode failure = %v; want SubmitError carrying the offending response and ErrEncode", err)
		}
	case <-time.After(receiveLifecycleWait):
		t.Fatal("server did not report its handler response encoding failure")
	}
	// The request is still answered: a header-only ESME_RSYSERR replaces the
	// response that could not be encoded (SMPP v3.4 §2.8, v5 §3.2.1.3).
	reply := receiveLifecycleRead(t, peer)
	if length := binary.BigEndian.Uint32(reply[:4]); length != 16 {
		t.Fatalf("fallback reply length = %d, want header-only 16", length)
	}
	receiveLifecycleCheckHeader(t, reply, data.SUBMIT_SM_RESP, data.ESME_RSYSERR, 63)
	receiveLifecycleContinues(t, peer)
}

func TestLifecycleHandlerResponseEncodeFailureHonorsCloseAfterFallback(t *testing.T) {
	conn, peer := net.Pipe()
	invalid := pdu.NewSubmitSMResp().(*pdu.SubmitSMResp)
	invalid.MessageID = "invalid\x00message"
	submits := make(chan struct {
		p   pdu.PDU
		err error
	}, 2)
	trx := newTransceivable(NewConnection(conn), Settings{
		OnAllPDU: func(request pdu.PDU) (pdu.PDU, bool) {
			if request.GetHeader().CommandID == data.SUBMIT_SM {
				invalid.SetSequenceNumber(request.GetSequenceNumber())
				return invalid, true
			}
			return request.GetResponse(), false
		},
		OnSubmitError: func(p pdu.PDU, err error) {
			submits <- struct {
				p   pdu.PDU
				err error
			}{p, err}
		},
	}, nil)
	trx.start()
	t.Cleanup(func() { _ = peer.Close(); _ = trx.Close() })
	request := pdu.NewSubmitSM()
	request.SetSequenceNumber(64)
	receiveLifecycleWrite(t, peer, receiveLifecycleMarshal(t, request))
	select {
	case result := <-submits:
		if result.p != invalid || !errors.Is(result.err, pdu.ErrEncode) {
			t.Fatalf("response submit error = %T, %v; want offending response and ErrEncode", result.p, result.err)
		}
	case <-time.After(receiveLifecycleWait):
		t.Fatal("response encoding failure did not reach OnSubmitError")
	}
	// SMPP v3.4 §2.8: the request must still be answered. The callback's
	// closeBind decision applies after that header-only fallback is written.
	receiveLifecycleCheckHeader(t, receiveLifecycleRead(t, peer), data.SUBMIT_SM_RESP, data.ESME_RSYSERR, 64)
	_ = peer.SetReadDeadline(time.Now().Add(receiveLifecycleWait))
	var one [1]byte
	_, err := peer.Read(one[:])
	if !errors.Is(err, io.EOF) {
		t.Fatalf("read after error fallback = %v, want EOF", err)
	}
}

func TestLifecycleNilResponseDoesNotWaitForOutboundCapacity(t *testing.T) {
	for _, window := range []bool{false, true} {
		name := "OnAllPDU"
		if window {
			name = "OnReceivedPduRequest"
		}
		t.Run(name, func(t *testing.T) {
			conn, peer := net.Pipe()
			gate := &receiveLifecycleWriteGate{Conn: conn, entered: make(chan struct{}), release: make(chan struct{}), err: errors.New("released write gate")}
			gate.armed.Store(true)
			processed := make(chan int32, 2)
			handler := func(p pdu.PDU) (pdu.PDU, bool) { processed <- p.GetSequenceNumber(); return nil, false }
			settings := Settings{OnAllPDU: handler}
			var store RequestStore
			if window {
				store = NewDefaultStore()
				settings.WindowedRequestTracking = &WindowedRequestTracking{MaxWindowSize: 2, StoreAccessTimeOut: time.Second,
					OnReceivedPduRequest: handler, OnExpectedPduResponse: func(Response) {}}
			}
			trx := newTransceivable(NewConnection(gate), settings, store)
			trx.start()
			t.Cleanup(func() { close(gate.release); _ = peer.Close(); _ = trx.Close() })
			if err := trx.Submit(pdu.NewSubmitSM()); err != nil {
				t.Fatal(err)
			}
			receiveLifecycleSignal(t, gate.entered, "outbound writer did not block")
			if err := trx.Submit(pdu.NewSubmitSM()); err != nil {
				t.Fatal(err)
			}
			first, second := pdu.NewEnquireLink(), pdu.NewEnquireLink()
			first.SetSequenceNumber(51)
			second.SetSequenceNumber(52)
			receiveLifecycleWrite(t, peer, receiveLifecycleMarshal(t, first))
			select {
			case <-processed:
			case <-time.After(receiveLifecycleWait):
				t.Fatal("first inbound request was not processed")
			}
			writeDone := make(chan error, 1)
			frame := receiveLifecycleMarshal(t, second)
			go func() { _, err := peer.Write(frame); writeDone <- err }()
			select {
			case sequence := <-processed:
				if sequence != 52 {
					t.Fatalf("second callback sequence = %d, want 52", sequence)
				}
			case <-time.After(receiveLifecycleWait):
				t.Fatal("nil handler response blocked the reader on the full outbound queue")
			}
			select {
			case err := <-writeDone:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(receiveLifecycleWait):
				t.Fatal("second inbound write did not complete")
			}
		})
	}
}

func TestServerLifecycleNilResponseDoesNotWaitForOutboundCapacity(t *testing.T) {
	conn, peer := net.Pipe()
	gate := &receiveLifecycleWriteGate{Conn: conn, entered: make(chan struct{}), release: make(chan struct{}), err: errors.New("released write gate")}
	processed := make(chan int32, 2)
	srv, listener := pipeServer(t, ServerSettings{}, 0)
	if err := srv.StartSystemID(SystemIDConfig{SystemID: "sys", Password: "pw",
		OnPDU: func(_ ServerSessionInfo, request pdu.PDU) (pdu.PDU, bool) {
			processed <- request.GetSequenceNumber()
			return nil, false
		}}); err != nil {
		t.Fatal(err)
	}
	listener.conns <- gate
	bindPipe(t, peer, pdu.Transceiver, data.SMPP_V34)
	receiveLifecycleAwait(t, func() bool { return len(srv.GetSystemIDStatus("sys")) == 1 }, "server session was not registered")
	gate.armed.Store(true)
	t.Cleanup(func() { close(gate.release); _ = peer.Close() })
	if err := srv.SubmitToSystemID(context.Background(), "sys", pdu.NewDeliverSM()); err != nil {
		t.Fatal(err)
	}
	receiveLifecycleSignal(t, gate.entered, "server outbound writer did not block")
	if err := srv.SubmitToSystemID(context.Background(), "sys", pdu.NewDeliverSM()); err != nil {
		t.Fatal(err)
	}
	first, second := pdu.NewSubmitSM(), pdu.NewSubmitSM()
	first.SetSequenceNumber(53)
	second.SetSequenceNumber(54)
	receiveLifecycleWrite(t, peer, receiveLifecycleMarshal(t, first))
	select {
	case <-processed:
	case <-time.After(receiveLifecycleWait):
		t.Fatal("first server handler did not run")
	}
	writeDone := make(chan error, 1)
	frame := receiveLifecycleMarshal(t, second)
	go func() { _, err := peer.Write(frame); writeDone <- err }()
	select {
	case sequence := <-processed:
		if sequence != 54 {
			t.Fatalf("second server handler sequence = %d, want 54", sequence)
		}
	case <-time.After(receiveLifecycleWait):
		t.Fatal("nil account-handler response blocked the reader on the full outbound queue")
	}
	select {
	case err := <-writeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(receiveLifecycleWait):
		t.Fatal("second inbound server write did not complete")
	}
}
