package gosmpp

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gomaja/go-smpp/data"
	"github.com/gomaja/go-smpp/pdu"
)

type receivable struct {
	callbacks        callbackScope
	ctx              context.Context
	cancel           context.CancelFunc
	wg               sync.WaitGroup
	settings         Settings
	conn             *Connection
	started          chan struct{}
	startOnce        sync.Once
	aliveState       int32
	requestStore     RequestStore
	owner            uint64
	stopOnce         sync.Once
	done             chan struct{}
	closeErr         error
	requestClose     func(State)
	discardedRequest func(Request) bool
}

func newReceivable(conn *Connection, settings Settings, store RequestStore) *receivable {
	r := &receivable{settings: settings, conn: conn, started: make(chan struct{}), done: make(chan struct{}), requestStore: store}
	r.ctx, r.cancel = context.WithCancel(context.Background())
	return r
}

func (t *receivable) stop(state State) {
	t.stopOnce.Do(func() {
		atomic.StoreInt32(&t.aliveState, Closed)
		t.cancel()
		if state != StoppingProcessOnly {
			t.closeErr = t.conn.Close()
		} else {
			_ = t.conn.SetReadDeadline(time.Now())
		}
		go func() {
			defer close(t.done)
			defer registerCallbackWorker(&t.callbacks)()
			t.awaitStarted()
			t.wg.Wait()
			if t.settings.OnClosed != nil {
				invokeCallback(&t.callbacks, func() { t.settings.OnClosed(state) })
			}
		}()
	})
}
func (t *receivable) close(state State) error {
	t.stop(state)
	if !joinCallbackScope(&t.callbacks, t.done) {
		return nil
	}
	return t.closeErr
}
func (t *receivable) closing(state State) {
	if t.requestClose != nil {
		t.requestClose(state)
	} else {
		t.stop(state)
	}
}
func (t *receivable) start() {
	t.startOnce.Do(func() {
		defer close(t.started)
		if atomic.LoadInt32(&t.aliveState) != Alive {
			return
		}
		t.wg.Add(1)
		go func() {
			defer t.wg.Done()
			defer registerCallbackWorker(&t.callbacks)()
			t.loop()
		}()
	})
}
func (t *receivable) awaitStarted() { t.start(); <-t.started }
func (t *receivable) receivingError(err error) {
	if t.settings.OnReceivingError != nil {
		invokeCallback(&t.callbacks, func() { t.settings.OnReceivingError(err) })
	}
}

// frameReader retains the header even when Parse returns a framing error.
// It observes bytes actually read; it never reads ahead of the parser.
type frameReader struct {
	io.Reader
	header [16]byte
	n      int
}

func (r *frameReader) Read(b []byte) (int, error) {
	n, err := r.Reader.Read(b)
	if r.n < len(r.header) {
		r.n += copy(r.header[r.n:], b[:n])
	}
	return n, err
}

func (t *receivable) loop() {
	for atomic.LoadInt32(&t.aliveState) == Alive {
		var err error
		if t.settings.ReadTimeout > 0 {
			err = t.conn.SetReadTimeout(t.settings.ReadTimeout)
		} else {
			err = t.conn.SetReadDeadline(time.Time{})
		}
		frame := &frameReader{Reader: t.conn}
		var p pdu.PDU
		if err == nil {
			p, err = pdu.Parse(frame)
		}
		if err != nil {
			if atomic.LoadInt32(&t.aliveState) != Alive {
				return
			}
			t.receivingError(err)
			if atomic.LoadInt32(&t.aliveState) != Alive {
				return
			}
			var decoded *pdu.DecodeError
			if errors.As(err, &decoded) {
				// SMPP v3.4 §§2.8, 3.3, 4.3 and v5 §2.8.2: intact framing
				// allows rejection of this PDU without losing the next transaction.
				if uint32(decoded.Header.CommandID)&0x80000000 != 0 {
					t.completeResponse(decodedResponsePDU(decoded), decoded.Header.SequenceNumber, err)
				} else {
					response := decodeErrorResponse(decoded)
					if !t.writeResponse(response) {
						return
					}
				}
				continue
			}
			if errors.Is(err, pdu.ErrInvalidCommandLength) || (errors.Is(err, pdu.ErrIncompletePDU) && frame.n > 0) {
				// SMPP v3.4 §4.3: nack corrupt framing before disconnecting.
				nack := pdu.NewGenericNack()
				nack.SetCommandStatus(data.ESME_RINVCMDLEN)
				var sequence int32
				if frame.n == len(frame.header) {
					sequence = pdu.ParseHeader(frame.header).SequenceNumber
				}
				nack.SetSequenceNumber(sequence)
				_ = t.writeResponse(nack)
			}
			t.closing(InvalidStreaming)
			return
		}
		if atomic.LoadInt32(&t.aliveState) != Alive {
			return
		}
		var closing bool
		if t.settings.WindowedRequestTracking != nil {
			closing = t.handleWindowPdu(p)
		} else if t.settings.OnAllPDU != nil {
			closing = t.handleAllPdu(p)
		} else {
			closing = t.handleOrClose(p)
		}
		if closing {
			t.closing(UnbindClosing)
			return
		}
	}
}

func decodeErrorResponse(err *pdu.DecodeError) pdu.PDU {
	response := pdu.NewGenericNack()
	if err.Status != data.ESME_RINVCMDLEN && err.Status != data.ESME_RINVCMDID {
		if request, createErr := pdu.CreatePDUFromCmdID(err.Header.CommandID); createErr == nil {
			if !request.CanResponse() {
				// SMPP v3.4 §§2.8, 4.12: alert_notification has no response.
				// Section 4.3 reserves generic_nack for header errors.
				return nil
			}
			response = request.GetResponse()
		}
	}
	response.SetSequenceNumber(err.Header.SequenceNumber)
	response.SetCommandStatus(err.Status)
	// SMPP v3.4 §2.8; v5 §3.2.1.3: failed responses omit the body.
	return headerOnlyResponse{response}
}

func decodedResponsePDU(err *pdu.DecodeError) pdu.PDU {
	response, createErr := pdu.CreatePDUFromCmdID(err.Header.CommandID)
	if createErr != nil {
		response = pdu.NewGenericNack()
	}
	// Preserve the received header rather than replacing its status with the
	// suggested rejection status. Err carries the body failure separately.
	header := err.Header
	header.CommandLength = data.PDU_HEADER_SIZE
	buffer := pdu.NewBuffer(nil)
	header.Marshal(buffer)
	_ = response.Unmarshal(buffer)
	return response
}

func (t *receivable) writeResponse(p pdu.PDU) bool {
	if p == nil {
		return true
	}
	timeout := t.settings.WriteTimeout
	if timeout <= 0 {
		timeout = DefaultBindTimeout
	}
	if _, err := t.conn.writePDUWithTimeout(p, timeout); err != nil {
		if t.settings.OnSubmitError != nil {
			invokeCallback(&t.callbacks, func() { t.settings.OnSubmitError(p, err) })
		}
		if !errors.Is(err, pdu.ErrEncode) && atomic.LoadInt32(&t.aliveState) == Alive {
			t.closing(ConnectionIssue)
		}
		return false
	}
	return true
}

func (t *receivable) respond(request, p pdu.PDU, closing bool) bool {
	if p == nil {
		return true
	}
	buffer := pdu.NewBuffer(nil)
	p.Marshal(buffer)
	if err := buffer.Err(); err != nil {
		encodeErr := &pdu.EncodeError{PDU: p, Err: err}
		if t.settings.OnSubmitError != nil {
			invokeCallback(&t.callbacks, func() { t.settings.OnSubmitError(p, encodeErr) })
		}
		// SMPP v3.4 §§2.8, 5.1.3 and v5 §3.2.1.3: an application
		// response that cannot be encoded still needs an error reply. Preserve
		// the request sequence and the callback's close-after-response choice.
		if request != nil && request.CanResponse() {
			fallback := request.GetResponse()
			fallback.SetCommandStatus(data.ESME_RSYSERR)
			fallback.SetSequenceNumber(request.GetSequenceNumber())
			return t.writeResponse(headerOnlyResponse{fallback})
		}
		return true
	}
	if closing && t.conn != nil {
		return t.writeResponse(p)
	}
	if t.settings.response != nil {
		t.settings.response(p)
	}
	return true
}

func (t *receivable) completeResponse(p pdu.PDU, sequence int32, decodeErr error) {
	if t.settings.WindowedRequestTracking == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), t.settings.StoreAccessTimeOut)
	request, ok := t.requestStore.Get(ctx, sequence)
	getErr := ctx.Err()
	cancel()
	if !ok && getErr != nil {
		if local, found := localRequest(t.owner, sequence); found {
			abandonRequest(local)
		}
		t.receivingError(getErr)
		return
	}
	if !ok {
		if local, found := localRequest(t.owner, sequence); found {
			if forgetRequest(local) && t.settings.OnExpectedPduResponse != nil {
				invokeCallback(&t.callbacks, func() { t.settings.OnExpectedPduResponse(Response{PDU: p, OriginalRequest: local, Err: decodeErr}) })
			}
			return
		}
	}
	if ok && (request.Owner == 0 || request.Owner == t.owner) {
		if !requestTransmitting(request) {
			if p != nil && t.settings.OnUnexpectedPduResponse != nil {
				invokeCallback(&t.callbacks, func() { t.settings.OnUnexpectedPduResponse(p) })
			}
			return
		}
		ctx, cancel = context.WithTimeout(context.Background(), t.settings.StoreAccessTimeOut)
		removed, err := t.requestStore.Delete(ctx, request)
		cancel()

		if err != nil {
			abandonRequest(request)
			t.receivingError(err)
			return
		}
		if !removed || !forgetRequest(request) {
			return
		}
		if t.discardedRequest != nil && t.discardedRequest(request) {
			return
		}
		if t.settings.OnExpectedPduResponse != nil {
			invokeCallback(&t.callbacks, func() { t.settings.OnExpectedPduResponse(Response{PDU: p, OriginalRequest: request, Err: decodeErr}) })
		}
	} else if p != nil && t.settings.OnUnexpectedPduResponse != nil {
		invokeCallback(&t.callbacks, func() { t.settings.OnUnexpectedPduResponse(p) })
	}
}

func (t *receivable) handleWindowPdu(p pdu.PDU) (closing bool) {
	if t.settings.WindowedRequestTracking == nil || p == nil {
		return false
	}
	// SMPP v3.4 §§4.3.1, 5.1.4: generic_nack is a correlated response.
	if uint32(p.GetHeader().CommandID)&0x80000000 != 0 {
		t.completeResponse(p, p.GetSequenceNumber(), nil)
		return false
	}
	if t.settings.OnReceivedPduRequest == nil {
		// SMPP v3.4 §2.8: enabling outbound response correlation must not
		// suppress request handling or the request's associated response.
		if t.settings.OnAllPDU != nil {
			return t.handleAllPdu(p)
		}
		return t.handleOrClose(p)
	}
	switch p.(type) {
	case *pdu.EnquireLink, *pdu.Unbind:
		if t.settings.EnableAutoRespond {
			_, closing = p.(*pdu.Unbind)
			closing = t.respond(p, p.GetResponse(), closing) && closing
			return closing
		}
	}
	if t.settings.OnReceivedPduRequest != nil {
		var response pdu.PDU
		invokeCallback(&t.callbacks, func() { response, closing = t.settings.OnReceivedPduRequest(p) })
		closing = t.respond(p, response, closing) && closing
	}
	return closing
}

func (t *receivable) handleAllPdu(p pdu.PDU) (closing bool) {
	if t.settings.OnAllPDU != nil && p != nil {
		var response pdu.PDU
		invokeCallback(&t.callbacks, func() { response, closing = t.settings.OnAllPDU(p) })
		closing = t.respond(p, response, closing) && closing
	}
	return closing
}

func (t *receivable) handleOrClose(p pdu.PDU) (closing bool) {
	if p == nil {
		return false
	}
	switch p.(type) {
	case *pdu.EnquireLink:
		t.respond(p, p.GetResponse(), false)
		t.notifyPDU(p, true)
	case *pdu.Unbind:
		t.respond(p, p.GetResponse(), true)
		t.notifyPDU(p, true)
		closing = true
	default:
		responded := p.CanResponse()
		if responded {
			t.respond(p, p.GetResponse(), false)
		}
		t.notifyPDU(p, responded)
	}
	return closing
}
func (t *receivable) notifyPDU(p pdu.PDU, responded bool) {
	if t.settings.OnPDU != nil {
		invokeCallback(&t.callbacks, func() { t.settings.OnPDU(p, responded) })
	}
}
