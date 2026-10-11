package gosmpp

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gomaja/go-smpp/pdu"
)

type recoveryBudgetStore struct {
	DefaultStore
	admissionOutage atomic.Bool
	deleteTimeout   atomic.Bool
	getAllTimeout   atomic.Bool
	getTimeout      atomic.Int32
	gets            atomic.Int32
	deletes         atomic.Int32
}

func (s *recoveryBudgetStore) Set(ctx context.Context, request Request) error {
	if s.admissionOutage.Load() {
		return errors.New("reservation unavailable")
	}
	return s.DefaultStore.Set(ctx, request)
}

func (s *recoveryBudgetStore) Delete(ctx context.Context, request Request) (bool, error) {
	if s.admissionOutage.Load() {
		return false, errors.New("cleanup unavailable")
	}
	s.deletes.Add(1)
	if s.deleteTimeout.Load() {
		<-ctx.Done()
		return false, ctx.Err()
	}
	return s.DefaultStore.Delete(ctx, request)
}

func (s *recoveryBudgetStore) Get(ctx context.Context, sequence int32) (Request, bool) {
	s.gets.Add(1)
	if s.getAllTimeout.Load() {
		<-ctx.Done()
		return Request{}, false
	}
	if blocked := s.getTimeout.Load(); blocked == -1 {
		s.getTimeout.CompareAndSwap(-1, sequence)
	}
	if s.getTimeout.Load() == sequence {
		<-ctx.Done()
		return Request{}, false
	}
	return s.DefaultStore.Get(ctx, sequence)
}

func recoveryBudgetOwner(t *testing.T, store *recoveryBudgetStore, count int) *transmittable {
	t.Helper()
	settings := lifecycleSettings()
	settings.StoreAccessTimeOut = 20 * time.Millisecond
	out := newTransmittable(nil, settings, store)
	out.nextReclaim = time.Now().Add(time.Hour)
	for range count {
		if err := out.Submit(pdu.NewSubmitSM()); err == nil {
			t.Fatal("outage admitted a request")
		}
	}
	t.Cleanup(func() { liveRequestOwners.Delete(out.owner) })
	return out
}

func TestWindowCloseFlushBoundsStoreTimeWithoutLosingCompletions(t *testing.T) {
	conn, cleanup := newPipePair()
	defer cleanup()
	store := &recoveryBudgetStore{DefaultStore: NewDefaultStore()}
	settings := lifecycleSettings()
	settings.StoreAccessTimeOut = 20 * time.Millisecond
	var failures, callbacks atomic.Int32
	settings.OnReceivingError = func(error) { failures.Add(1) }
	settings.OnClosePduRequest = func(pdu.PDU) { callbacks.Add(1) }
	out := newTransmittable(conn, settings, store)
	for range 16 {
		request := out.newRequest(pdu.NewSubmitSM())
		if err := out.rememberRequest(request); err != nil {
			t.Fatal(err)
		}
		if err := store.Set(context.Background(), request); err != nil {
			t.Fatal(err)
		}
		if !out.beginTransmission(request) {
			t.Fatal("request did not begin transmission")
		}
		out.rememberWritten(request, time.Now())
	}
	store.deleteTimeout.Store(true)
	if err := out.close(ExplicitClosing); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { liveRequestOwners.Delete(out.owner) })
	if deletes := store.deletes.Load(); deletes < 1 || deletes > cleanupParallelism {
		t.Fatalf("close started %d blocked cleanup operations; want one bounded wave", deletes)
	}
	if !out.lineage.orphaned.Load() || failures.Load() == 0 || callbacks.Load() != 16 {
		t.Fatalf("failed flush lost cleanup accounting: hint=%t errors=%d callbacks=%d", out.lineage.orphaned.Load(), failures.Load(), callbacks.Load())
	}
	if requests, _ := cleanupOutstanding(out); requests != 16 {
		t.Fatalf("failed flush forgot %d of 16 cleanup obligations", 16-requests)
	}
}

func TestWindowRecoveryLimitsGetsAcrossClosedLineages(t *testing.T) {
	store := &recoveryBudgetStore{DefaultStore: NewDefaultStore()}
	store.admissionOutage.Store(true)
	var owners []*transmittable
	for range 600 {
		out := recoveryBudgetOwner(t, store, 1)
		out.retireRequestOwner()
		out.lineage.markLineageClosed()
		owners = append(owners, out)
	}
	caller := recoveryBudgetOwner(t, store, 0)
	// This case measures the lookup-count cap, independently of the deadline.
	caller.settings.StoreAccessTimeOut = 5 * time.Second
	store.admissionOutage.Store(false)
	for scan := range 3 {
		before := store.gets.Load()
		forceCleanupScan(caller)
		if gets := store.gets.Load() - before; gets > 256 {
			t.Fatalf("scan %d performed %d remote Gets; limit 256", scan, gets)
		}
	}
	for _, owner := range owners {
		if requests, abandoned := cleanupOutstanding(owner); requests != 0 || abandoned != 0 {
			t.Fatalf("bounded continuation starved a closed lineage: requests=%d abandoned=%d", requests, abandoned)
		}
	}
}

func TestWindowRecoveryLimitsGetsWithinLineage(t *testing.T) {
	store := &recoveryBudgetStore{DefaultStore: NewDefaultStore()}
	store.admissionOutage.Store(true)
	out := recoveryBudgetOwner(t, store, 600)
	out.settings.StoreAccessTimeOut = 5 * time.Second
	store.admissionOutage.Store(false)
	for scan := range 3 {
		before := store.gets.Load()
		forceCleanupScan(out)
		if gets := store.gets.Load() - before; gets > 256 {
			t.Fatalf("scan %d performed %d remote Gets within one lineage; limit 256", scan, gets)
		}
	}
	if requests, abandoned := cleanupOutstanding(out); requests != 0 || abandoned != 0 {
		t.Fatalf("bounded continuation retained uncertainty: requests=%d abandoned=%d", requests, abandoned)
	}
}

func TestWindowRecoveryRotatesAfterUnavailableIdentity(t *testing.T) {
	store := &recoveryBudgetStore{DefaultStore: NewDefaultStore()}
	store.admissionOutage.Store(true)
	out := recoveryBudgetOwner(t, store, 8)
	store.admissionOutage.Store(false)
	store.getTimeout.Store(-1)
	for range 3 {
		forceCleanupScan(out)
	}
	if requests, abandoned := cleanupOutstanding(out); requests != 1 || abandoned != 1 {
		t.Fatalf("unavailable identity starved healthy absence lookups: requests=%d abandoned=%d; want one", requests, abandoned)
	}
	store.getTimeout.Store(0)
	forceCleanupScan(out)
	if requests, abandoned := cleanupOutstanding(out); requests != 0 || abandoned != 0 {
		t.Fatalf("restored lookup retained uncertainty: requests=%d abandoned=%d", requests, abandoned)
	}
}

func TestWindowRecoveryUsesOneStoreDeadline(t *testing.T) {
	store := &recoveryBudgetStore{DefaultStore: NewDefaultStore()}
	store.admissionOutage.Store(true)
	out := recoveryBudgetOwner(t, store, 16)
	store.admissionOutage.Store(false)
	store.getAllTimeout.Store(true)
	forceCleanupScan(out)
	if gets := store.gets.Load(); gets != 1 {
		t.Fatalf("timed-out recovery performed %d lookups; want one", gets)
	}
	if requests, abandoned := cleanupOutstanding(out); requests != 16 || abandoned != 16 || !out.lineage.orphaned.Load() {
		t.Fatalf("timed-out scan lost retry obligations: requests=%d abandoned=%d hint=%t", requests, abandoned, out.lineage.orphaned.Load())
	}
}

func TestWindowRecoveryRotatesAcrossClosedLineages(t *testing.T) {
	store := &recoveryBudgetStore{DefaultStore: NewDefaultStore()}
	store.admissionOutage.Store(true)
	var owners []*transmittable
	for range 4 {
		out := recoveryBudgetOwner(t, store, 1)
		out.retireRequestOwner()
		out.lineage.markLineageClosed()
		owners = append(owners, out)
	}
	caller := recoveryBudgetOwner(t, store, 0)
	store.admissionOutage.Store(false)
	store.getTimeout.Store(-1)
	forceCleanupScan(caller)
	blocked := store.getTimeout.Load()
	if blocked <= 0 {
		t.Fatal("first recovery did not reach the gated lookup")
	}
	forceCleanupScan(caller)
	for _, owner := range owners {
		requests, abandoned := cleanupOutstanding(owner)
		owner.ownerState.mu.Lock()
		_, stuck := localRequestForSequence(owner.ownerState.requests, blocked)
		owner.ownerState.mu.Unlock()
		if stuck {
			if requests != 1 || abandoned != 1 {
				t.Fatal("failed lookup lost its cleanup identity")
			}
		} else if requests != 0 || abandoned != 0 {
			t.Fatalf("timed-out lineage starved another lineage: requests=%d abandoned=%d", requests, abandoned)
		}
	}
	store.getTimeout.Store(0)
	forceCleanupScan(caller)
}

func localRequestForSequence(requests map[uint64]ownedRequest, sequence int32) (Request, bool) {
	for _, request := range requests {
		if request.GetSequenceNumber() == sequence {
			return request.Request, true
		}
	}
	return Request{}, false
}
