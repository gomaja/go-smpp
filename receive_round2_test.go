package gosmpp

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/gomaja/go-smpp/data"
	"github.com/gomaja/go-smpp/pdu"
)

func TestLifecycleWindowWithoutRequestHandlerUsesRequestCallbacks(t *testing.T) {
	for _, path := range []string{"default", "OnPDU", "OnAllPDU"} {
		for _, auto := range []bool{false, true} {
			name := path + "/manual_housekeeping"
			if auto {
				name = path + "/automatic_housekeeping"
			}
			t.Run(name, func(t *testing.T) {
				conn, peer := net.Pipe()
				called := make(chan int32, 4)
				settings := Settings{WindowedRequestTracking: &WindowedRequestTracking{
					MaxWindowSize: 1, StoreAccessTimeOut: time.Second, EnableAutoRespond: auto,
				}}
				switch path {
				case "OnPDU":
					settings.OnPDU = func(p pdu.PDU, responded bool) {
						if !responded {
							t.Error("request was not answered before OnPDU")
						}
						called <- p.GetSequenceNumber()
					}
				case "OnAllPDU":
					settings.OnAllPDU = func(p pdu.PDU) (pdu.PDU, bool) {
						called <- p.GetSequenceNumber()
						return p.GetResponse(), false
					}
				}
				store := NewDefaultStore()
				trx := newTransceivable(NewConnection(conn), settings, store)
				trx.start()
				t.Cleanup(func() { _ = peer.Close(); _ = trx.Close() })
				// Tracking remains active when no response callback is configured.
				outbound := pdu.NewSubmitSM()
				outbound.SetSequenceNumber(70)
				if err := trx.Submit(outbound); err != nil {
					t.Fatal(err)
				}
				_ = receiveLifecycleRead(t, peer)
				receiveLifecycleWrite(t, peer, receiveLifecycleMarshal(t, outbound.GetResponse()))
				receiveLifecycleAwait(t, func() bool { size, _ := store.Length(context.Background()); return size == 0 }, "response without a callback was not retired")
				for _, request := range []pdu.PDU{pdu.NewDeliverSM(), pdu.NewEnquireLink()} {
					request.SetSequenceNumber(71)
					receiveLifecycleWrite(t, peer, receiveLifecycleMarshal(t, request))
					reply := receiveLifecycleRead(t, peer)
					header := pdu.ParseHeader([16]byte(reply[:16]))
					if header.CommandID != request.GetResponse().GetHeader().CommandID || header.SequenceNumber != 71 || header.CommandStatus != data.ESME_ROK {
						t.Fatalf("request reply = %+v, want matching successful response sequence 71", header)
					}
					if path != "default" {
						select {
						case sequence := <-called:
							if sequence != 71 {
								t.Fatalf("request callback sequence = %d, want 71", sequence)
							}
						case <-time.After(receiveLifecycleWait):
							t.Fatal("window tracking suppressed the request callback")
						}
					}
				}
			})
		}
	}
}

func TestLifecycleMalformedResponsePreservesUsableHeader(t *testing.T) {
	conn, peer := net.Pipe()
	type completion struct {
		response   Response
		panicValue any
	}
	completed := make(chan completion, 1)
	trx := newTransceivable(NewConnection(conn), Settings{WindowedRequestTracking: &WindowedRequestTracking{
		MaxWindowSize: 1, StoreAccessTimeOut: time.Second,
		OnExpectedPduResponse: func(response Response) {
			result := completion{response: response}
			defer func() { result.panicValue = recover(); completed <- result }()
			_ = response.IsOk()
			_ = response.GetSequenceNumber()
		},
	}}, NewDefaultStore())
	trx.start()
	t.Cleanup(func() { _ = peer.Close(); _ = trx.Close() })
	request := pdu.NewSubmitSM()
	request.SetSequenceNumber(72)
	if err := trx.Submit(request); err != nil {
		t.Fatal(err)
	}
	_ = receiveLifecycleRead(t, peer)
	frame := receiveLifecycleHeader(data.SUBMIT_SM_RESP, 72, []byte{0, 0x04, 0x24, 0, 2, 1})
	binary.BigEndian.PutUint32(frame[8:12], uint32(data.ESME_RSYSERR))
	receiveLifecycleWrite(t, peer, frame)
	select {
	case result := <-completed:
		if result.panicValue != nil {
			t.Fatalf("malformed response promoted PDU methods panic: %v", result.panicValue)
		}
		response := result.response
		if _, ok := response.PDU.(*pdu.SubmitSMResp); !ok {
			t.Fatalf("malformed response type = %T, want *pdu.SubmitSMResp", response.PDU)
		}
		header := response.GetHeader()
		if header.CommandID != data.SUBMIT_SM_RESP || header.CommandStatus != data.ESME_RSYSERR || header.SequenceNumber != 72 || header.CommandLength != 16 {
			t.Fatalf("malformed response header = %+v, want original command/status/sequence and no body", header)
		}
		var decode *pdu.DecodeError
		if !errors.As(response.Err, &decode) || decode.Status != data.ESME_RINVOPTPARSTREAM || response.OriginalRequest.PDU != request {
			t.Fatalf("malformed response completion = %+v, want original request and decode error", response)
		}
	case <-time.After(receiveLifecycleWait):
		t.Fatal("malformed response did not complete its request")
	}
}

func TestLifecycleHandlerResponseEncodeFailureUsesFallback(t *testing.T) {
	for _, window := range []bool{false, true} {
		for _, closeBind := range []bool{false, true} {
			name := "OnAllPDU/keep_open"
			if window {
				name = "OnReceivedPduRequest/keep_open"
			}
			if closeBind {
				name = name[:len(name)-len("keep_open")] + "close_after_response"
			}
			t.Run(name, func(t *testing.T) {
				conn, peer := net.Pipe()
				invalid := pdu.NewDeliverSMResp().(*pdu.DeliverSMResp)
				invalid.MessageID = "invalid\x00message"
				invalid.SetSequenceNumber(999)
				reported := make(chan pdu.PDU, 2)
				closed := make(chan State, 1)
				handler := func(request pdu.PDU) (pdu.PDU, bool) {
					if request.GetHeader().CommandID == data.DELIVER_SM {
						return invalid, closeBind
					}
					return request.GetResponse(), false
				}
				settings := Settings{WriteTimeout: time.Second, OnAllPDU: handler,
					OnSubmitError: func(p pdu.PDU, err error) {
						if !errors.Is(err, pdu.ErrEncode) {
							t.Errorf("response error = %v, want ErrEncode", err)
						}
						reported <- p
					}, OnClosed: func(state State) { closed <- state },
				}
				var store RequestStore
				if window {
					store = NewDefaultStore()
					settings.WindowedRequestTracking = &WindowedRequestTracking{MaxWindowSize: 1, StoreAccessTimeOut: time.Second, OnReceivedPduRequest: handler}
				}
				trx := newTransceivable(NewConnection(conn), settings, store)
				trx.start()
				t.Cleanup(func() { _ = peer.Close(); _ = trx.Close() })
				request := pdu.NewDeliverSM()
				request.SetSequenceNumber(73)
				receiveLifecycleWrite(t, peer, receiveLifecycleMarshal(t, request))
				receiveLifecycleCheckHeader(t, receiveLifecycleRead(t, peer), data.DELIVER_SM_RESP, data.ESME_RSYSERR, 73)
				select {
				case p := <-reported:
					if p != invalid {
						t.Fatalf("error PDU = %T, want offending response", p)
					}
				case <-time.After(receiveLifecycleWait):
					t.Fatal("response encoding failure was not reported")
				}
				if closeBind {
					_ = peer.SetReadDeadline(time.Now().Add(receiveLifecycleWait))
					_, err := peer.Read(make([]byte, 1))
					if !errors.Is(err, io.EOF) {
						t.Fatalf("read after fallback = %v, want EOF", err)
					}
					select {
					case state := <-closed:
						if state != UnbindClosing {
							t.Fatalf("close state = %v, want UnbindClosing", state)
						}
					case <-time.After(receiveLifecycleWait):
						t.Fatal("handler's closeBind decision was ignored after fallback")
					}
				} else {
					receiveLifecycleContinues(t, peer)
				}
			})
		}
	}
}

type receiveRound2ResponseRequest struct {
	pdu.PDU
	response pdu.PDU
}

func (p receiveRound2ResponseRequest) GetResponse() pdu.PDU { return p.response }

func TestLifecycleOnPDUDefaultResponseEncodeFailureUsesFallback(t *testing.T) {
	conn, peer := net.Pipe()
	invalid := pdu.NewDeliverSMResp().(*pdu.DeliverSMResp)
	invalid.MessageID = "invalid\x00message"
	request := pdu.NewDeliverSM()
	request.SetSequenceNumber(74)
	called := make(chan bool, 1)
	errorsReceived := make(chan error, 1)
	trx := newTransceivable(NewConnection(conn), Settings{
		OnPDU:         func(_ pdu.PDU, responded bool) { called <- responded },
		OnSubmitError: func(_ pdu.PDU, err error) { errorsReceived <- err },
	}, nil)
	trx.start()
	t.Cleanup(func() { _ = peer.Close(); _ = trx.Close() })
	completed := make(chan bool, 1)
	go func() {
		completed <- trx.in.handleOrClose(receiveRound2ResponseRequest{PDU: request, response: invalid})
	}()
	receiveLifecycleCheckHeader(t, receiveLifecycleRead(t, peer), data.DELIVER_SM_RESP, data.ESME_RSYSERR, 74)
	select {
	case err := <-errorsReceived:
		if !errors.Is(err, pdu.ErrEncode) {
			t.Fatalf("default response error = %v, want ErrEncode", err)
		}
	case <-time.After(receiveLifecycleWait):
		t.Fatal("default response encoding failure was not reported")
	}
	select {
	case responded := <-called:
		if !responded {
			t.Fatal("OnPDU did not observe the fallback response")
		}
	case <-time.After(receiveLifecycleWait):
		t.Fatal("OnPDU did not run after default response fallback")
	}
	select {
	case closeBind := <-completed:
		if closeBind {
			t.Fatal("default response encoding failure requested shutdown")
		}
	case <-time.After(receiveLifecycleWait):
		t.Fatal("default response processing did not finish")
	}
}

func TestLifecycleHandlerResponseFallbackWriteFailureClosesConnection(t *testing.T) {
	for _, window := range []bool{false, true} {
		name := "OnAllPDU"
		if window {
			name = "OnReceivedPduRequest"
		}
		t.Run(name, func(t *testing.T) {
			conn, peer := net.Pipe()
			invalid := pdu.NewDeliverSMResp().(*pdu.DeliverSMResp)
			invalid.MessageID = "invalid\x00message"
			type failure struct {
				p   pdu.PDU
				err error
			}
			reported := make(chan failure, 2)
			closed := make(chan State, 1)
			handler := func(pdu.PDU) (pdu.PDU, bool) { return invalid, false }
			settings := Settings{OnAllPDU: handler,
				OnSubmitError: func(p pdu.PDU, err error) {
					reported <- failure{p, err}
					if errors.Is(err, pdu.ErrEncode) {
						// The invalid response sent no bytes. Fail the following
						// fallback write after the encode callback has observed it.
						_ = peer.Close()
					}
				}, OnClosed: func(state State) { closed <- state },
			}
			var store RequestStore
			if window {
				store = NewDefaultStore()
				settings.WindowedRequestTracking = &WindowedRequestTracking{MaxWindowSize: 1, StoreAccessTimeOut: time.Second, OnReceivedPduRequest: handler}
			}
			trx := newTransceivable(NewConnection(conn), settings, store)
			trx.start()
			t.Cleanup(func() { _ = peer.Close(); _ = trx.Close() })
			request := pdu.NewDeliverSM()
			request.SetSequenceNumber(75)
			receiveLifecycleWrite(t, peer, receiveLifecycleMarshal(t, request))
			for index := 0; index < 2; index++ {
				select {
				case result := <-reported:
					if index == 0 {
						if result.p != invalid || !errors.Is(result.err, pdu.ErrEncode) {
							t.Fatalf("first error = %T, %v; want invalid response and ErrEncode", result.p, result.err)
						}
					} else if result.p.GetHeader().CommandID != data.DELIVER_SM_RESP || result.p.GetSequenceNumber() != 75 || errors.Is(result.err, pdu.ErrEncode) {
						t.Fatalf("fallback write error = %+v, %v; want fallback with request sequence and transport error", result.p.GetHeader(), result.err)
					}
				case <-time.After(receiveLifecycleWait):
					t.Fatal("response fallback write failure was not reported")
				}
			}
			select {
			case state := <-closed:
				if state != ConnectionIssue {
					t.Fatalf("fallback failure close state = %v, want ConnectionIssue", state)
				}
			case <-time.After(receiveLifecycleWait):
				t.Fatal("failed fallback left the connection open")
			}
		})
	}
}

type receiveRound2SnapshotStore struct {
	RequestStore
	captured chan struct{}
	release  chan struct{}
	gateOnce sync.Once
}

func (s *receiveRound2SnapshotStore) Get(ctx context.Context, sequence int32) (Request, bool) {
	request, found := s.RequestStore.Get(ctx, sequence)
	s.gateOnce.Do(func() { close(s.captured); <-s.release })
	return request, found
}

func TestLifecycleResponseSurvivesPendingWriteTransition(t *testing.T) {
	for _, change := range []string{"written", "replacement_id", "replacement_owner"} {
		t.Run(change, func(t *testing.T) {
			conn, peer := net.Pipe()
			store := &receiveRound2SnapshotStore{RequestStore: NewDefaultStore(), captured: make(chan struct{}), release: make(chan struct{})}
			completed := make(chan Response, 1)
			trx := newTransceivable(NewConnection(conn), Settings{WindowedRequestTracking: &WindowedRequestTracking{
				MaxWindowSize: 1, StoreAccessTimeOut: time.Second,
				OnExpectedPduResponse: func(response Response) { completed <- response },
			}}, store)
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(store.release) }) }
			t.Cleanup(func() { release(); _ = peer.Close(); _ = trx.Close() })
			p := pdu.NewSubmitSM()
			p.SetSequenceNumber(76)
			pending := trx.out.newRequest(p)
			if err := store.Set(context.Background(), pending); err != nil {
				t.Fatal(err)
			}
			// The request is on the wire; only its metadata is still pending.
			if err := trx.out.rememberRequest(pending); err != nil {
				t.Fatal(err)
			}
			if !trx.out.beginTransmission(pending) {
				t.Fatal("request did not begin transport")
			}
			done := make(chan struct{})
			go func() { defer close(done); trx.in.completeResponse(p.GetResponse(), 76, nil) }()
			receiveLifecycleSignal(t, store.captured, "response did not capture the pending reservation")
			written := pending
			written.WrittenAt = time.Now()
			switch change {
			case "written":
				marked, err := store.MarkWritten(context.Background(), pending, written.WrittenAt)
				if err != nil || !marked {
					t.Fatalf("mark successful write = %v, %v", marked, err)
				}
			case "replacement_id":
				if _, err := store.Delete(context.Background(), pending); err != nil {
					t.Fatal(err)
				}
				written.ID++
				if err := store.Set(context.Background(), written); err != nil {
					t.Fatal(err)
				}
			case "replacement_owner":
				if _, err := store.Delete(context.Background(), pending); err != nil {
					t.Fatal(err)
				}
				written.Owner++
				if err := store.Set(context.Background(), written); err != nil {
					t.Fatal(err)
				}
			}
			release()
			receiveLifecycleSignal(t, done, "response completion did not finish")
			current, exists := store.RequestStore.Get(context.Background(), 76)
			select {
			case response := <-completed:
				if change != "written" {
					t.Fatalf("response completed a replacement reservation after %s", change)
				}
				if exists || !response.OriginalRequest.TimeSent.Equal(pending.TimeSent) {
					t.Fatal("successful write transition lost the response or retained its reservation")
				}
			default:
				if change == "written" {
					t.Fatal("pending-to-written timestamp change discarded the matching response")
				}
				if !exists || current.ID != written.ID || current.Owner != written.Owner {
					t.Fatal("response removed a replacement reservation")
				}
			}
		})
	}
}
