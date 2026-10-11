package gosmpp

import (
	"cmp"
	"context"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gomaja/go-smpp/pdu"
)

// Stable ordering exposes scans that restart at the same unavailable row.
type recoveryProgressRound7Store struct {
	DefaultStore
	blockedThrough atomic.Int32
	listTimeout    atomic.Bool
	getTimeout     atomic.Bool
	blockedGet     atomic.Int32
	gets           atomic.Int32
	firstGet       atomic.Int32
	firstGetAt     atomic.Int64
}

func (s *recoveryProgressRound7Store) List(ctx context.Context) []Request {
	if s.listTimeout.Load() {
		<-ctx.Done()
		return nil
	}
	rows := s.DefaultStore.List(ctx)
	slices.SortFunc(rows, func(a, b Request) int { return cmp.Compare(a.GetSequenceNumber(), b.GetSequenceNumber()) })
	return rows
}

func (s *recoveryProgressRound7Store) Delete(ctx context.Context, request Request) (bool, error) {
	if request.GetSequenceNumber() <= s.blockedThrough.Load() {
		<-ctx.Done()
		return false, ctx.Err()
	}
	return s.DefaultStore.Delete(ctx, request)
}

func (s *recoveryProgressRound7Store) Get(ctx context.Context, sequence int32) (Request, bool) {
	s.gets.Add(1)
	if s.firstGet.CompareAndSwap(0, sequence) {
		s.firstGetAt.Store(time.Now().UnixNano())
	}
	if s.getTimeout.Load() || s.blockedGet.Load() == sequence {
		<-ctx.Done()
		return Request{}, false
	}
	return s.DefaultStore.Get(ctx, sequence)
}

func recoveryProgressRound7Owner(t *testing.T, store RequestStore, settings Settings, lineage ...*requestLineage) *transmittable {
	t.Helper()
	out := newTransmittable(nil, settings, store)
	if len(lineage) != 0 {
		out.lineage = lineage[0]
	}
	out.trackRequestOwner()
	out.nextReclaim = time.Now().Add(time.Hour)
	t.Cleanup(func() { out.reclaimWG.Wait(); liveRequestOwners.Delete(out.owner) })
	return out
}

func recoveryProgressRound7Written(t *testing.T, out *transmittable, store RequestStore, count int) []Request {
	t.Helper()
	rows := make([]Request, 0, count)
	for range count {
		request := out.newRequest(pdu.NewSubmitSM())
		if err := out.rememberRequest(request); err != nil {
			t.Fatal(err)
		}
		if err := store.Set(context.Background(), request); err != nil {
			t.Fatal(err)
		}
		if !out.beginTransmission(request) {
			t.Fatal("local transmission was not admitted")
		}
		at := time.Now()
		out.rememberWritten(request, at)
		if marked, err := store.MarkWritten(context.Background(), request, at); !marked || err != nil {
			t.Fatalf("mark written: marked=%t error=%v", marked, err)
		}
		rows = append(rows, request)
	}
	slices.SortFunc(rows, func(a, b Request) int { return cmp.Compare(a.GetSequenceNumber(), b.GetSequenceNumber()) })
	return rows
}

func recoveryProgressRound7Uncertain(t *testing.T, out *transmittable, count int) []Request {
	t.Helper()
	rows := make([]Request, 0, count)
	for range count {
		request := out.newRequest(pdu.NewSubmitSM())
		if err := out.rememberRequest(request); err != nil {
			t.Fatal(err)
		}
		abandonRequest(request)
		rows = append(rows, request)
	}
	return rows
}

func TestWindowRecoveryRound7StableListDoesNotStarveDeadRows(t *testing.T) {
	store := &recoveryProgressRound7Store{DefaultStore: NewDefaultStore()}
	settings := lifecycleSettings()
	settings.StoreAccessTimeOut = 20 * time.Millisecond
	var completed atomic.Int32
	settings.OnClosePduRequest = func(pdu.PDU) { completed.Add(1) }
	dead := recoveryProgressRound7Owner(t, store, settings)
	rows := recoveryProgressRound7Written(t, dead, store, 20)
	dead.retireRequestOwner()
	store.blockedThrough.Store(rows[0].GetSequenceNumber())
	next := recoveryProgressRound7Owner(t, store, settings, dead.lineage)
	for range 3 {
		forceCleanupScan(next)
	}
	if left, _ := store.Length(context.Background()); left != 1 {
		t.Fatalf("blocked first List row starved healthy dead rows: remaining=%d; want 1", left)
	}
	if count := completed.Load(); count < 19 || count > 20 {
		t.Fatalf("dead-generation completions=%d; want each of 19 cleaned requests once", count)
	}
}

func TestWindowRecoveryRound7StableListFreesReboundWindow(t *testing.T) {
	store := &recoveryProgressRound7Store{DefaultStore: NewDefaultStore()}
	settings := lifecycleSettings()
	settings.StoreAccessTimeOut = 20 * time.Millisecond
	dead := recoveryProgressRound7Owner(t, store, settings)
	rows := recoveryProgressRound7Written(t, dead, store, 10)
	dead.retireRequestOwner()
	store.blockedThrough.Store(rows[0].GetSequenceNumber())
	conn, _, frames := windowResponsePeer(t)
	next := newTransmittable(conn, settings, store)
	next.lineage = dead.lineage
	next.trackRequestOwner()
	t.Cleanup(func() { _ = next.close(ExplicitClosing); liveRequestOwners.Delete(next.owner) })
	for range 3 {
		forceCleanupScan(next)
	}
	if written, _ := store.WrittenLength(context.Background(), next.owner); written != 1 {
		t.Fatalf("rebound window retains %d dead-generation written requests; want 1", written)
	}
	next.start()
	request := pdu.NewSubmitSM()
	if err := next.Submit(request); err != nil {
		t.Fatal(err)
	}
	if frame := windowResponseFrame(t, frames); frame.GetSequenceNumber() != request.GetSequenceNumber() {
		t.Fatal("freed rebound capacity did not transmit the new request")
	}
}

func TestWindowRecoveryRound7ListCursorSurvivesRebind(t *testing.T) {
	store := &recoveryProgressRound7Store{DefaultStore: NewDefaultStore()}
	settings := lifecycleSettings()
	settings.StoreAccessTimeOut = 20 * time.Millisecond
	dead := recoveryProgressRound7Owner(t, store, settings)
	rows := recoveryProgressRound7Written(t, dead, store, 9)
	dead.retireRequestOwner()
	// A full wave of unavailable operations must not restart at every rebind.
	store.blockedThrough.Store(rows[7].GetSequenceNumber())
	for range 3 {
		next := recoveryProgressRound7Owner(t, store, settings, dead.lineage)
		forceCleanupScan(next)
		next.retireRequestOwner()
	}
	if _, exists := store.Get(context.Background(), rows[8].GetSequenceNumber()); exists {
		t.Fatal("rebind reset recovery progress behind eight consistently unavailable identities")
	}
}

func TestWindowRecoveryRound7CloseCompletesAllBehindBlockedRow(t *testing.T) {
	store := &recoveryProgressRound7Store{DefaultStore: NewDefaultStore()}
	settings := lifecycleSettings()
	settings.MaxWindowSize = 20
	settings.StoreAccessTimeOut = 30 * time.Millisecond
	var mu sync.Mutex
	completed := make(map[int32]int)
	settings.OnClosePduRequest = func(p pdu.PDU) { mu.Lock(); completed[p.GetSequenceNumber()]++; mu.Unlock() }
	conn, cleanup := newPipePair()
	defer cleanup()
	session, err := NewSession(lifecycleConnector{connect: func() (*Connection, error) { return conn, nil }}, settings, 0, WithRequestStore(store))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close(); liveRequestOwners.Delete(session.bound().out.owner) })
	requests := make([]pdu.PDU, 20)
	for i := range requests {
		requests[i] = pdu.NewSubmitSM()
		if err := session.Transceiver().Submit(requests[i]); err != nil {
			t.Fatal(err)
		}
	}
	windowResponseAwait(t, func() bool { n, _ := store.WrittenLength(context.Background(), 0); return n == 20 })
	rows := store.List(context.Background())
	store.blockedThrough.Store(rows[0].GetSequenceNumber())
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, request := range requests {
		if count := completed[request.GetSequenceNumber()]; count != 1 {
			t.Errorf("accepted sequence %d close completions=%d; want 1 independently of backend deletion", request.GetSequenceNumber(), count)
		}
	}
}

// All deletes stay gated until the test releases a complete wave. No elapsed
// sleep is used to infer that store work has started or that it overlaps.
type recoveryWaveRound7Store struct {
	DefaultStore
	gated   atomic.Bool
	active  atomic.Int32
	peak    atomic.Int32
	entered chan struct{}
	release chan struct{}
}

func (s *recoveryWaveRound7Store) Delete(ctx context.Context, request Request) (bool, error) {
	if s.gated.Load() {
		active := s.active.Add(1)
		defer s.active.Add(-1)
		for old := s.peak.Load(); active > old && !s.peak.CompareAndSwap(old, active); old = s.peak.Load() {
		}
		s.entered <- struct{}{}
		select {
		case <-s.release:
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	return s.DefaultStore.Delete(ctx, request)
}

func TestWindowRecoveryRound7CloseUsesBoundedParallelHealthyCleanup(t *testing.T) {
	store := &recoveryWaveRound7Store{DefaultStore: NewDefaultStore(), entered: make(chan struct{}, 60), release: make(chan struct{}, 60)}
	settings := lifecycleSettings()
	settings.MaxWindowSize = 60
	settings.StoreAccessTimeOut = time.Second
	var completed atomic.Int32
	settings.OnClosePduRequest = func(pdu.PDU) { completed.Add(1) }
	conn, cleanup := newPipePair()
	defer cleanup()
	session, err := NewSession(lifecycleConnector{connect: func() (*Connection, error) { return conn, nil }}, settings, 0, WithRequestStore(store))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		store.gated.Store(false)
		close(store.release)
		_ = session.Close()
		liveRequestOwners.Delete(session.bound().out.owner)
	})
	for range 60 {
		if err := session.Transceiver().Submit(pdu.NewSubmitSM()); err != nil {
			t.Fatal(err)
		}
	}
	windowResponseAwait(t, func() bool { n, _ := store.WrittenLength(context.Background(), 0); return n == 60 })
	store.gated.Store(true)
	done := make(chan struct{})
	go func() { _ = session.Close(); close(done) }()
	for remaining := 60; remaining > 0; {
		wave := min(8, remaining)
		for range wave {
			select {
			case <-store.entered:
			case <-done:
				t.Fatalf("Close stopped before healthy cleanup completed: callbacks=%d remaining=%d", completed.Load(), remaining)
			case <-time.After(2 * time.Second):
				t.Fatal("cleanup did not launch a complete wave of eight independent store operations")
			}
		}
		if active := store.active.Load(); remaining == 60 && active != int32(wave) {
			t.Fatalf("gated cleanup concurrency=%d; want %d", active, wave)
		}
		for range wave {
			store.release <- struct{}{}
		}
		remaining -= wave
	}
	lifecycleWait(t, done)
	if callbacks := completed.Load(); callbacks != 60 {
		t.Fatalf("healthy store close completions=%d; want all 60", callbacks)
	}
	if left, _ := store.Length(context.Background()); left != 0 || store.peak.Load() != 8 {
		t.Fatalf("healthy cleanup: remaining=%d peak concurrency=%d; want 0 and 8", left, store.peak.Load())
	}
}

func TestWindowRecoveryRound7FinalCloseResolvesThousandUncertainIdentities(t *testing.T) {
	store := &recoveryProgressRound7Store{DefaultStore: NewDefaultStore()}
	settings := lifecycleSettings()
	settings.StoreAccessTimeOut = time.Second
	var callbacks atomic.Int32
	settings.OnClosePduRequest = func(pdu.PDU) { callbacks.Add(1) }
	conn, cleanup := newPipePair()
	defer cleanup()
	out := newTransmittable(conn, settings, store)
	out.nextReclaim = time.Now().Add(time.Hour)
	recoveryProgressRound7Uncertain(t, out, 1000)
	t.Cleanup(func() { liveRequestOwners.Delete(out.owner) })
	if err := out.close(ExplicitClosing); err != nil {
		t.Fatal(err)
	}
	if callbacks.Load() != 0 {
		t.Errorf("rejected uncertain admissions received %d close completions; want 0", callbacks.Load())
	}
	if requests, abandoned := cleanupOutstanding(out); requests != 0 || abandoned != 0 {
		t.Fatalf("healthy final Close retained uncertain identities: requests=%d abandoned=%d Gets=%d; want no 256-lookup cap", requests, abandoned, store.gets.Load())
	}
}

func TestWindowRecoveryRound7IdleSessionFindsForeignCleanupWithoutOwnHint(t *testing.T) {
	store := &recoveryProgressRound7Store{DefaultStore: NewDefaultStore()}
	settings := lifecycleSettings()
	settings.StoreAccessTimeOut = 20 * time.Millisecond
	conn, cleanup := newPipePair()
	defer cleanup()
	dead := newTransmittable(conn, settings, store)
	dead.nextReclaim = time.Now().Add(time.Hour)
	recoveryProgressRound7Uncertain(t, dead, 16)
	store.getTimeout.Store(true)
	if err := dead.close(ExplicitClosing); err != nil {
		t.Fatal(err)
	}
	dead.lineage.markLineageClosed()
	t.Cleanup(func() { liveRequestOwners.Delete(dead.owner) })
	if requests, _ := cleanupOutstanding(dead); requests == 0 {
		t.Fatal("setup did not retain unavailable foreign cleanup")
	}
	store.getTimeout.Store(false)
	settings.ExpireCheckTimer = 10 * time.Millisecond
	settings.PduExpireTimeOut = 0
	conn2, cleanup2 := newPipePair()
	defer cleanup2()
	session, err := NewSession(lifecycleConnector{connect: func() (*Connection, error) { return conn2, nil }}, settings, 0, WithRequestStore(store))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	if session.lineage.orphaned.Load() {
		t.Fatal("new idle session unexpectedly has an own-lineage cleanup hint")
	}
	windowResponseAwait(t, func() bool { requests, abandoned := cleanupOutstanding(dead); return requests == 0 && abandoned == 0 })
}

func TestWindowRecoveryRound7OwnLineagePrecedesBackgroundLineages(t *testing.T) {
	store := &recoveryProgressRound7Store{DefaultStore: NewDefaultStore()}
	settings := lifecycleSettings()
	settings.StoreAccessTimeOut = time.Second
	for range 8 {
		dead := recoveryProgressRound7Owner(t, store, settings)
		recoveryProgressRound7Uncertain(t, dead, 1000)
		dead.retireRequestOwner()
		dead.lineage.markLineageClosed()
	}
	caller := recoveryProgressRound7Owner(t, store, settings)
	rows := recoveryProgressRound7Uncertain(t, caller, maxUnresolvedRequests)
	forceCleanupScan(caller)
	first := store.firstGet.Load()
	if !slices.ContainsFunc(rows, func(request Request) bool { return request.GetSequenceNumber() == first }) {
		t.Fatalf("first cleanup Get=%d belongs to a background lineage; want own-lineage identity", first)
	}
	caller.lineage.mu.Lock()
	unresolved := caller.lineage.unresolved
	caller.lineage.mu.Unlock()
	if unresolved >= maxUnresolvedRequests {
		t.Fatalf("eight closed background lineages consumed own-lineage recovery budget: unresolved=%d", unresolved)
	}
}

func TestWindowRecoveryRound7ForeignLineageCursorSurvivesRebind(t *testing.T) {
	store := &recoveryProgressRound7Store{DefaultStore: NewDefaultStore()}
	settings := lifecycleSettings()
	settings.StoreAccessTimeOut = 20 * time.Millisecond
	var foreign []*transmittable
	for range 4 {
		dead := recoveryProgressRound7Owner(t, store, settings)
		rows := recoveryProgressRound7Uncertain(t, dead, 1)
		if len(foreign) == 0 {
			store.blockedGet.Store(rows[0].GetSequenceNumber())
		}
		dead.retireRequestOwner()
		dead.lineage.markLineageClosed()
		foreign = append(foreign, dead)
	}
	caller := recoveryProgressRound7Owner(t, store, settings)
	for range 2 {
		next := recoveryProgressRound7Owner(t, store, settings, caller.lineage)
		forceCleanupScan(next)
		next.retireRequestOwner()
	}
	for i, owner := range foreign {
		requests, abandoned := cleanupOutstanding(owner)
		want := 0
		if i == 0 {
			want = 1
		}
		if requests != want || abandoned != want {
			t.Errorf("foreign lineage %d: requests=%d abandoned=%d; want %d after recovery across rebind", i, requests, abandoned, want)
		}
	}
}

func TestWindowRecoveryRound7ListTimeoutCannotStarveForeignLookup(t *testing.T) {
	store := &recoveryProgressRound7Store{DefaultStore: NewDefaultStore()}
	settings := lifecycleSettings()
	settings.StoreAccessTimeOut = 30 * time.Millisecond
	dead := recoveryProgressRound7Owner(t, store, settings)
	recoveryProgressRound7Uncertain(t, dead, 1)
	dead.retireRequestOwner()
	dead.lineage.markLineageClosed()
	caller := recoveryProgressRound7Owner(t, store, settings)
	store.listTimeout.Store(true)
	for range 3 {
		forceCleanupScan(caller)
	}
	if requests, abandoned := cleanupOutstanding(dead); requests != 0 || abandoned != 0 {
		t.Fatalf("List timeout consumed healthy foreign lookup opportunity: requests=%d abandoned=%d Gets=%d; want complete absence settlement", requests, abandoned, store.gets.Load())
	}
}

func TestWindowRecoveryRound7PhaseCursorSurvivesRebind(t *testing.T) {
	store := &recoveryProgressRound7Store{DefaultStore: NewDefaultStore()}
	settings := lifecycleSettings()
	settings.StoreAccessTimeOut = 10 * time.Millisecond
	dead := recoveryProgressRound7Owner(t, store, settings)
	rows := recoveryProgressRound7Written(t, dead, store, 2)
	abandonRequest(rows[0])
	store.blockedGet.Store(rows[0].GetSequenceNumber())
	dead.retireRequestOwner()
	first := recoveryProgressRound7Owner(t, store, settings, dead.lineage)
	forceCleanupScan(first)
	if store.firstGet.Load() != rows[0].GetSequenceNumber() {
		t.Fatal("setup did not reach the unavailable own-lineage lookup")
	}
	if _, exists := store.DefaultStore.Get(context.Background(), rows[1].GetSequenceNumber()); !exists {
		t.Fatal("setup did not leave the healthy row for the unreached List phase")
	}
	first.retireRequestOwner()
	second := recoveryProgressRound7Owner(t, store, settings, dead.lineage)
	forceCleanupScan(second)
	if _, exists := store.DefaultStore.Get(context.Background(), rows[1].GetSequenceNumber()); exists {
		t.Fatal("rebind reset the phase cursor and stranded healthy List cleanup behind the unavailable own lookup")
	}
}

func TestWindowRecoveryRound7ForeignLookupQuotaPreservesOwnProgress(t *testing.T) {
	store := &recoveryProgressRound7Store{DefaultStore: NewDefaultStore()}
	settings := lifecycleSettings()
	settings.StoreAccessTimeOut = 10 * time.Millisecond
	foreign := recoveryProgressRound7Owner(t, store, settings)
	recoveryProgressRound7Uncertain(t, foreign, maxCleanupGets)
	foreign.retireRequestOwner()
	foreign.lineage.markLineageClosed()
	caller := recoveryProgressRound7Owner(t, store, settings)
	// Two List timeouts rotate the starting phase through own, List and then
	// foreign work. Seed own work afterward so setup depends only on deadlines,
	// not on how many healthy lookups can finish within a short elapsed time.
	store.listTimeout.Store(true)
	for range 2 {
		forceCleanupScan(caller)
	}
	if calls := store.gets.Load(); calls != 0 {
		t.Fatalf("setup reached foreign lookups before their starting phase: Gets=%d", calls)
	}
	recoveryProgressRound7Uncertain(t, caller, maxCleanupGets/2)
	caller.retireRequestOwner()
	store.listTimeout.Store(false)
	// The assertion is about the count budget, not backend speed. Give the
	// healthy continuation ample time to exercise both sides of that count.
	settings.StoreAccessTimeOut = time.Second
	next := recoveryProgressRound7Owner(t, store, settings, caller.lineage)
	forceCleanupScan(next)
	if requests, _ := cleanupOutstanding(caller); requests != 0 {
		t.Fatalf("foreign continuation consumed the own-lineage lookup quota: own identities=%d", requests)
	}
	if requests, _ := cleanupOutstanding(foreign); requests != maxCleanupGets/2 {
		t.Fatalf("foreign continuation retained %d identities; want half the shared count budget", requests)
	}
}

// Cold scans include own-lineage snapshot construction in the measured time.
// The backend budget is deliberately short, but scheduling misses are reported
// as a metric instead of turning machine speed into a correctness assertion.
func BenchmarkWindowRecoveryRound7ColdOwnLineageProgress(b *testing.B) {
	var firstGetDelay int64
	var misses int
	for range b.N {
		b.StopTimer()
		store := &recoveryProgressRound7Store{DefaultStore: NewDefaultStore()}
		settings := lifecycleSettings()
		settings.StoreAccessTimeOut = 2 * time.Millisecond
		var owners []*transmittable
		newOwner := func(count int) *transmittable {
			out := newTransmittable(nil, settings, store)
			out.trackRequestOwner()
			owners = append(owners, out)
			for range count {
				request := out.newRequest(pdu.NewSubmitSM())
				if err := out.rememberRequest(request); err != nil {
					b.Fatal(err)
				}
				abandonRequest(request)
			}
			return out
		}
		for range 8 {
			dead := newOwner(1000)
			dead.retireRequestOwner()
			dead.lineage.markLineageClosed()
		}
		caller := newOwner(maxUnresolvedRequests)
		own := make(map[int32]bool, maxUnresolvedRequests)
		caller.ownerState.mu.Lock()
		for _, request := range caller.ownerState.requests {
			own[request.GetSequenceNumber()] = true
		}
		caller.ownerState.mu.Unlock()
		b.StartTimer()
		started := time.Now()
		forceCleanupScan(caller)
		b.StopTimer()
		if first := store.firstGet.Load(); first == 0 {
			misses++
		} else if !own[first] {
			b.Errorf("cold scan first Get=%d belongs to a closed background lineage", first)
		} else {
			firstGetDelay += store.firstGetAt.Load() - started.UnixNano()
		}
		for _, owner := range owners {
			liveRequestOwners.Delete(owner.owner)
		}
	}
	b.ReportMetric(float64(misses)/float64(b.N), "no_get/op")
	if observed := b.N - misses; observed != 0 {
		b.ReportMetric(float64(firstGetDelay)/float64(observed), "first_get_ns/op")
	}
}

func TestWindowRecoveryRound7RejectedTombstonesStayBounded(t *testing.T) {
	lineage := &requestLineage{id: 1}
	requests := make([]Request, 3*maxUnresolvedRequests)
	for i := range requests {
		requests[i] = Request{PDU: pdu.NewSubmitSM(), ID: uint64(i + 1), Lineage: lineage.id}
		lineage.rememberDiscarded(requests[i])
	}
	if len(lineage.rejected) != maxUnresolvedRequests || len(lineage.rejections) != maxUnresolvedRequests {
		t.Fatalf("tombstone retention exceeded bound: map=%d ring=%d", len(lineage.rejected), len(lineage.rejections))
	}
	for i, request := range requests {
		if got, want := lineage.discarded(request), i >= len(requests)-maxUnresolvedRequests; got != want {
			t.Fatalf("tombstone identity %d retained=%t; want %t", request.ID, got, want)
		}
	}
}
