package gosmpp

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gomaja/go-smpp/pdu"
)

type closeCompletionRound7Store struct {
	DefaultStore
	get    func(context.Context, int32) (Request, bool)
	delete func(context.Context, Request) (bool, error)
	list   func(context.Context) []Request
	set    func(context.Context, Request) error
}

func (s *closeCompletionRound7Store) Get(ctx context.Context, sequence int32) (Request, bool) {
	if s.get != nil {
		return s.get(ctx, sequence)
	}
	return s.DefaultStore.Get(ctx, sequence)
}
func (s *closeCompletionRound7Store) Delete(ctx context.Context, request Request) (bool, error) {
	if s.delete != nil {
		return s.delete(ctx, request)
	}
	return s.DefaultStore.Delete(ctx, request)
}
func (s *closeCompletionRound7Store) List(ctx context.Context) []Request {
	if s.list != nil {
		return s.list(ctx)
	}
	return s.DefaultStore.List(ctx)
}
func (s *closeCompletionRound7Store) Set(ctx context.Context, request Request) error {
	if s.set != nil {
		return s.set(ctx, request)
	}
	return s.DefaultStore.Set(ctx, request)
}

func TestWindowCloseCompletionRound7AbandonmentBeforeFlushClaim(t *testing.T) {
	conn, cleanup := newPipePair()
	defer cleanup()
	store := &closeCompletionRound7Store{DefaultStore: NewDefaultStore()}
	settings := lifecycleSettings()
	settings.StoreAccessTimeOut = 50 * time.Millisecond
	var closed, expected, expired atomic.Int32
	errorsSeen := make(chan error, 8)
	settings.OnClosePduRequest = func(pdu.PDU) { closed.Add(1) }
	settings.OnExpectedPduResponse = func(Response) { expected.Add(1) }
	settings.OnExpiredPduRequest = func(pdu.PDU) bool { expired.Add(1); return false }
	settings.OnReceivingError = func(err error) { errorsSeen <- err }
	v := newTransceivable(conn, settings, store)
	request := pdu.NewSubmitSM()
	if _, err := v.out.write(request); err != nil {
		t.Fatal(err)
	}
	getEntered, getRelease := make(chan struct{}), make(chan struct{})
	deleteEntered, deleteRelease := make(chan struct{}), make(chan struct{})
	var getReleased sync.Once
	releaseGet := func() { getReleased.Do(func() { close(getRelease) }) }
	var release sync.Once
	releaseDelete := func() { release.Do(func() { close(deleteRelease) }) }
	store.get = func(ctx context.Context, _ int32) (Request, bool) {
		close(getEntered)
		<-ctx.Done()
		<-getRelease
		return Request{}, false
	}
	store.delete = func(ctx context.Context, row Request) (bool, error) {
		removed, err := store.DefaultStore.Delete(ctx, row)
		close(deleteEntered)
		<-deleteRelease
		return removed, err
	}
	responseDone := make(chan struct{})
	go func() {
		defer close(responseDone)
		v.in.completeResponse(request.GetResponse(), request.GetSequenceNumber(), nil)
	}()
	closeDone := make(chan struct{})
	t.Cleanup(func() { releaseGet(); releaseDelete(); lifecycleWait(t, responseDone); _ = v.Close() })
	lifecycleWait(t, getEntered)
	go func() { defer close(closeDone); _ = v.Close() }()
	lifecycleWait(t, deleteEntered)
	releaseGet()
	// The flush has a row snapshot and a pending successful Delete. The
	// response's failed lookup must suppress completion before the local claim.
	lifecycleWait(t, responseDone)
	select {
	case err := <-errorsSeen:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("response lookup diagnostic: %v", err)
		}
	default:
		t.Fatal("response lookup timeout was not reported")
	}
	releaseDelete()
	lifecycleWait(t, closeDone)
	if closed.Load() != 0 || expected.Load() != 0 || expired.Load() != 0 {
		t.Fatalf("abandoned response retried as a completion: close=%d response=%d expiry=%d", closed.Load(), expected.Load(), expired.Load())
	}
}

func TestWindowCloseCompletionRound7HealthyRollbackDoesNotConsumeUncertainty(t *testing.T) {
	conn, cleanup := newPipePair()
	defer cleanup()
	const canceled = maxUnresolvedRequests
	store := &closeCompletionRound7Store{DefaultStore: NewDefaultStore()}
	arrivals := make(chan Request, canceled+2)
	rollbacks := make(chan struct{}, canceled+1)
	release := make(chan struct{})
	var released sync.Once
	releaseDeletes := func() { released.Do(func() { close(release) }) }
	var gate atomic.Bool
	store.set = func(ctx context.Context, row Request) error {
		err := store.DefaultStore.Set(ctx, row)
		arrivals <- row
		return err
	}
	store.delete = func(ctx context.Context, row Request) (bool, error) {
		if gate.Load() && row.WrittenAt.IsZero() {
			rollbacks <- struct{}{}
			<-release
		}
		return store.DefaultStore.Delete(ctx, row)
	}
	settings := lifecycleSettings()
	settings.MaxWindowSize = 1
	settings.StoreAccessTimeOut = 5 * time.Second
	v := newTransceivable(conn, settings, store)
	// Keep the single transport permit occupied. The canceled wave has healthy
	// reservations but cannot enter transport before cancellation.
	first := pdu.NewSubmitSM()
	if err := v.Submit(first); err != nil {
		t.Fatal(err)
	}
	<-arrivals
	batch, cancelBatch := context.WithCancel(context.Background())
	results := make(chan error, canceled)
	var producers sync.WaitGroup
	for range canceled {
		producers.Add(1)
		go func() { defer producers.Done(); results <- v.SubmitContext(batch, pdu.NewSubmitSM()) }()
	}
	t.Cleanup(func() { cancelBatch(); releaseDeletes(); producers.Wait(); _ = v.Close() })
	for range canceled {
		select {
		case <-arrivals:
		case <-time.After(3 * time.Second):
			t.Fatal("canceled producer did not reserve")
		}
	}
	gate.Store(true)
	cancelBatch()
	for range canceled {
		lifecycleWait(t, rollbacks)
	}
	v.out.lineage.mu.Lock()
	unresolved := v.out.lineage.unresolved
	v.out.lineage.mu.Unlock()
	// A successful Set with its normal rollback still in flight is not an
	// ambiguous store operation, regardless of the number of canceled callers.
	if unresolved != 0 {
		t.Errorf("healthy pending rollback consumed uncertainty budget: %d", unresolved)
	}
	healthyResult := make(chan error, 1)
	go func() { healthyResult <- v.Submit(pdu.NewSubmitSM()) }()
	select {
	case err := <-healthyResult:
		t.Fatalf("healthy producer rejected during successful rollback wave: %v", err)
	case <-arrivals:
	case <-time.After(3 * time.Second):
		t.Fatal("healthy producer neither reserved nor returned")
	}
	releaseDeletes()
	producers.Wait()
	for range canceled {
		if err := <-results; !errors.Is(err, context.Canceled) {
			t.Errorf("canceled admission: %v", err)
		}
	}
	// Write and settle the occupying request so the fresh producer can acquire
	// capacity and enter the queue without starting an unrelated ticker.
	if _, err := v.out.writeQueued(<-v.out.input); err != nil {
		t.Fatal(err)
	}
	v.in.completeResponse(first.GetResponse(), first.GetSequenceNumber(), nil)
	select {
	case err := <-healthyResult:
		if err != nil {
			t.Fatalf("healthy admission: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("healthy producer did not finish after permit release")
	}
}

func TestWindowCloseCompletionRound7StopsExpiryBeforeNextDelete(t *testing.T) {
	conn, cleanup := newPipePair()
	defer cleanup()
	store := &closeCompletionRound7Store{DefaultStore: NewDefaultStore()}
	settings := lifecycleSettings()
	settings.MaxWindowSize = 8
	settings.StoreAccessTimeOut = 40 * time.Millisecond
	settings.PduExpireTimeOut = time.Second
	settings.ExpireCheckTimer = time.Millisecond
	var closed atomic.Int32
	settings.OnClosePduRequest = func(pdu.PDU) { closed.Add(1) }
	v := newTransceivable(conn, settings, store)
	const requests = 6
	for range requests {
		if _, err := v.out.write(pdu.NewSubmitSM()); err != nil {
			t.Fatal(err)
		}
	}
	expiredStamp := time.Unix(1, 0)
	var lists, expiryDeletes atomic.Int32
	var repaired atomic.Bool
	firstDelete := make(chan struct{})
	release := make(chan struct{})
	var released sync.Once
	releaseDelete := func() { released.Do(func() { close(release) }) }
	store.list = func(ctx context.Context) []Request {
		if lists.Add(1) != 1 {
			return nil
		}
		rows := store.DefaultStore.List(ctx)
		for i := range rows {
			rows[i].WrittenAt = expiredStamp
		}
		return rows
	}
	store.delete = func(ctx context.Context, row Request) (bool, error) {
		if repaired.Load() {
			return store.DefaultStore.Delete(ctx, row)
		}
		if row.WrittenAt.Equal(expiredStamp) && expiryDeletes.Add(1) == 1 {
			close(firstDelete)
			<-release
		}
		<-ctx.Done()
		return false, ctx.Err()
	}
	t.Cleanup(func() { releaseDelete(); _ = v.Close() })
	v.start()
	lifecycleWait(t, firstDelete)
	closeDone := make(chan struct{})
	started := time.Now()
	go func() { defer close(closeDone); _ = v.Close() }()
	lifecycleWait(t, v.ctx.Done())
	releaseDelete()
	lifecycleWait(t, closeDone)
	if got := expiryDeletes.Load(); got != 1 {
		t.Errorf("expiry continued after shutdown: Deletes=%d, want only the already-started Delete", got)
	}
	if elapsed := time.Since(started); elapsed > 2*settings.StoreAccessTimeOut+100*time.Millisecond {
		t.Errorf("Close joined one deadline per expired row: elapsed=%v, store budget=%v", elapsed, settings.StoreAccessTimeOut)
	}
	if got := closed.Load(); got != requests {
		t.Errorf("final Close lost accepted local requests after store timeout: callbacks=%d, want %d", got, requests)
	}
	value, retained := liveRequestOwners.Load(v.out.owner)
	if !retained {
		t.Fatal("failed close cleanup lost its deferred owner identities")
	}
	owner := value.(*requestOwnerState)
	owner.mu.Lock()
	pending, written, permits := len(owner.requests), owner.written, len(owner.permits)
	owner.mu.Unlock()
	if pending != requests || written != 0 || permits != 0 {
		t.Errorf("deferred close ledger: identities=%d written=%d permits=%d, want identities=%d without capacity", pending, written, permits, requests)
	}
	// A later generation on the same store must remove these rows silently,
	// including when List still omits them. The original callbacks stay final.
	repaired.Store(true)
	conn2, cleanup2 := newPipePair()
	defer cleanup2()
	settings.ExpireCheckTimer = 0
	recovery := newTransceivable(conn2, settings, store)
	t.Cleanup(func() { _ = recovery.Close() })
	forceCleanupScan(recovery.out)
	if remaining, err := store.DefaultStore.Length(context.Background()); err != nil || remaining != 0 {
		t.Errorf("deferred close rows not recovered: remaining=%d error=%v", remaining, err)
	}
	if _, retained := liveRequestOwners.Load(v.out.owner); retained {
		t.Error("recovered close identities remained pinned in owner registry")
	}
	_ = recovery.Close()
	if got := closed.Load(); got != requests {
		t.Errorf("deferred cleanup repeated close completions: callbacks=%d, want %d", got, requests)
	}
}

func TestWindowCloseCompletionRound7AbandonmentDuringSilentCleanup(t *testing.T) {
	for _, path := range []string{"retireEnquiry", "uncertainCleanup"} {
		t.Run(path, func(t *testing.T) {
			conn, cleanup := newPipePair()
			defer cleanup()
			store := &closeCompletionRound7Store{DefaultStore: NewDefaultStore()}
			settings := lifecycleSettings()
			settings.StoreAccessTimeOut = 40 * time.Millisecond
			var closed, expected, expired atomic.Int32
			settings.OnClosePduRequest = func(pdu.PDU) { closed.Add(1) }
			settings.OnExpectedPduResponse = func(Response) { expected.Add(1) }
			settings.OnExpiredPduRequest = func(pdu.PDU) bool { expired.Add(1); return false }
			v := newTransceivable(conn, settings, store)
			request := pdu.PDU(pdu.NewSubmitSM())
			if path == "retireEnquiry" {
				request = pdu.NewEnquireLink()
			}
			if _, err := v.out.write(request); err != nil {
				t.Fatal(err)
			}
			row, ok := store.DefaultStore.Get(context.Background(), request.GetSequenceNumber())
			if !ok {
				t.Fatal("written request missing")
			}
			deleteEntered, release := make(chan struct{}), make(chan struct{})
			var released sync.Once
			releaseDelete := func() { released.Do(func() { close(release) }) }
			var gated atomic.Bool
			gated.Store(true)
			store.delete = func(ctx context.Context, r Request) (bool, error) {
				removed, err := store.DefaultStore.Delete(ctx, r)
				if gated.CompareAndSwap(true, false) {
					close(deleteEntered)
					<-release
				}
				return removed, err
			}
			cleanupDone := make(chan struct{})
			t.Cleanup(func() { releaseDelete(); lifecycleWait(t, cleanupDone); _ = v.Close() })
			if path == "uncertainCleanup" {
				abandonRequest(row)
			}
			go func() {
				defer close(cleanupDone)
				if path == "retireEnquiry" {
					v.out.retireEnquiry()
					return
				}
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				gets := 0
				_ = v.out.resolveUncertainRequests(ctx, v.out.lineage, &gets)
			}()
			lifecycleWait(t, deleteEntered)
			store.get = func(ctx context.Context, _ int32) (Request, bool) { <-ctx.Done(); return Request{}, false }
			v.in.completeResponse(request.GetResponse(), request.GetSequenceNumber(), nil)
			releaseDelete()
			lifecycleWait(t, cleanupDone)
			// A late replay of the exact row must stay silent even after successful
			// cleanup removes the live abandonment marker.
			store.get = nil
			if err := store.DefaultStore.Set(context.Background(), row); err != nil {
				t.Fatal(err)
			}
			v.in.completeResponse(request.GetResponse(), request.GetSequenceNumber(), nil)
			v.expireRequests()
			_ = v.Close()
			if closed.Load() != 0 || expected.Load() != 0 || expired.Load() != 0 {
				t.Fatalf("response abandonment lost after %s: close=%d response=%d expiry=%d", path, closed.Load(), expected.Load(), expired.Load())
			}
		})
	}
}

func TestWindowCloseCompletionRound7MissingRowSettlementPreservesLaterAbandonment(t *testing.T) {
	conn, cleanup := newPipePair()
	defer cleanup()
	v := newTransceivable(conn, lifecycleSettings(), NewDefaultStore())
	t.Cleanup(func() { _ = v.Close() })
	request := v.out.newRequest(pdu.NewSubmitSM())
	if err := v.out.rememberRequest(request); err != nil {
		t.Fatal(err)
	}
	if !v.out.beginTransmission(request) {
		t.Fatal("request did not begin transmission")
	}
	v.out.rememberWritten(request, time.Now())
	// An absent backend row cannot settle an ongoing transport reservation.
	// A response lookup can still fail immediately after this cleanup attempt.
	if settleRequest(request, false) {
		t.Fatal("missing backend row claimed a transmitting request")
	}
	abandonRequest(request)
	owner := v.out.ownerState
	owner.mu.Lock()
	retained, present := owner.requests[request.ID]
	written, permits := owner.written, len(owner.permits)
	owner.mu.Unlock()
	marker, marked := v.out.lineage.abandoned.Load(request.ID)
	v.out.lineage.mu.Lock()
	unresolved := v.out.lineage.unresolved
	v.out.lineage.mu.Unlock()
	if !present || !retained.uncertain || retained.transmitting || !marked || !sameReservation(marker.(Request), request) || unresolved != 1 || written != 0 || permits != 0 {
		t.Fatalf("later abandonment lost cleanup identity: present=%v uncertain=%v transmitting=%v marker=%v unresolved=%d written=%d permits=%d", present, retained.uncertain, retained.transmitting, marked, unresolved, written, permits)
	}
	// Once abandonment has settled transport, absence cleanup removes the
	// reservation and its enumeration marker in the same local settlement.
	if settleRequest(request, false) {
		t.Fatal("missing backend row claimed completion of an abandoned request")
	}
	owner.mu.Lock()
	_, present = owner.requests[request.ID]
	owner.mu.Unlock()
	_, marked = v.out.lineage.abandoned.Load(request.ID)
	v.out.lineage.mu.Lock()
	unresolved = v.out.lineage.unresolved
	v.out.lineage.mu.Unlock()
	if present || marked || unresolved != 0 {
		t.Fatalf("settled cleanup identity retained accounting: present=%v marker=%v unresolved=%d", present, marked, unresolved)
	}
}

func TestWindowCloseCompletionRound7ClaimedCloseRetainsOnlySilentCleanup(t *testing.T) {
	conn, cleanup := newPipePair()
	defer cleanup()
	v := newTransceivable(conn, lifecycleSettings(), NewDefaultStore())
	t.Cleanup(func() { _ = v.Close() })
	request := v.out.newRequest(pdu.NewSubmitSM())
	if err := v.out.rememberRequest(request); err != nil {
		t.Fatal(err)
	}
	if !v.out.beginTransmission(request) {
		t.Fatal("request did not begin transmission")
	}
	v.out.rememberWritten(request, time.Now())
	if !claimCloseRequest(request) {
		t.Fatal("final close did not claim transmitting request")
	}
	if claimCloseRequest(request) {
		t.Fatal("final close claimed the same completion twice")
	}
	if settleRequest(request, true) {
		t.Fatal("deferred backend cleanup claimed an already completed request")
	}
	owner := v.out.ownerState
	owner.mu.Lock()
	remaining, written, permits := len(owner.requests), owner.written, len(owner.permits)
	owner.mu.Unlock()
	_, marked := v.out.lineage.abandoned.Load(request.ID)
	if remaining != 0 || written != 0 || permits != 0 || marked {
		t.Fatalf("successful silent cleanup retained local obligations: identities=%d written=%d permits=%d marker=%v", remaining, written, permits, marked)
	}
}

func TestWindowCloseCompletionRound7AbandonmentSurvivesTombstoneEviction(t *testing.T) {
	store := NewDefaultStore()
	settings := lifecycleSettings()
	var callbacks atomic.Int32
	settings.OnClosePduRequest = func(pdu.PDU) { callbacks.Add(1) }
	out := newTransmittable(nil, settings, store)
	out.trackRequestOwner()
	defer liveRequestOwners.Delete(out.owner)
	first := out.newRequest(pdu.NewSubmitSM())
	if err := out.rememberRequest(first); err != nil {
		t.Fatal(err)
	}
	if err := store.Set(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	abandonRequest(first)
	for range maxUnresolvedRequests {
		next := out.newRequest(pdu.NewSubmitSM())
		if err := out.rememberRequest(next); err != nil {
			t.Fatal(err)
		}
		abandonRequest(next)
		settleRequest(next, false)
	}
	if !out.lineage.discarded(first) {
		t.Fatal("setup lost abandonment marker")
	}
	out.lineage.mu.Lock()
	_, tombstone := out.lineage.rejected[first.ID]
	out.lineage.mu.Unlock()
	if tombstone {
		t.Fatal("setup did not evict old tombstone")
	}
	out.flushRequests()
	if n := callbacks.Load(); n != 0 {
		t.Fatalf("abandoned request received %d close callbacks after tombstone eviction", n)
	}
}

func TestWindowRecoveryRound7OwnLookupCannotStarveListedRows(t *testing.T) {
	store := &recoveryProgressRound7Store{DefaultStore: NewDefaultStore()}
	settings := lifecycleSettings()
	settings.StoreAccessTimeOut = 10 * time.Millisecond
	old := recoveryProgressRound7Owner(t, store, settings)
	rows := recoveryProgressRound7Written(t, old, store, 2)
	abandonRequest(rows[0])
	store.blockedGet.Store(rows[0].GetSequenceNumber())
	old.retireRequestOwner()
	next := recoveryProgressRound7Owner(t, store, settings, old.lineage)
	for range 5 {
		forceCleanupScan(next)
	}
	if _, exists := store.DefaultStore.Get(context.Background(), rows[1].GetSequenceNumber()); exists {
		t.Fatal("healthy listed row remains stranded behind the single blocked own-lineage Get")
	}
}

func TestWindowRecoveryRound7OwnGetCapCannotStarveForeignIdentity(t *testing.T) {
	store := &closeCompletionRound7Store{DefaultStore: NewDefaultStore()}
	settings := lifecycleSettings()
	settings.StoreAccessTimeOut = time.Second
	foreign := recoveryProgressRound7Owner(t, store, settings)
	recoveryProgressRound7Uncertain(t, foreign, 1)
	foreign.retireRequestOwner()
	foreign.lineage.markLineageClosed()
	own := recoveryProgressRound7Owner(t, store, settings)
	ownRows := recoveryProgressRound7Uncertain(t, own, maxCleanupGets)
	for _, row := range ownRows {
		if err := store.DefaultStore.Set(context.Background(), row); err != nil {
			t.Fatal(err)
		}
	}
	store.delete = func(context.Context, Request) (bool, error) { return false, errors.New("per-row deletion unavailable") }
	for range 5 {
		forceCleanupScan(own)
	}
	if retained, _ := cleanupOutstanding(foreign); retained != 0 {
		t.Fatalf("foreign absent identity stranded by own lookup cap: retained=%d", retained)
	}
}

func TestWindowRecoveryRound7ListReceivesRemainingOperationBudget(t *testing.T) {
	store := &closeCompletionRound7Store{DefaultStore: NewDefaultStore()}
	settings := lifecycleSettings()
	settings.StoreAccessTimeOut = time.Second
	old := recoveryProgressRound7Owner(t, store, settings)
	rows := recoveryProgressRound7Written(t, old, store, 1)
	old.retireRequestOwner()
	// A healthy backend can require half its configured operation timeout.
	// It refuses a budget too short to start; no elapsed sleep orders this test.
	store.list = func(ctx context.Context) []Request {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) < settings.StoreAccessTimeOut/2 {
			return nil
		}
		return store.DefaultStore.List(ctx)
	}
	live := recoveryProgressRound7Owner(t, store, settings, old.lineage)
	for range 3 {
		forceCleanupScan(live)
	}
	if _, exists := store.DefaultStore.Get(context.Background(), rows[0].GetSequenceNumber()); exists {
		t.Fatal("healthy List cannot start within an artificially shortened operation deadline")
	}
}

func TestWindowRecoveryRound7EveryPhaseReceivesFullBudget(t *testing.T) {
	store := &closeCompletionRound7Store{DefaultStore: NewDefaultStore()}
	settings := lifecycleSettings()
	settings.StoreAccessTimeOut = 90 * time.Millisecond
	foreign := recoveryProgressRound7Owner(t, store, settings)
	foreignRows := recoveryProgressRound7Uncertain(t, foreign, 1)
	if err := store.DefaultStore.Set(context.Background(), foreignRows[0]); err != nil {
		t.Fatal(err)
	}
	foreign.retireRequestOwner()
	foreign.lineage.markLineageClosed()
	own := recoveryProgressRound7Owner(t, store, settings)
	recoveryProgressRound7Uncertain(t, own, 1)
	// This position can follow a List timeout. Advancing from the last attempted
	// phase repeatedly gives the own lookup only the foreign lookup's leftovers.
	own.lineage.cleanupPhase = 2
	store.list = func(ctx context.Context) []Request { <-ctx.Done(); return nil }
	store.get = func(ctx context.Context, sequence int32) (Request, bool) {
		if sequence == foreignRows[0].GetSequenceNumber() {
			// Model backend latency through a deadline, without using elapsed
			// sleeps to infer that another goroutine has reached an operation.
			latency, cancel := context.WithTimeout(ctx, 2*settings.StoreAccessTimeOut/3)
			<-latency.Done()
			cancel()
			return store.DefaultStore.Get(ctx, sequence)
		}
		deadline, _ := ctx.Deadline()
		if time.Until(deadline) < settings.StoreAccessTimeOut/2 {
			<-ctx.Done()
			return Request{}, false
		}
		return store.DefaultStore.Get(ctx, sequence)
	}
	store.delete = func(context.Context, Request) (bool, error) {
		return false, errors.New("foreign row deletion unavailable")
	}
	for range 6 {
		forceCleanupScan(own)
	}
	if retained, _ := cleanupOutstanding(own); retained != 0 {
		t.Fatalf("healthy absent own Get never receives its configured budget: retained=%d", retained)
	}
}
