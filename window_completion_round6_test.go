package gosmpp

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gomaja/go-smpp/pdu"
)

type completionRound6Store struct {
	DefaultStore
	gateID        atomic.Uint64
	gateGetID     atomic.Uint64
	gateList      atomic.Bool
	listEntered   chan struct{}
	listRelease   chan struct{}
	getEntered    chan struct{}
	getRelease    chan struct{}
	deleteEntered chan struct{}
	deleteRelease chan struct{}
	deadlineGet   atomic.Bool
	failGet       atomic.Bool
	failDelete    atomic.Bool
}

func (s *completionRound6Store) Get(ctx context.Context, sequence int32) (Request, bool) {
	if s.failGet.CompareAndSwap(true, false) {
		<-ctx.Done()
		return Request{}, false
	}
	request, ok := s.DefaultStore.Get(ctx, sequence)
	if ok && request.ID != 0 && s.gateGetID.CompareAndSwap(request.ID, 0) {
		close(s.getEntered)
		<-s.getRelease
	}
	if ok && s.deadlineGet.CompareAndSwap(true, false) {
		<-ctx.Done()
	}
	return request, ok
}

func (s *completionRound6Store) Delete(ctx context.Context, request Request) (bool, error) {
	if s.failDelete.Load() {
		return false, errWindowResponseDelete
	}
	removed, err := s.DefaultStore.Delete(ctx, request)
	if request.ID != 0 && s.gateID.CompareAndSwap(request.ID, 0) {
		close(s.deleteEntered)
		<-s.deleteRelease
	}
	return removed, err
}

func (s *completionRound6Store) List(ctx context.Context) []Request {
	rows := s.DefaultStore.List(ctx)
	if s.gateList.CompareAndSwap(true, false) {
		close(s.listEntered)
		<-s.listRelease
	}
	// Make written rows immediately eligible without a timer or changing live
	// metadata. Only explicit expiry scans run in these tests.
	for i := range rows {
		if !rows[i].WrittenAt.IsZero() {
			rows[i].WrittenAt = time.Now().Add(-time.Hour)
		}
	}
	return rows
}

type completionRound6Harness struct {
	session         *Session
	peer            net.Conn
	store           *completionRound6Store
	request         pdu.PDU
	frame           pdu.PDU
	reservation     Request
	expected        chan Response
	expired         chan pdu.PDU
	closed          chan pdu.PDU
	receivingErrors chan error
	unexpected      chan pdu.PDU
	barrier         chan struct{}
	releaseOnce     sync.Once
}

func newCompletionRound6Harness(t *testing.T) *completionRound6Harness {
	t.Helper()
	conn, peer, frames := windowResponsePeer(t)
	h := &completionRound6Harness{peer: peer,
		store:    &completionRound6Store{DefaultStore: NewDefaultStore(), deleteEntered: make(chan struct{}), deleteRelease: make(chan struct{}), getEntered: make(chan struct{}), getRelease: make(chan struct{}), listEntered: make(chan struct{}), listRelease: make(chan struct{})},
		expected: make(chan Response, 8), expired: make(chan pdu.PDU, 8), closed: make(chan pdu.PDU, 8),
		receivingErrors: make(chan error, 8), unexpected: make(chan pdu.PDU, 8), barrier: make(chan struct{}, 8),
	}
	settings := lifecycleSettings()
	settings.MaxWindowSize = 1
	settings.StoreAccessTimeOut = 50 * time.Millisecond
	settings.PduExpireTimeOut = time.Second
	settings.ExpireCheckTimer = time.Hour
	settings.OnExpectedPduResponse = func(r Response) { h.expected <- r }
	settings.OnExpiredPduRequest = func(p pdu.PDU) bool { h.expired <- p; return false }
	settings.OnClosePduRequest = func(p pdu.PDU) { h.closed <- p }
	settings.OnReceivingError = func(err error) { h.receivingErrors <- err }
	settings.OnUnexpectedPduResponse = func(p pdu.PDU) {
		if p.GetSequenceNumber() == 0 {
			h.barrier <- struct{}{}
			return
		}
		h.unexpected <- p
	}
	var err error
	h.session, err = NewSession(lifecycleConnector{connect: func() (*Connection, error) { return conn, nil }}, settings, 0, WithRequestStore(h.store))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.releaseDelete(); h.store.failDelete.Store(false); _ = h.session.Close() })
	h.request = pdu.NewSubmitSM()
	if err := h.session.Transceiver().Submit(h.request); err != nil {
		t.Fatal(err)
	}
	h.frame = windowResponseFrame(t, frames)
	windowResponseAwait(t, func() bool {
		row, ok := h.store.DefaultStore.Get(context.Background(), h.request.GetSequenceNumber())
		return ok && !row.WrittenAt.IsZero() && h.session.bound().out.writtenCount() == 1
	})
	h.reservation, _ = h.store.DefaultStore.Get(context.Background(), h.request.GetSequenceNumber())
	out := h.session.bound().out
	out.reclaimMu.Lock()
	out.nextReclaim = time.Now().Add(time.Hour)
	out.reclaimMu.Unlock()
	return h
}

func (h *completionRound6Harness) releaseDelete() {
	h.releaseOnce.Do(func() { close(h.store.deleteRelease) })
}

func (h *completionRound6Harness) response(t *testing.T) {
	t.Helper()
	if err := h.peer.SetWriteDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	writeWire(t, h.peer, h.frame.GetResponse())
}

func (h *completionRound6Harness) receiveBarrier(t *testing.T) {
	t.Helper()
	sentinel := pdu.NewEnquireLink().GetResponse()
	sentinel.SetSequenceNumber(0)
	if err := h.peer.SetWriteDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	writeWire(t, h.peer, sentinel)
	lifecycleWait(t, h.barrier)
}

func (h *completionRound6Harness) assertCompletions(t *testing.T, expected, expired int) {
	t.Helper()
	if gotExpected, gotExpired, gotClosed := len(h.expected), len(h.expired), len(h.closed); gotExpected != expected || gotExpired != expired || gotClosed != 0 {
		t.Fatalf("completion counts: expected=%d expiry=%d close=%d; want expected=%d expiry=%d close=0", gotExpected, gotExpired, gotClosed, expected, expired)
	}
	if expected == 1 {
		response := <-h.expected
		h.expected <- response
		if response.OriginalRequest.ID != h.reservation.ID || response.OriginalRequest.Owner != h.reservation.Owner || response.OriginalRequest.PDU != h.request || response.PDU == nil || response.GetSequenceNumber() != h.request.GetSequenceNumber() || response.Err != nil {
			t.Fatalf("response lost original reservation or response header: %+v", response)
		}
	}
	if written, permits := h.session.bound().out.writtenCount(), len(h.session.bound().out.ownerState.permits); written != 0 || permits != 0 {
		t.Fatalf("completion retained capacity: written=%d permits=%d", written, permits)
	}
}

func TestWindowCompletionRound6ResponseWinsPendingExpiryDelete(t *testing.T) {
	h := newCompletionRound6Harness(t)
	h.store.gateID.Store(h.reservation.ID)
	expiryDone := make(chan struct{})
	go func() { defer close(expiryDone); h.session.bound().expireRequests() }()
	t.Cleanup(func() { h.releaseDelete(); lifecycleWait(t, expiryDone) })
	lifecycleWait(t, h.store.deleteEntered)
	// Expiry has removed the backend row but has not returned to the local
	// completion arbiter. The arriving response must still settle the request.
	h.response(t)
	h.receiveBarrier(t)
	h.releaseDelete()
	lifecycleWait(t, expiryDone)
	h.assertCompletions(t, 1, 0)
	h.session.bound().expireRequests()
	_ = h.session.Close()
	h.assertCompletions(t, 1, 0)
}

func TestWindowCompletionRound6ExpiryWinsBeforeResponse(t *testing.T) {
	h := newCompletionRound6Harness(t)
	h.session.bound().expireRequests()
	h.response(t)
	h.receiveBarrier(t)
	h.assertCompletions(t, 0, 1)
	if len(h.unexpected) != 1 {
		t.Fatalf("late response callbacks=%d, want 1", len(h.unexpected))
	}
	_ = h.session.Close()
	h.assertCompletions(t, 0, 1)
}

func TestWindowCompletionRound6ResponseWinsBeforeExpiry(t *testing.T) {
	h := newCompletionRound6Harness(t)
	h.store.gateID.Store(h.reservation.ID)
	h.response(t)
	lifecycleWait(t, h.store.deleteEntered)
	h.session.bound().expireRequests()
	h.releaseDelete()
	h.receiveBarrier(t)
	h.assertCompletions(t, 1, 0)
	_ = h.session.Close()
	h.assertCompletions(t, 1, 0)
}

func TestWindowCompletionRound6GetSuccessAtDeadline(t *testing.T) {
	h := newCompletionRound6Harness(t)
	h.store.deadlineGet.Store(true)
	h.response(t)
	h.receiveBarrier(t)
	h.session.bound().expireRequests()
	h.assertCompletions(t, 1, 0)
	if len(h.receivingErrors) != 0 {
		t.Fatalf("successful Get produced %d receiving errors", len(h.receivingErrors))
	}
	_ = h.session.Close()
	h.assertCompletions(t, 1, 0)
}

func TestWindowCompletionRound6MissingRowUsesLocalRequest(t *testing.T) {
	h := newCompletionRound6Harness(t)
	if removed, err := h.store.DefaultStore.Delete(context.Background(), h.reservation); err != nil || !removed {
		t.Fatalf("remove backend row: removed=%v error=%v", removed, err)
	}
	h.response(t)
	h.receiveBarrier(t)
	h.assertCompletions(t, 1, 0)
	h.session.bound().expireRequests()
	_ = h.session.Close()
	h.assertCompletions(t, 1, 0)
}

func TestWindowCompletionRound6DeleteFailureReleasesWithoutDuplicate(t *testing.T) {
	h := newCompletionRound6Harness(t)
	h.store.failDelete.Store(true)
	h.response(t)
	h.receiveBarrier(t)
	h.assertCompletions(t, 0, 0)
	select {
	case err := <-h.receivingErrors:
		if !errors.Is(err, errWindowResponseDelete) {
			t.Fatalf("Delete diagnostic: %v", err)
		}
	default:
		t.Fatal("Delete failure was not reported")
	}
	h.store.failDelete.Store(false)
	h.session.bound().expireRequests()
	h.response(t)
	h.receiveBarrier(t)
	_ = h.session.Close()
	h.assertCompletions(t, 0, 0)
}

func TestWindowCompletionRound6StaleGetDoesNotStealExpiry(t *testing.T) {
	h := newCompletionRound6Harness(t)
	var getRelease sync.Once
	releaseGet := func() { getRelease.Do(func() { close(h.store.getRelease) }) }
	t.Cleanup(releaseGet)
	h.store.gateGetID.Store(h.reservation.ID)
	h.response(t)
	lifecycleWait(t, h.store.getEntered)
	h.store.gateID.Store(h.reservation.ID)
	expiryDone := make(chan struct{})
	go func() { defer close(expiryDone); h.session.bound().expireRequests() }()
	t.Cleanup(func() { releaseGet(); h.releaseDelete(); lifecycleWait(t, expiryDone) })
	lifecycleWait(t, h.store.deleteEntered)
	// The response has a stale row. Its conditional Delete must lose without
	// consuming the expiry operation's still-pending local completion.
	releaseGet()
	h.receiveBarrier(t)
	h.releaseDelete()
	lifecycleWait(t, expiryDone)
	h.assertCompletions(t, 0, 1)
	_ = h.session.Close()
	h.assertCompletions(t, 0, 1)
}

func TestWindowCompletionRound6StaleGetAfterExpiryClaim(t *testing.T) {
	h := newCompletionRound6Harness(t)
	var getRelease sync.Once
	releaseGet := func() { getRelease.Do(func() { close(h.store.getRelease) }) }
	t.Cleanup(releaseGet)
	h.store.gateGetID.Store(h.reservation.ID)
	h.response(t)
	lifecycleWait(t, h.store.getEntered)
	h.session.bound().expireRequests()
	releaseGet()
	h.receiveBarrier(t)
	h.assertCompletions(t, 0, 1)
	_ = h.session.Close()
	h.assertCompletions(t, 0, 1)
}

func TestWindowCompletionRound6StaleExpiryDoesNotStealResponse(t *testing.T) {
	h := newCompletionRound6Harness(t)
	var listRelease sync.Once
	releaseList := func() { listRelease.Do(func() { close(h.store.listRelease) }) }
	t.Cleanup(releaseList)
	h.store.gateList.Store(true)
	expiryDone := make(chan struct{})
	go func() { defer close(expiryDone); h.session.bound().expireRequests() }()
	t.Cleanup(func() { releaseList(); h.releaseDelete(); lifecycleWait(t, expiryDone) })
	lifecycleWait(t, h.store.listEntered)
	h.store.gateID.Store(h.reservation.ID)
	h.response(t)
	lifecycleWait(t, h.store.deleteEntered)
	releaseList()
	lifecycleWait(t, expiryDone)
	h.releaseDelete()
	h.receiveBarrier(t)
	h.assertCompletions(t, 1, 0)
	_ = h.session.Close()
	h.assertCompletions(t, 1, 0)
}

func TestWindowCompletionRound6ConcurrentResponsesShareLocalClaim(t *testing.T) {
	h := newCompletionRound6Harness(t)
	h.store.gateID.Store(h.reservation.ID)
	h.response(t)
	lifecycleWait(t, h.store.deleteEntered)
	// Exercise the arbiter directly while the socket reader's completion is
	// awaiting its successful Delete result. Both paths own the same response.
	h.session.bound().in.completeResponse(h.frame.GetResponse(), h.frame.GetSequenceNumber(), nil)
	h.releaseDelete()
	h.receiveBarrier(t)
	h.assertCompletions(t, 1, 0)
	h.session.bound().expireRequests()
	_ = h.session.Close()
	h.assertCompletions(t, 1, 0)
}

func TestWindowCompletionRound6ResponseTimeoutAbandonsBeforeExpiryClaim(t *testing.T) {
	h := newCompletionRound6Harness(t)
	h.store.gateID.Store(h.reservation.ID)
	expiryDone := make(chan struct{})
	go func() { defer close(expiryDone); h.session.bound().expireRequests() }()
	t.Cleanup(func() { h.releaseDelete(); lifecycleWait(t, expiryDone) })
	lifecycleWait(t, h.store.deleteEntered)
	h.store.failGet.Store(true)
	h.response(t)
	h.receiveBarrier(t)
	h.releaseDelete()
	lifecycleWait(t, expiryDone)
	select {
	case err := <-h.receivingErrors:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Get diagnostic: %v", err)
		}
	default:
		t.Fatal("Get timeout was not reported")
	}
	h.assertCompletions(t, 0, 0)
	_ = h.session.Close()
	h.assertCompletions(t, 0, 0)
}
