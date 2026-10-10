package gosmpp

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gomaja/go-smpp/pdu"
)

var (
	ErrConnectionClosing = errors.New("connection is closing, can not send PDU to SMSC")
	ErrWindowsFull       = errors.New("window full")
	// ErrSubmitQueueFull rejects a callback submission that cannot safely wait
	// for queue or application-window capacity without blocking library workers.
	ErrSubmitQueueFull = errors.New("submission capacity unavailable in callback")
	// ErrRequestCleanupFull rejects tracked work while a session already retains
	// 1024 unresolved reservation identities. Recovery must settle some first.
	ErrRequestCleanupFull = errors.New("request cleanup backlog is full")
)

var connectionGeneration atomic.Uint64
var requestIdentity atomic.Uint64

type outboundPDU struct {
	pdu      pdu.PDU
	request  Request
	reserved bool
}

type transmittable struct {
	callbacks      callbackScope
	settings       Settings
	wg             sync.WaitGroup
	reclaimWG      sync.WaitGroup
	writerID       atomic.Uint64
	ownerOnce      sync.Once
	ownerState     *requestOwnerState
	reclaimMu      sync.Mutex
	reclaimRunning bool
	nextReclaim    time.Time
	input          chan outboundPDU
	queueSpace     chan struct{}
	conn           *Connection
	started        chan struct{}
	startOnce      sync.Once
	aliveState     int32
	pendingWrite   int32
	requestStore   RequestStore
	owner          uint64
	lineage        *requestLineage
	submitMu       sync.Mutex
	submitWG       sync.WaitGroup
	stopOnce       sync.Once
	stopped        chan struct{}
	done           chan struct{}
	closeErr       error
	requestClose   func(State)
	lastEnquiry    Request // Writer-owned; housekeeping retires the last application enquiry.
}

func newTransmittable(conn *Connection, settings Settings, requestStore RequestStore) *transmittable {
	owner := connectionGeneration.Add(1)
	return &transmittable{lineage: &requestLineage{id: owner}, settings: settings, conn: conn, input: make(chan outboundPDU, 1), queueSpace: make(chan struct{}, 1), started: make(chan struct{}), stopped: make(chan struct{}), done: make(chan struct{}), requestStore: requestStore, owner: owner}
}

func (t *transmittable) stop(state State) {
	t.stopOnce.Do(func() {
		t.submitMu.Lock()
		atomic.StoreInt32(&t.aliveState, Closed)
		close(t.stopped)
		t.submitMu.Unlock()
		// Interrupt pending transport operations before waiting for either worker.
		if state != StoppingProcessOnly {
			if state == ExplicitClosing {
				t.conn.tryUnbind()
			}
			t.closeErr = t.conn.Close()
		} else {
			_ = t.conn.SetWriteDeadline(time.Now())
		}
		go func() {
			defer close(t.done)
			defer func() {
				if t.callbacks.parent == nil && !t.lineage.managed {
					t.lineage.markLineageClosed()
				}
			}()
			defer registerCallbackWorker(&t.callbacks)()
			t.awaitStarted()
			t.submitWG.Wait()
			close(t.input)
			t.wg.Wait()
			t.reclaimWG.Wait()
			t.drain()
			t.flushRequests()
			if t.settings.OnClosed != nil {
				invokeCallback(&t.callbacks, func() { t.settings.OnClosed(state) })
			}
		}()
	})
}

func (t *transmittable) close(state State) error {
	t.stop(state)
	if !joinCallbackScope(&t.callbacks, t.done) {
		return nil
	}
	return t.closeErr
}

func (t *transmittable) closing(state State) {
	if t.requestClose != nil {
		t.requestClose(state)
	} else {
		t.stop(state)
	}
}

func (t *transmittable) Submit(p pdu.PDU) error { return t.SubmitContext(context.Background(), p) }
func (t *transmittable) SubmitContext(ctx context.Context, p pdu.PDU) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	t.submitMu.Lock()
	if atomic.LoadInt32(&t.aliveState) != Alive {
		t.submitMu.Unlock()
		return ErrConnectionClosing
	}
	// A nil handler response must never wait for outbound capacity.
	if p == nil {
		t.submitMu.Unlock()
		return nil
	}
	t.submitWG.Add(1)
	atomic.AddInt32(&t.pendingWrite, 1)
	t.submitMu.Unlock()
	defer t.submitWG.Done()
	defer atomic.AddInt32(&t.pendingWrite, -1)
	queued := outboundPDU{pdu: p}
	var callerID uint64
	if t.tracks(p) {
		if t.lineage.orphaned.Load() {
			t.reclaimDeadRequests()
		}
		// SMPP v3.4 §§3.2.1, 5.1.4: reserve unique correlation before accepting
		// the PDU for transport; a duplicate must not reach the socket.
		queued.request = t.newRequest(p)
		if err = t.rememberRequest(queued.request); err != nil {
			return err
		}
		storeCtx, cancel := context.WithTimeout(ctx, t.settings.StoreAccessTimeOut)
		err = t.requestStore.Set(storeCtx, queued.request)
		cancel()
		if err != nil {
			// A remote Set may have committed before its acknowledgement failed.
			abandonRequest(queued.request)
			_, cleanupErr := t.deleteRequest(queued.request)
			if cleanupErr != nil {
				t.lineage.orphaned.Store(true)
				return errors.Join(err, cleanupErr)
			}
			return err
		}
		queued.reserved = true
		defer func() {
			if err != nil {
				releaseRequestPermit(queued.request)
				// A failed admission never became an accepted outbound PDU. If
				// cleanup must be retried, retain that distinction across rebinds.
				_, deleteErr := t.deleteRequest(queued.request)
				if deleteErr != nil {
					abandonRequest(queued.request)
					t.lineage.orphaned.Store(true)
					err = errors.Join(err, deleteErr)
				}
			}
		}()
		if _, enquiry := p.(*pdu.EnquireLink); !enquiry {
			var pending bool
			pending, err = t.acquireRequestPermit(ctx, queued.request, &callerID)
			if err != nil || !pending {
				return err
			}
		}
	}
	if admitted, enqueueErr := t.tryEnqueue(ctx, queued); admitted || enqueueErr != nil {
		return enqueueErr
	}
	// Only the full-queue slow path needs goroutine identity. Ordinary producers
	// retain backpressure; the writer cannot wait for itself to drain the queue.
	release, allowed := t.beginSubmitWait(&callerID, false)
	if !allowed {
		return ErrSubmitQueueFull
	}
	defer release()
	for {
		select {
		case <-t.stopped:
			return ErrConnectionClosing
		case <-ctx.Done():
			return ctx.Err()
		case <-t.queueSpace:
		}
		if admitted, enqueueErr := t.tryEnqueue(ctx, queued); admitted || enqueueErr != nil {
			if !admitted {
				// A canceled waiter consumed the capacity notification without
				// filling the slot. Pass it on so another producer can proceed.
				t.signalQueueSpace()
			}
			return enqueueErr
		}
	}
}

// Admission and stop use the same lock. A select containing both a queue send
// and a closed stop channel can choose the send after the writer drains, so
// capacity notifications must retry this nonblocking operation instead.
func (t *transmittable) tryEnqueue(ctx context.Context, queued outboundPDU) (bool, error) {
	t.submitMu.Lock()
	defer t.submitMu.Unlock()
	if atomic.LoadInt32(&t.aliveState) != Alive {
		return false, ErrConnectionClosing
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	select {
	case t.input <- queued:
		return true, nil
	default:
		return false, nil
	}
}

func (t *transmittable) signalQueueSpace() {
	select {
	case t.queueSpace <- struct{}{}:
	default:
	}
}

func (t *transmittable) start() {
	t.startOnce.Do(func() {
		defer close(t.started)
		if atomic.LoadInt32(&t.aliveState) != Alive {
			return
		}
		t.trackRequestOwner()
		t.wg.Add(1)
		go func() {
			defer t.wg.Done()
			id := callbackGoroutineID()
			defer registerCallbackWorkerID(&t.callbacks, id)()
			t.writerID.Store(id)
			defer t.writerID.Store(0)
			t.loop()
		}()
	})
}
func (t *transmittable) awaitStarted() { t.start(); <-t.started }

func (t *transmittable) submitError(p pdu.PDU, err error) {
	if p != nil && t.settings.OnSubmitError != nil {
		invokeCallback(&t.callbacks, func() { t.settings.OnSubmitError(p, err) })
	}
}
func (t *transmittable) drain() {
	for queued := range t.input {
		t.signalQueueSpace()
		t.discardQueued(queued)
	}
}
func (t *transmittable) discardQueued(queued outboundPDU) {
	if queued.reserved {
		releaseRequestPermit(queued.request)
		removed, err := t.deleteRequest(queued.request)
		if err != nil {
			// Drain owns this unsent completion even if store cleanup needs
			// retrying later. Flush must not issue a second close callback.
			abandonRequest(queued.request)
			t.lineage.orphaned.Store(true)
			t.reportStoreError(err)
		} else if !removed {
			return
		}
	}
	t.submitError(queued.pdu, ErrConnectionClosing)
}

func (t *transmittable) loop() {
	var ticker *time.Ticker
	var tick <-chan time.Time
	if t.settings.EnquireLink > 0 {
		ticker = time.NewTicker(t.settings.EnquireLink)
		tick = ticker.C
		defer ticker.Stop()
	}
	var cleanupTick <-chan time.Time
	if t.settings.WindowedRequestTracking != nil {
		interval := t.settings.ExpireCheckTimer
		if interval <= 0 {
			interval = time.Second
		}
		cleanupTicker := time.NewTicker(interval)
		defer cleanupTicker.Stop()
		cleanupTick = cleanupTicker.C
	}
	defer t.drain()
	for {
		var queued outboundPDU
		var housekeeping bool
		select {
		case <-t.stopped:
			return
		case <-cleanupTick:
			if t.lineage.orphaned.Load() || t.pendingStoreCleanup() {
				t.reclaimDeadRequests()
			}
			continue
		case <-tick:
			queued.pdu = pdu.NewEnquireLink()
			housekeeping = true
		case next, ok := <-t.input:
			if !ok {
				return
			}
			t.signalQueueSpace()
			queued = next
		}
		p := queued.pdu
		if p == nil {
			continue
		}
		if atomic.LoadInt32(&t.aliveState) != Alive {
			if !housekeeping {
				t.discardQueued(queued)
			}
			return
		}
		var n int
		var err error
		if housekeeping {
			// Keepalive transport never depends on backend availability. It has no
			// application reservation; retire any previous tracked enquiry first.
			t.retireEnquiry()
			n, err = t.writeTransport(p)
		} else {
			n, err = t.writeQueued(queued)
		}
		if housekeeping && atomic.LoadInt32(&t.aliveState) != Alive {
			return
		}
		if t.check(p, n, err) {
			return
		}
	}
}

func (t *transmittable) check(p pdu.PDU, n int, err error) (closing bool) {
	if err == nil {
		return false
	}
	if atomic.LoadInt32(&t.aliveState) != Alive {
		err = errors.Join(ErrConnectionClosing, err)
	}
	// Encoding and a full window reject one PDU without damaging the stream.
	closing = n > 0 || (!errors.Is(err, ErrWindowsFull) && !errors.Is(err, ErrSequenceInUse) && !errors.Is(err, ErrRequestCleanupFull) && !errors.Is(err, pdu.ErrEncode))
	var completed *completedRequestError
	if !errors.As(err, &completed) {
		t.submitError(p, err)
	}
	if closing {
		t.closing(ConnectionIssue)
	}
	return closing
}

func (t *transmittable) owns(request Request) bool {
	return request.Owner == 0 || request.Owner == t.owner
}
func (t *transmittable) flushRequests() {
	defer t.retireRequestOwner()
	if t.settings.WindowedRequestTracking == nil {
		return
	}
	// Local bookkeeping is outside the backend budget. Close completes every
	// accepted request even if List is truncated or deletion cannot finish.
	requests := t.localRequests()
	uncertain := uncertainSnapshot(t.lineage)
	known := make(map[uint64]bool, len(requests))
	for _, request := range requests {
		known[request.ID] = true
	}
	ctx, cancel := context.WithTimeout(context.Background(), t.settings.StoreAccessTimeOut)
	var completed []Request
	var failures []error
	opCtx, opCancel := context.WithTimeout(ctx, t.settings.StoreAccessTimeOut)
	for _, request := range t.requestStore.List(opCtx) {
		// Owned requests come from the ledger. Keep supporting caller-installed
		// legacy entries, which have no local owner identity.
		if request.Owner == 0 || (request.Owner == t.owner && !known[request.ID] && t.lineage.discarded(request)) {
			requests = append(requests, request)
		}
	}
	if err := opCtx.Err(); err != nil {
		failures = append(failures, err)
	}
	opCancel()
	results, _ := t.cleanupRequests(ctx, requests, func(ctx context.Context, request Request) cleanupResult {
		removed, err := t.deleteRequestContext(ctx, request)
		// Completion owns the local claim before checking abandonment. A response
		// whose lookup failed while Delete ran must never be offered for retry.
		return cleanupResult{request: request, err: err, completed: removed && !t.lineage.discarded(request)}
	})
	for _, result := range results {
		if result.err != nil {
			failures = append(failures, result.err)
		}
		if result.completed {
			completed = append(completed, result.request)
		}
	}
	gets := 0
	// Final close is time-bounded, with no background-scan lookup-count cap.
	failures = append(failures, t.resolveUncertainSnapshot(ctx, t.lineage, uncertain, &gets, 0)...)
	if ctx.Err() != nil && len(failures) == 0 {
		failures = append(failures, ctx.Err())
	}
	cancel()
	for _, request := range requests {
		if claimCloseRequest(request) {
			completed = append(completed, request)
		}
	}
	for _, err := range failures {
		t.reportStoreError(err)
	}
	if t.settings.OnClosePduRequest != nil {
		for _, request := range completed {
			invokeCallback(&t.callbacks, func() { t.settings.OnClosePduRequest(request.PDU) })
		}
	}
}

func (t *transmittable) tracks(p pdu.PDU) bool {
	return t.settings.WindowedRequestTracking != nil && t.settings.MaxWindowSize != 0 && isAllowPDU(p)
}

func (t *transmittable) newRequest(p pdu.PDU) Request {
	return Request{PDU: p, TimeSent: time.Now(), Owner: t.owner, ID: requestIdentity.Add(1), Lineage: t.lineage.id}
}

func (t *transmittable) write(p pdu.PDU) (int, error) {
	return t.writeQueued(outboundPDU{pdu: p})
}

func (t *transmittable) writeQueued(queued outboundPDU) (n int, err error) {
	p := queued.pdu
	if !t.tracks(p) {
		return t.writeTransport(p)
	}
	request, reserved := queued.request, queued.reserved
	written := false
	defer func() {
		if err == nil || !reserved || written {
			return
		}
		releaseRequestPermit(request)
		removed, deleteErr := t.deleteRequest(request)
		if deleteErr != nil {
			// Failure to clean up is not evidence that another completion won.
			// Report this unsent PDU once, then retry cleanup without a callback.
			abandonRequest(request)
			t.lineage.orphaned.Store(true)
			t.reportStoreError(deleteErr)
			err = errors.Join(err, deleteErr)
		} else {
			if !removed {
				err = &completedRequestError{err}
			}
		}
	}()
	if atomic.LoadInt32(&t.aliveState) != Alive {
		return 0, ErrConnectionClosing
	}
	// Length and reservation share a pre-write budget. A fresh budget starts
	// after transport I/O; neither the write nor callbacks consume it.
	ctx, cancel := context.WithTimeout(context.Background(), t.settings.StoreAccessTimeOut)
	defer cancel()
	_, enquiry := p.(*pdu.EnquireLink)
	if t.lineage.orphaned.Load() {
		t.reclaimDeadRequests()
	}
	if !enquiry {
		length, countErr := t.requestStore.WrittenLength(ctx, t.owner)
		if countErr != nil {
			return 0, countErr
		}
		if length+t.writtenCount() >= int(t.settings.MaxWindowSize) {
			t.reclaimDeadRequests()
			return 0, ErrWindowsFull
		}
	}
	if !reserved {
		request = t.newRequest(p)
		if err = t.rememberRequest(request); err != nil {
			return 0, err
		}
		reserved = true
		err = t.requestStore.Set(ctx, request)
		if err != nil {
			abandonRequest(request)
			return 0, err
		}
	}
	cancel()
	if enquiry {
		t.retireEnquiry()
	}
	// SMPP v3.4 §§3.2.1, 5.1.4: correlation is valid once this request
	// begins transport, never merely because a producer reserved its sequence.
	if !t.beginTransmission(request) {
		return 0, &completedRequestError{ErrConnectionClosing}
	}
	n, err = t.writeTransport(p)
	if err != nil {
		return n, err
	}
	written = true
	if enquiry {
		t.lastEnquiry = request
	}
	writtenAt := time.Now()
	t.rememberWritten(request, writtenAt)
	ctx, cancel = context.WithTimeout(context.Background(), t.settings.StoreAccessTimeOut)
	_, err = t.requestStore.MarkWritten(ctx, request, writtenAt)
	cancel()
	if err != nil {
		// Bytes reached the peer. Report store failure without claiming a failed send.
		t.rememberMetadataFailure(request)
		t.lineage.orphaned.Store(true)
		t.reportStoreError(err)
		t.reclaimDeadRequests()
		return n, nil
	}
	return n, nil
}

func (t *transmittable) retireEnquiry() {
	request := t.lastEnquiry
	t.lastEnquiry = Request{}
	if request.PDU == nil {
		return
	}
	_, err := t.deleteRequest(request)
	if err != nil {
		abandonRequest(request)
		t.reportStoreError(err)
	}
}

func isAllowPDU(p pdu.PDU) bool {
	if p.CanResponse() {
		switch p.(type) {
		case *pdu.BindRequest, *pdu.Unbind:
			return false
		}
		return true
	}
	return false
}

func (t *transmittable) writeTransport(p pdu.PDU) (int, error) {
	n, err := t.conn.writePDUWithTimeout(p, t.settings.WriteTimeout)
	if err != nil && atomic.LoadInt32(&t.aliveState) != Alive {
		err = errors.Join(ErrConnectionClosing, err)
	}
	return n, err
}
