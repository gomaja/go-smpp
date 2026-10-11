package gosmpp

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gomaja/go-smpp/pdu"
)

type windowDeadOwnerStore struct {
	DefaultStore
	failDeletes     atomic.Bool
	closed          atomic.Bool
	setsAfterClose  atomic.Int32
	deletedContexts chan context.Context
}

func (s *windowDeadOwnerStore) Set(ctx context.Context, request Request) error {
	if s.closed.Load() {
		s.setsAfterClose.Add(1)
	}
	return s.DefaultStore.Set(ctx, request)
}

func (s *windowDeadOwnerStore) Delete(ctx context.Context, request Request) (bool, error) {
	if s.failDeletes.Load() {
		return false, context.DeadlineExceeded
	}
	removed, err := s.DefaultStore.Delete(ctx, request)
	if s.deletedContexts != nil {
		s.deletedContexts <- ctx
	}
	return removed, err
}

func TestWindowReclaimsDeadOwnerWhenExpiryDisabled(t *testing.T) {
	oldConn, oldClean := newPipePair()
	defer oldClean()
	liveConn, liveClean := newPipePair()
	defer liveClean()
	store := &windowDeadOwnerStore{DefaultStore: NewDefaultStore(), deletedContexts: make(chan context.Context, 8)}
	settings := lifecycleSettings()
	settings.MaxWindowSize = 2
	settings.StoreAccessTimeOut = 20 * time.Millisecond
	var expired, closed, storeErrors atomic.Int32
	settings.OnExpiredPduRequest = func(pdu.PDU) bool { expired.Add(1); return false }
	settings.OnReceivingError = func(error) { storeErrors.Add(1) }
	old := newTransceivable(oldConn, settings, store)
	oldRequests := map[pdu.PDU]bool{}
	for range 2 {
		request := pdu.NewSubmitSM()
		oldRequests[request] = true
		if _, err := old.out.write(request); err != nil {
			t.Fatal(err)
		}
	}
	store.failDeletes.Store(true)
	retireWindowGeneration(t, old)
	store.failDeletes.Store(false)
	settings.OnClosePduRequest = func(request pdu.PDU) {
		if !oldRequests[request] {
			return
		}
		closed.Add(1)
		// Callback work must not consume another orphan's store deadline.
		<-(<-store.deletedContexts).Done()
	}
	live := newTransceivable(liveConn, settings, store)
	live.out.lineage = old.out.lineage
	defer func() { _ = live.Close() }()
	live.out.reclaimDeadRequests()
	live.out.reclaimWG.Wait()
	fresh := pdu.NewSubmitSM()
	if _, err := live.out.write(fresh); err != nil {
		t.Fatalf("disabled expiry left dead-owner window permanently full: %v", err)
	}
	live.out.reclaimWG.Wait()
	entries := store.List(context.Background())
	if len(entries) != 1 || entries[0].PDU != fresh || entries[0].Owner != live.out.owner {
		t.Fatalf("dead-owner entries consumed the new window: %+v", entries)
	}
	if closed.Load() != 2 || expired.Load() != 0 || storeErrors.Load() != 2 {
		t.Fatalf("orphan completion: close=%d expiry=%d store errors=%d", closed.Load(), expired.Load(), storeErrors.Load())
	}
}

func TestWindowReclamationProtectsLiveOwner(t *testing.T) {
	aConn, aClean := newPipePair()
	defer aClean()
	bConn, bClean := newPipePair()
	defer bClean()
	settings := lifecycleSettings()
	settings.MaxWindowSize = 1
	var closed atomic.Int32
	settings.OnClosePduRequest = func(pdu.PDU) { closed.Add(1) }
	store := NewDefaultStore()
	a := newTransceivable(aConn, settings, store)
	b := newTransceivable(bConn, settings, store)
	a.start()
	b.start()
	defer func() { _ = a.Close(); _ = b.Close() }()
	owned := pdu.NewSubmitSM()
	if _, err := b.out.write(owned); err != nil {
		t.Fatal(err)
	}
	if _, err := a.out.write(pdu.NewSubmitSM()); !errors.Is(err, ErrWindowsFull) {
		t.Fatalf("new write reclaimed another live owner's reservation: %v", err)
	}
	request, ok := store.Get(context.Background(), owned.GetSequenceNumber())
	if !ok || request.Owner != b.out.owner || closed.Load() != 0 {
		t.Fatalf("live owner's reservation was lost: present=%v owner=%d close callbacks=%d", ok, request.Owner, closed.Load())
	}
}

func TestWindowReclamationCallbackCanCloseWriter(t *testing.T) {
	oldConn, oldClean := newPipePair()
	defer oldClean()
	liveConn, liveClean := newPipePair()
	defer liveClean()
	store := &windowDeadOwnerStore{DefaultStore: NewDefaultStore()}
	settings := lifecycleSettings()
	old := newTransceivable(oldConn, settings, store)
	orphan := pdu.NewSubmitSM()
	if _, err := old.out.write(orphan); err != nil {
		t.Fatal(err)
	}
	store.failDeletes.Store(true)
	retireWindowGeneration(t, old)
	store.failDeletes.Store(false)
	var live *transceivable
	var callbacks atomic.Int32
	callbackDone := make(chan struct{})
	settings.OnClosePduRequest = func(request pdu.PDU) {
		if request == orphan {
			callbacks.Add(1)
			_ = live.Close()
			store.closed.Store(true)
			close(callbackDone)
		}
	}
	settings.OnSubmitError = func(_ pdu.PDU, err error) {
		if !errors.Is(err, ErrConnectionClosing) {
			t.Errorf("shutdown submission error: %v", err)
		}
	}
	live = newTransceivable(liveConn, settings, store)
	live.out.lineage = old.out.lineage
	defer func() { _ = live.Close() }()
	live.start()
	if err := live.Submit(pdu.NewSubmitSM()); err != nil && !errors.Is(err, ErrConnectionClosing) {
		t.Fatalf("reclamation submission: %v", err)
	}
	lifecycleWait(t, callbackDone)
	if err := live.Close(); err != nil {
		t.Fatal(err)
	}
	n, _ := store.Length(context.Background())
	if callbacks.Load() != 1 || n != 0 || requestOwnerAlive(live.out.owner) {
		t.Fatalf("closed writer reserved another request: callbacks=%d window=%d registered=%v post-close sets=%d", callbacks.Load(), n, requestOwnerAlive(live.out.owner), store.setsAfterClose.Load())
	}
}

type windowReclamationSnapshotStore struct {
	*windowDeadOwnerStore
	gate              atomic.Bool
	captured, release chan struct{}
}

func (s *windowReclamationSnapshotStore) List(ctx context.Context) []Request {
	requests := s.DefaultStore.List(ctx)
	if s.gate.CompareAndSwap(true, false) {
		close(s.captured)
		<-s.release
	}
	return requests
}

func TestWindowReclamationLosesToExpiryCompletion(t *testing.T) {
	oldConn, oldClean := newPipePair()
	defer oldClean()
	writerConn, writerClean := newPipePair()
	defer writerClean()
	expiryConn, expiryClean := newPipePair()
	defer expiryClean()
	store := &windowReclamationSnapshotStore{windowDeadOwnerStore: &windowDeadOwnerStore{DefaultStore: NewDefaultStore()}, captured: make(chan struct{}), release: make(chan struct{})}
	settings := lifecycleSettings()
	old := newTransceivable(oldConn, settings, store)
	if _, err := old.out.write(pdu.NewSubmitSM()); err != nil {
		t.Fatal(err)
	}
	store.failDeletes.Store(true)
	retireWindowGeneration(t, old)
	store.failDeletes.Store(false)
	var closed, expired atomic.Int32
	settings.OnClosePduRequest = func(pdu.PDU) { closed.Add(1) }
	writer := newTransceivable(writerConn, settings, store)
	writer.out.lineage = old.out.lineage
	defer func() { _ = writer.Close() }()
	settings.PduExpireTimeOut = time.Nanosecond
	settings.OnExpiredPduRequest = func(pdu.PDU) bool { expired.Add(1); return false }
	expiry := newTransceivable(expiryConn, settings, store)
	expiry.out.lineage = old.out.lineage
	defer func() { _ = expiry.Close() }()
	store.gate.Store(true)
	written := make(chan struct{})
	go func() {
		defer close(written)
		if _, err := writer.out.write(pdu.NewSubmitSM()); err != nil {
			t.Errorf("fresh write: %v", err)
		}
	}()
	lifecycleWait(t, store.captured)
	expiry.expireRequests()
	close(store.release)
	lifecycleWait(t, written)
	writer.out.reclaimWG.Wait()
	if expired.Load() != 1 || closed.Load() != 0 {
		t.Fatalf("stale recovery snapshot completed request twice: expiry=%d close=%d", expired.Load(), closed.Load())
	}
}
