package gosmpp

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gomaja/go-smpp/pdu"
)

var errWindowForeignUncertainStore = errors.New("uncertain store acknowledgement")

type windowForeignUncertainStore struct {
	DefaultStore
	unavailable atomic.Bool
	getTimeout  atomic.Bool
	gets        atomic.Int32
}

func (s *windowForeignUncertainStore) Set(ctx context.Context, request Request) error {
	if s.unavailable.Load() {
		return errWindowForeignUncertainStore
	}
	return s.DefaultStore.Set(ctx, request)
}

func (s *windowForeignUncertainStore) Delete(ctx context.Context, request Request) (bool, error) {
	if s.unavailable.Load() {
		return false, errWindowForeignUncertainStore
	}
	return s.DefaultStore.Delete(ctx, request)
}

func (s *windowForeignUncertainStore) Get(ctx context.Context, sequence int32) (Request, bool) {
	s.gets.Add(1)
	if s.getTimeout.Load() {
		<-ctx.Done()
		return Request{}, false
	}
	return s.DefaultStore.Get(ctx, sequence)
}

func windowForeignUncertainSession(t *testing.T, store RequestStore, settings Settings) *Session {
	t.Helper()
	conn, cleanup := newPipePair()
	t.Cleanup(cleanup)
	session, err := NewSession(lifecycleConnector{connect: func() (*Connection, error) { return conn, nil }}, settings, 0, WithRequestStore(store))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func windowForeignUncertainCount(out *transmittable) (requests, abandoned int) {
	out.ownerState.mu.Lock()
	requests = len(out.ownerState.requests)
	out.ownerState.mu.Unlock()
	out.lineage.abandoned.Range(func(_, _ any) bool { abandoned++; return true })
	return
}

func windowForeignUncertainRecover(out *transmittable) {
	out.reclaimWG.Wait()
	out.reclaimMu.Lock()
	out.nextReclaim = time.Time{}
	out.reclaimMu.Unlock()
	out.reclaimDeadRequests()
	out.reclaimWG.Wait()
}

func windowForeignUncertainReject(t *testing.T, session *Session, count int) {
	t.Helper()
	for range count {
		if err := session.Transceiver().Submit(pdu.NewSubmitSM()); !errors.Is(err, errWindowForeignUncertainStore) {
			t.Fatalf("failed reservation = %v", err)
		}
	}
}

func TestWindowForeignUncertainAdmissionsResolveAfterSessionClose(t *testing.T) {
	store := &windowForeignUncertainStore{DefaultStore: NewDefaultStore()}
	store.unavailable.Store(true)
	settings := lifecycleSettings()
	settings.StoreAccessTimeOut = 10 * time.Millisecond
	var callbacks atomic.Int32
	settings.OnClosePduRequest = func(pdu.PDU) { callbacks.Add(1) }
	settings.OnExpiredPduRequest = func(pdu.PDU) bool { callbacks.Add(1); return false }
	first := windowForeignUncertainSession(t, store, settings)
	out := first.bound().out
	// Keep cleanup uncertain while closing, rather than letting a healthy read
	// establish that these acknowledged-as-failed writes never created rows.
	store.getTimeout.Store(true)
	windowForeignUncertainReject(t, first, 3)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { liveRequestOwners.Delete(out.owner) })
	if requests, abandoned := windowForeignUncertainCount(out); requests != 3 || abandoned != 3 {
		t.Fatalf("failed close lost uncertainty: requests=%d abandoned=%d", requests, abandoned)
	}
	store.unavailable.Store(false)
	store.getTimeout.Store(false)
	second := windowForeignUncertainSession(t, store, settings)
	windowForeignUncertainRecover(second.bound().out)
	requests, abandoned := windowForeignUncertainCount(out)
	_, retained := liveRequestOwners.Load(out.owner)
	if requests != 0 || abandoned != 0 || retained {
		t.Fatalf("healthy shared-store recovery retained absent foreign identities: requests=%d abandoned=%d owner=%t", requests, abandoned, retained)
	}
	if callbacks.Load() != 0 {
		t.Fatal("never-accepted foreign requests produced completion callbacks")
	}
}

func TestWindowForeignUncertainRecoveryLeavesDifferentBackendUntouched(t *testing.T) {
	firstStore := &windowForeignUncertainStore{DefaultStore: NewDefaultStore()}
	firstStore.unavailable.Store(true)
	firstStore.getTimeout.Store(true)
	settings := lifecycleSettings()
	settings.StoreAccessTimeOut = 10 * time.Millisecond
	first := windowForeignUncertainSession(t, firstStore, settings)
	out := first.bound().out
	windowForeignUncertainReject(t, first, 2)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { liveRequestOwners.Delete(out.owner) })
	firstStore.unavailable.Store(false)
	firstStore.getTimeout.Store(false)
	gets := firstStore.gets.Load()
	secondStore := &windowForeignUncertainStore{DefaultStore: NewDefaultStore()}
	second := windowForeignUncertainSession(t, secondStore, settings)
	windowForeignUncertainRecover(second.bound().out)
	requests, abandoned := windowForeignUncertainCount(out)
	_, retained := liveRequestOwners.Load(out.owner)
	if requests != 2 || abandoned != 2 || !retained || firstStore.gets.Load() != gets {
		t.Fatalf("unrelated backend affected foreign uncertainty: requests=%d abandoned=%d owner=%t Get=%d, before=%d", requests, abandoned, retained, firstStore.gets.Load(), gets)
	}
}

func TestWindowCloseSettlesAbsentUncertainAdmissions(t *testing.T) {
	store := &windowForeignUncertainStore{DefaultStore: NewDefaultStore()}
	store.unavailable.Store(true)
	var callbacks atomic.Int32
	settings := lifecycleSettings()
	settings.OnClosePduRequest = func(pdu.PDU) { callbacks.Add(1) }
	first := windowForeignUncertainSession(t, store, settings)
	out := first.bound().out
	// Only Close may settle these identities; no accepted writer work exists.
	out.reclaimMu.Lock()
	out.nextReclaim = time.Now().Add(time.Hour)
	out.reclaimMu.Unlock()
	windowForeignUncertainReject(t, first, 5)
	store.unavailable.Store(false)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { liveRequestOwners.Delete(out.owner) })
	requests, abandoned := windowForeignUncertainCount(out)
	_, retained := liveRequestOwners.Load(out.owner)
	if requests != 0 || abandoned != 0 || retained {
		t.Fatalf("healthy close retained absent admission identities: requests=%d abandoned=%d owner=%t", requests, abandoned, retained)
	}
	if callbacks.Load() != 0 {
		t.Fatal("close completed requests that were never accepted")
	}
}

func TestWindowForeignUncertainRecoveryRetainsTimedOutRead(t *testing.T) {
	store := &windowForeignUncertainStore{DefaultStore: NewDefaultStore()}
	store.unavailable.Store(true)
	store.getTimeout.Store(true)
	settings := lifecycleSettings()
	settings.StoreAccessTimeOut = 10 * time.Millisecond
	first := windowForeignUncertainSession(t, store, settings)
	out := first.bound().out
	windowForeignUncertainReject(t, first, 2)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { liveRequestOwners.Delete(out.owner) })
	store.unavailable.Store(false)
	var failures atomic.Int32
	settings.OnReceivingError = func(err error) {
		if errors.Is(err, context.DeadlineExceeded) {
			failures.Add(1)
		}
	}
	second := windowForeignUncertainSession(t, store, settings)
	windowForeignUncertainRecover(second.bound().out)
	requests, abandoned := windowForeignUncertainCount(out)
	if requests != 2 || abandoned != 2 || failures.Load() == 0 {
		t.Fatalf("failed foreign lookup lost uncertainty or diagnostic: requests=%d abandoned=%d errors=%d", requests, abandoned, failures.Load())
	}
	store.getTimeout.Store(false)
	windowForeignUncertainRecover(second.bound().out)
	requests, abandoned = windowForeignUncertainCount(out)
	if requests != 0 || abandoned != 0 {
		t.Fatalf("healthy foreign lookup retained uncertainty: requests=%d abandoned=%d", requests, abandoned)
	}
}

func TestWindowCleanupFullTriggersRecoveryAfterStoreRestored(t *testing.T) {
	store := &windowForeignUncertainStore{DefaultStore: NewDefaultStore()}
	store.unavailable.Store(true)
	session := windowForeignUncertainSession(t, store, lifecycleSettings())
	out := session.bound().out
	// Keep an outage scan from resolving known-absent rows before the bound is
	// reached. After restoration, admission itself must request a fresh scan.
	out.reclaimMu.Lock()
	out.nextReclaim = time.Now().Add(time.Hour)
	out.reclaimMu.Unlock()
	windowForeignUncertainReject(t, session, 1024)
	store.unavailable.Store(false)
	out.reclaimMu.Lock()
	out.nextReclaim = time.Time{}
	out.reclaimMu.Unlock()
	// The first retry may hit the cleanup bound, but must itself trigger
	// recovery: no accepted writer work exists to notice the orphan hint.
	_ = session.Transceiver().Submit(pdu.NewSubmitSM())
	out.reclaimWG.Wait()
	if err := session.Transceiver().Submit(pdu.NewSubmitSM()); err != nil {
		t.Fatalf("healthy retry after cleanup-cap recovery = %v", err)
	}
	// Recovery bounds each pass to 256 lookups; join the continuation passes
	// before checking that all 1024 outage identities have been settled.
	for range 3 {
		windowForeignUncertainRecover(out)
	}
	requests, abandoned := windowForeignUncertainCount(out)
	if requests > 2 || abandoned != 0 || out.lineage.orphaned.Load() {
		t.Fatalf("healthy retries retained outage identities: requests=%d abandoned=%d hint=%t", requests, abandoned, out.lineage.orphaned.Load())
	}
}

type windowCommittedUncertainStore struct{ *windowForeignUncertainStore }

func (s windowCommittedUncertainStore) Set(ctx context.Context, request Request) error {
	if err := s.DefaultStore.Set(ctx, request); err != nil {
		return err
	}
	if s.unavailable.Load() {
		return errWindowForeignUncertainStore
	}
	return nil
}

func TestWindowUncertainCommittedAdmissionAllowsDuplicateRetryAfterRecovery(t *testing.T) {
	backend := &windowForeignUncertainStore{DefaultStore: NewDefaultStore()}
	backend.unavailable.Store(true)
	store := windowCommittedUncertainStore{windowForeignUncertainStore: backend}
	settings := lifecycleSettings()
	var callbacks atomic.Int32
	settings.OnClosePduRequest = func(pdu.PDU) { callbacks.Add(1) }
	settings.OnExpiredPduRequest = func(pdu.PDU) bool { callbacks.Add(1); return false }
	session := windowForeignUncertainSession(t, store, settings)
	out := session.bound().out
	request := pdu.NewSubmitSM()
	if err := session.Transceiver().Submit(request); !errors.Is(err, errWindowForeignUncertainStore) {
		t.Fatalf("ambiguous reservation = %v", err)
	}
	original, exists := backend.DefaultStore.Get(context.Background(), request.GetSequenceNumber())
	if !exists {
		t.Fatal("ambiguous Set did not commit its reservation")
	}
	backend.unavailable.Store(false)
	// Permit a recovery attempt now even if the failed initial admission already
	// triggered one during the outage. The retry must schedule it itself.
	out.reclaimWG.Wait()
	out.reclaimMu.Lock()
	out.nextReclaim = time.Time{}
	out.reclaimMu.Unlock()
	firstErr := session.Transceiver().Submit(request)
	out.reclaimWG.Wait()
	if firstErr != nil {
		if !errors.Is(firstErr, ErrSequenceInUse) {
			t.Fatalf("first healthy retry = %v", firstErr)
		}
		if err := session.Transceiver().Submit(request); err != nil {
			t.Fatalf("healthy duplicate retry remained blocked after recovery: %v", err)
		}
	}
	current, exists := backend.DefaultStore.Get(context.Background(), request.GetSequenceNumber())
	if !exists || current.ID == original.ID {
		t.Fatal("healthy retry did not replace the silently cleaned uncertain identity")
	}
	_, abandoned := windowForeignUncertainCount(out)
	if abandoned != 0 || callbacks.Load() != 0 {
		t.Fatalf("uncertain retry retained ghosts or completion callbacks: abandoned=%d callbacks=%d", abandoned, callbacks.Load())
	}
}

type windowNonComparableUncertainStore struct {
	RequestStore
	tags []byte
}

func TestWindowForeignUncertainRecoveryProtectsNonComparableStoreValue(t *testing.T) {
	backend := &windowForeignUncertainStore{DefaultStore: NewDefaultStore()}
	backend.unavailable.Store(true)
	backend.getTimeout.Store(true)
	store := windowNonComparableUncertainStore{RequestStore: backend, tags: []byte{1}}
	settings := lifecycleSettings()
	settings.StoreAccessTimeOut = 10 * time.Millisecond
	first := windowForeignUncertainSession(t, store, settings)
	out := first.bound().out
	windowForeignUncertainReject(t, first, 2)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { liveRequestOwners.Delete(out.owner) })
	backend.unavailable.Store(false)
	backend.getTimeout.Store(false)
	second := windowForeignUncertainSession(t, store, settings)
	windowForeignUncertainRecover(second.bound().out)
	requests, abandoned := windowForeignUncertainCount(out)
	if requests != 2 || abandoned != 2 {
		t.Fatalf("a non-comparable wrapper established backend identity: requests=%d abandoned=%d", requests, abandoned)
	}
}

func TestWindowForeignUncertainRecoveryRequiresRetiredOwner(t *testing.T) {
	store := &windowForeignUncertainStore{DefaultStore: NewDefaultStore()}
	store.unavailable.Store(true)
	first := windowForeignUncertainSession(t, store, lifecycleSettings())
	out := first.bound().out
	// Isolate the other Session's scan from this live owner's own recovery.
	out.reclaimMu.Lock()
	out.nextReclaim = time.Now().Add(time.Hour)
	out.reclaimMu.Unlock()
	windowForeignUncertainReject(t, first, 2)
	store.unavailable.Store(false)
	// A closed-lineage marker alone cannot authorize cleanup while a known
	// connection owner remains live. Both lifecycle conditions are required.
	first.lineage.markLineageClosed()
	second := windowForeignUncertainSession(t, store, lifecycleSettings())
	windowForeignUncertainRecover(second.bound().out)
	requests, abandoned := windowForeignUncertainCount(out)
	if requests != 2 || abandoned != 2 {
		t.Fatalf("foreign recovery settled a live owner's identities: requests=%d abandoned=%d", requests, abandoned)
	}
}

func windowForeignUncertainRebindingSession(t *testing.T, store RequestStore, settings Settings) *Session {
	t.Helper()
	first, cleanFirst := newPipePair()
	next, cleanNext := newPipePair()
	t.Cleanup(cleanFirst)
	t.Cleanup(cleanNext)
	var connects atomic.Int32
	session, err := NewSession(lifecycleConnector{connect: func() (*Connection, error) {
		if connects.Add(1) == 1 {
			return first, nil
		}
		return next, nil
	}}, settings, 0, WithRequestStore(store))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func TestWindowForeignUncertainRecoveryProtectsReboundLiveLineage(t *testing.T) {
	store := &windowForeignUncertainStore{DefaultStore: NewDefaultStore()}
	store.unavailable.Store(true)
	store.getTimeout.Store(true)
	settings := lifecycleSettings()
	settings.StoreAccessTimeOut = 10 * time.Millisecond
	first := windowForeignUncertainRebindingSession(t, store, settings)
	old := first.bound().out
	windowForeignUncertainReject(t, first, 2)
	first.rebind()
	if first.bound().out.owner == old.owner || first.lineage.closed.Load() {
		t.Fatal("rebind did not retain a live lineage with a retired generation")
	}
	store.unavailable.Store(false)
	store.getTimeout.Store(false)
	second := windowForeignUncertainSession(t, store, settings)
	windowForeignUncertainRecover(second.bound().out)
	requests, abandoned := windowForeignUncertainCount(old)
	if requests != 2 || abandoned != 2 {
		t.Fatalf("another Session resolved a live lineage's uncertainty: requests=%d abandoned=%d", requests, abandoned)
	}
}

func TestWindowForeignUncertainRecoveryChecksLineageOnce(t *testing.T) {
	store := &windowForeignUncertainStore{DefaultStore: NewDefaultStore()}
	store.unavailable.Store(true)
	store.getTimeout.Store(true)
	settings := lifecycleSettings()
	settings.StoreAccessTimeOut = 10 * time.Millisecond
	first := windowForeignUncertainRebindingSession(t, store, settings)
	old := first.bound().out
	windowForeignUncertainReject(t, first, 2)
	first.rebind()
	windowForeignUncertainReject(t, first, 2)
	current := first.bound().out
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { liveRequestOwners.Delete(old.owner); liveRequestOwners.Delete(current.owner) })
	store.unavailable.Store(false)
	var failures atomic.Int32
	settings.OnReceivingError = func(err error) {
		if errors.Is(err, context.DeadlineExceeded) {
			failures.Add(1)
		}
	}
	second := windowForeignUncertainSession(t, store, settings)
	// Measure one blocked lookup per lineage without expiring during local
	// bookkeeping. Healthy continuation below tests settlement separately.
	second.bound().out.settings.StoreAccessTimeOut = 100 * time.Millisecond
	before := store.gets.Load()
	windowForeignUncertainRecover(second.bound().out)
	if gets := store.gets.Load() - before; gets != 1 || failures.Load() != 1 {
		t.Fatalf("one unavailable lineage was checked per generation: Get=%d errors=%d", gets, failures.Load())
	}
	store.getTimeout.Store(false)
	second.bound().out.settings.StoreAccessTimeOut = 5 * time.Second
	windowForeignUncertainRecover(second.bound().out)
	for _, owner := range []*transmittable{old, current} {
		requests, _ := windowForeignUncertainCount(owner)
		if requests != 0 {
			t.Fatalf("healthy lineage recovery retained generation %d: requests=%d", owner.owner, requests)
		}
	}
}

func TestWindowForeignUncertainCommittedRecoveryClearsRetiredLineage(t *testing.T) {
	backend := &windowForeignUncertainStore{DefaultStore: NewDefaultStore()}
	backend.unavailable.Store(true)
	backend.getTimeout.Store(true)
	store := windowCommittedUncertainStore{windowForeignUncertainStore: backend}
	settings := lifecycleSettings()
	settings.StoreAccessTimeOut = 10 * time.Millisecond
	var callbacks atomic.Int32
	settings.OnClosePduRequest = func(pdu.PDU) { callbacks.Add(1) }
	settings.OnExpiredPduRequest = func(pdu.PDU) bool { callbacks.Add(1); return false }
	first := windowForeignUncertainSession(t, store, settings)
	out := first.bound().out
	windowForeignUncertainReject(t, first, 1)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { liveRequestOwners.Delete(out.owner) })
	if requests, abandoned := windowForeignUncertainCount(out); requests != 1 || abandoned != 1 {
		t.Fatalf("outage close lost its uncertain identity: requests=%d abandoned=%d", requests, abandoned)
	}
	backend.unavailable.Store(false)
	backend.getTimeout.Store(false)
	second := windowForeignUncertainSession(t, store, settings)
	windowForeignUncertainRecover(second.bound().out)
	requests, abandoned := windowForeignUncertainCount(out)
	_, retained := liveRequestOwners.Load(out.owner)
	rows := len(backend.DefaultStore.List(context.Background()))
	if rows != 0 || requests != 0 || abandoned != 0 || retained || out.lineage.orphaned.Load() {
		t.Fatalf("closed foreign committed uncertainty: rows=%d requests=%d abandoned=%d owner=%t hint=%t", rows, requests, abandoned, retained, out.lineage.orphaned.Load())
	}
	if callbacks.Load() != 0 {
		t.Fatal("a failed admission produced a foreign completion callback")
	}
}

type windowPartialForeignUncertainStore struct {
	windowCommittedUncertainStore
	partial atomic.Bool
	deleted atomic.Bool
}

func (s *windowPartialForeignUncertainStore) Delete(ctx context.Context, request Request) (bool, error) {
	if s.partial.Load() && !s.unavailable.Load() && !s.deleted.CompareAndSwap(false, true) {
		return false, errWindowForeignUncertainStore
	}
	return s.windowCommittedUncertainStore.Delete(ctx, request)
}

func TestWindowForeignUncertainCleanupPreservesRemainingIdentity(t *testing.T) {
	backend := &windowForeignUncertainStore{DefaultStore: NewDefaultStore()}
	backend.unavailable.Store(true)
	backend.getTimeout.Store(true)
	store := &windowPartialForeignUncertainStore{windowCommittedUncertainStore: windowCommittedUncertainStore{windowForeignUncertainStore: backend}}
	settings := lifecycleSettings()
	settings.StoreAccessTimeOut = 10 * time.Millisecond
	var callbacks atomic.Int32
	settings.OnClosePduRequest = func(pdu.PDU) { callbacks.Add(1) }
	settings.OnExpiredPduRequest = func(pdu.PDU) bool { callbacks.Add(1); return false }
	first := windowForeignUncertainSession(t, store, settings)
	out := first.bound().out
	windowForeignUncertainReject(t, first, 2)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { liveRequestOwners.Delete(out.owner) })
	backend.unavailable.Store(false)
	backend.getTimeout.Store(false)
	store.partial.Store(true)
	second := windowForeignUncertainSession(t, store, settings)
	windowForeignUncertainRecover(second.bound().out)
	requests, abandoned := windowForeignUncertainCount(out)
	if rows := len(backend.DefaultStore.List(context.Background())); rows != 1 || requests != 1 || abandoned != 1 || !out.lineage.orphaned.Load() {
		t.Fatalf("partial recovery lost the remaining cleanup identity: rows=%d requests=%d abandoned=%d hint=%t", rows, requests, abandoned, out.lineage.orphaned.Load())
	}
	store.partial.Store(false)
	windowForeignUncertainRecover(second.bound().out)
	requests, abandoned = windowForeignUncertainCount(out)
	if requests != 0 || abandoned != 0 || out.lineage.orphaned.Load() || callbacks.Load() != 0 {
		t.Fatalf("final cleanup retained identities or callbacks: requests=%d abandoned=%d hint=%t callbacks=%d", requests, abandoned, out.lineage.orphaned.Load(), callbacks.Load())
	}
}

func TestWindowForeignUncertainLiveLineageKeepsExpiryArbiter(t *testing.T) {
	backend := &windowForeignUncertainStore{DefaultStore: NewDefaultStore()}
	backend.unavailable.Store(true)
	backend.getTimeout.Store(true)
	store := windowCommittedUncertainStore{windowForeignUncertainStore: backend}
	settings := lifecycleSettings()
	settings.StoreAccessTimeOut = 10 * time.Millisecond
	var callbacks atomic.Int32
	settings.OnClosePduRequest = func(pdu.PDU) { callbacks.Add(1) }
	settings.OnExpiredPduRequest = func(pdu.PDU) bool { callbacks.Add(1); return false }
	first := windowForeignUncertainRebindingSession(t, store, settings)
	out := first.bound().out
	windowForeignUncertainReject(t, first, 1)
	first.rebind()
	if first.lineage.closed.Load() {
		t.Fatal("rebind closed the active Session lineage")
	}
	backend.unavailable.Store(false)
	backend.getTimeout.Store(false)
	// The active successor must consume the old abandoned marker after Delete
	// wins, so cleanup of a rejected submission cannot also invoke expiry.
	first.bound().expireRequests()
	requests, abandoned := windowForeignUncertainCount(out)
	if rows := len(backend.DefaultStore.List(context.Background())); rows != 0 || requests != 0 || abandoned != 0 || callbacks.Load() != 0 {
		t.Fatalf("active-lineage cleanup lost its expiry arbiter: rows=%d requests=%d abandoned=%d callbacks=%d", rows, requests, abandoned, callbacks.Load())
	}
}
