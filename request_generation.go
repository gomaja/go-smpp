package gosmpp

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/binary"
	"io"
	"os"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// Owner records contain reservation metadata, never Sessions or callbacks. A
// dead record remains only until its unresolved reservations are accounted for.
var liveRequestOwners sync.Map

// One random starting point keeps identities from independent processes and
// previous process lifetimes distinct without entropy work on the write path.
var processRequestPrefix = requestProcessPrefix(rand.Reader, time.Now().UnixNano(), uint64(os.Getpid()))

func init() {
	connectionGeneration.Store(processRequestPrefix)
	requestIdentity.Store(processRequestPrefix)
}

func requestProcessPrefix(entropy io.Reader, now int64, pid uint64) uint64 {
	var bytes [8]byte
	_, err := io.ReadFull(entropy, bytes[:])
	prefix := binary.BigEndian.Uint64(bytes[:])
	if err != nil || prefix == 0 {
		prefix = uint64(now) ^ pid*0x9e3779b97f4a7c15
		if prefix == 0 {
			prefix = 1 << 63
		}
	}
	return prefix
}

// Bound identities awaiting cleanup after ambiguous remote operations, and the
// small tombstones retained for late commits. Healthy pending work is separate.
const maxUnresolvedRequests = 1024

type requestLineage struct {
	id             uint64
	managed        bool
	closed         atomic.Bool
	orphaned       atomic.Bool
	orphans        atomic.Int64
	repairs        atomic.Int64
	abandoned      sync.Map
	mu             sync.Mutex
	unresolved     int
	rejected       map[uint64]int32
	rejections     []requestTombstone
	rejectNext     int
	cleanupID      uint64
	cleanupRow     uint64
	cleanupLineage uint64
	cleanupPhase   int
}

type requestTombstone struct {
	id       uint64
	sequence int32
}

// A remote Set may commit after an absent Get. Keep the most recent rejected
// identities without retaining their PDUs or counting healthy pending work.
func (l *requestLineage) rememberDiscarded(request Request) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.rejected == nil {
		l.rejected = make(map[uint64]int32)
	}
	if _, exists := l.rejected[request.ID]; exists {
		return
	}
	entry := requestTombstone{request.ID, request.GetSequenceNumber()}
	if len(l.rejections) < maxUnresolvedRequests {
		l.rejections = append(l.rejections, entry)
	} else {
		delete(l.rejected, l.rejections[l.rejectNext].id)
		l.rejections[l.rejectNext] = entry
		l.rejectNext = (l.rejectNext + 1) % maxUnresolvedRequests
	}
	l.rejected[entry.id] = entry.sequence
}

func (l *requestLineage) discarded(request Request) bool {
	if value, exists := l.abandoned.Load(request.ID); exists {
		original := value.(Request)
		if sameReservation(original, request) && original.GetSequenceNumber() == request.GetSequenceNumber() {
			return true
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	sequence, exists := l.rejected[request.ID]
	return exists && sequence == request.GetSequenceNumber() && request.Lineage == l.id
}

func (l *requestLineage) markLineageClosed() { l.closed.Store(true) }

type ownedRequest struct {
	Request
	transmitting bool
	uncertain    bool
	completed    bool // Close delivered the callback; only backend cleanup remains.
}

type requestOwnerState struct {
	mu              sync.Mutex
	lineage         *requestLineage
	store           RequestStore // Retained only while this owner's cleanup identities remain.
	alive           bool
	requests        map[uint64]ownedRequest
	written         int
	pendingMetadata map[uint64]struct{}
	permits         chan struct{}
	admitted        map[uint64]struct{}
}

func (t *transmittable) trackRequestOwner() {
	t.ownerOnce.Do(func() {
		limit := 0
		if t.settings.WindowedRequestTracking != nil {
			limit = int(t.settings.MaxWindowSize)
		}
		t.ownerState = &requestOwnerState{lineage: t.lineage, store: t.requestStore, alive: true, requests: make(map[uint64]ownedRequest), permits: make(chan struct{}, limit), admitted: make(map[uint64]struct{})}
		liveRequestOwners.Store(t.owner, t.ownerState)
	})
}
func requestOwnerAlive(owner uint64) bool {
	value, ok := liveRequestOwners.Load(owner)
	if !ok {
		return false
	}
	state := value.(*requestOwnerState)
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.alive
}
func (t *transmittable) rememberRequest(request Request) error {
	t.trackRequestOwner()
	t.lineage.mu.Lock()
	if t.lineage.unresolved >= maxUnresolvedRequests {
		t.lineage.mu.Unlock()
		return ErrRequestCleanupFull
	}
	t.lineage.mu.Unlock()
	s := t.ownerState
	s.mu.Lock()
	s.requests[request.ID] = ownedRequest{Request: request}
	s.mu.Unlock()
	return nil
}

func requestTransmitting(request Request) bool {
	if request.Owner == 0 {
		return true // Legacy entries were installed by a caller after writing.
	}
	value, ok := liveRequestOwners.Load(request.Owner)
	if !ok {
		return false
	}
	s := value.(*requestOwnerState)
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.requests[request.ID]
	return ok && current.GetSequenceNumber() == request.GetSequenceNumber() && current.transmitting
}

func (t *transmittable) beginTransmission(request Request) bool {
	s := t.ownerState
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.requests[request.ID]
	if !ok {
		return false
	}
	current.transmitting = true
	s.requests[request.ID] = current
	return true
}

func localRequest(owner uint64, sequence int32) (Request, bool) {
	value, ok := liveRequestOwners.Load(owner)
	if !ok {
		return Request{}, false
	}
	s := value.(*requestOwnerState)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, request := range s.requests {
		if request.GetSequenceNumber() == sequence && request.transmitting {
			return request.Request, true
		}
	}
	return Request{}, false
}

// A response has settled local capacity even when its backend removal failed.
// Retain only the exact cleanup identity, and never complete it a second time.
func abandonRequest(request Request) {
	value, ok := liveRequestOwners.Load(request.Owner)
	if !ok {
		return
	}
	s := value.(*requestOwnerState)
	s.mu.Lock()
	current, ok := s.requests[request.ID]
	if !ok {
		s.mu.Unlock()
		return
	}
	s.lineage.rememberDiscarded(current.Request)
	s.lineage.mu.Lock()
	full := !current.uncertain && s.lineage.unresolved >= maxUnresolvedRequests
	if !current.uncertain && !full {
		s.lineage.unresolved++
		current.uncertain = true
	}
	s.lineage.mu.Unlock()
	if full {
		// Concurrent failing admissions may all pass the earlier cap check.
		// Their bounded tombstones still suppress late commits; do not retain
		// another full cleanup identity beyond the uncertain-work limit.
		s.mu.Unlock()
		s.lineage.orphaned.Store(true)
		forgetRequest(request)
		return
	}
	defer s.mu.Unlock()
	s.lineage.abandoned.Store(request.ID, current.Request)
	s.lineage.orphaned.Store(true)
	s.releasePermit(request.ID)
	if countsInWindow(current.Request) {
		s.written--
	}
	current.WrittenAt = time.Time{}
	current.transmitting = false
	s.requests[request.ID] = current
	if _, pending := s.pendingMetadata[request.ID]; pending {
		delete(s.pendingMetadata, request.ID)
		s.lineage.repairs.Add(-1)
	}
}
func (t *transmittable) rememberWritten(request Request, at time.Time) {
	s := t.ownerState
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.requests[request.ID]
	if !ok || !current.transmitting || !current.WrittenAt.IsZero() {
		return
	}
	current.WrittenAt = at
	s.requests[request.ID] = current
	if countsInWindow(current.Request) {
		s.written++
	}
}
func (t *transmittable) writtenCount() int {
	value, ok := liveRequestOwners.Load(t.owner)
	if !ok {
		return 0
	}
	s := value.(*requestOwnerState)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.written
}
func requestWrittenTime(request Request) time.Time {
	value, ok := liveRequestOwners.Load(request.Owner)
	if !ok {
		return time.Time{}
	}
	s := value.(*requestOwnerState)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests[request.ID].WrittenAt
}
func forgetRequest(request Request) bool {
	return settleRequest(request, true)
}

// Missing rows settle only non-transmitting identities. Checking and removing
// under the owner lock also keeps a concurrent abandonment marker enumerable.
func settleRequest(request Request, removed bool) bool {
	value, ok := liveRequestOwners.Load(request.Owner)
	if !ok {
		return removed && request.Owner == 0
	}
	s := value.(*requestOwnerState)
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.requests[request.ID]
	if !ok || (!removed && current.transmitting) {
		return false
	}
	delete(s.requests, request.ID)
	s.lineage.abandoned.Delete(request.ID)
	if current.uncertain {
		s.lineage.mu.Lock()
		s.lineage.unresolved--
		s.lineage.mu.Unlock()
	}
	s.releasePermit(request.ID)
	if _, pending := s.pendingMetadata[request.ID]; pending {
		delete(s.pendingMetadata, request.ID)
		s.lineage.repairs.Add(-1)
	}
	if countsInWindow(current.Request) {
		s.written--
	}
	if !s.alive {
		remaining := s.lineage.orphans.Add(-1)
		if remaining == 0 && s.lineage.closed.Load() {
			// A finally closed lineage cannot admit new work. Its last settled
			// identity also settles any abandoned markers retained for cleanup.
			s.lineage.abandoned.Clear()
			s.lineage.orphaned.Store(false)
		}
		if len(s.requests) == 0 {
			liveRequestOwners.Delete(request.Owner)
		}
	}
	// Abandoned identities have already settled local completion. Their small
	// replay tombstones may have been evicted while backend cleanup was pending.
	return !current.completed && !current.uncertain
}

// Final close owns completion independently of backend availability. Keep the
// exact identity for silent recovery without charging the failed-admission cap.
func claimCloseRequest(request Request) bool {
	value, ok := liveRequestOwners.Load(request.Owner)
	if !ok {
		return false
	}
	s := value.(*requestOwnerState)
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.requests[request.ID]
	if !ok || !current.transmitting || current.completed {
		return false
	}
	current.completed = true
	current.transmitting = false
	s.releasePermit(request.ID)
	if countsInWindow(current.Request) {
		s.written--
	}
	current.WrittenAt = time.Time{}
	s.requests[request.ID] = current
	if _, pending := s.pendingMetadata[request.ID]; pending {
		delete(s.pendingMetadata, request.ID)
		s.lineage.repairs.Add(-1)
	}
	s.lineage.abandoned.Store(request.ID, current.Request)
	s.lineage.orphaned.Store(true)
	return true
}

func (t *transmittable) localRequests() []Request {
	if t.ownerState == nil {
		return nil
	}
	s := t.ownerState
	s.mu.Lock()
	defer s.mu.Unlock()
	requests := make([]Request, 0, len(s.requests))
	for _, current := range s.requests {
		if current.transmitting {
			requests = append(requests, current.Request)
		}
	}
	return requests
}
func (t *transmittable) retireRequestOwner() {
	if t.ownerState == nil {
		return
	}
	s := t.ownerState
	s.mu.Lock()
	defer s.mu.Unlock()
	s.alive = false
	if len(s.requests) == 0 {
		liveRequestOwners.Delete(t.owner)
	} else {
		t.lineage.orphans.Add(int64(len(s.requests)))
		t.lineage.orphaned.Store(true)
	}
}
func (t *transmittable) ownsDead(request Request) bool {
	return request.Owner != 0 && request.Owner != t.owner && request.Lineage == t.lineage.id && !requestOwnerAlive(request.Owner)
}
func (t *transmittable) foreignClosed(request Request) bool {
	if request.Lineage == t.lineage.id {
		return false
	}
	value, ok := liveRequestOwners.Load(request.Owner)
	if !ok {
		return false
	}
	s := value.(*requestOwnerState)
	s.mu.Lock()
	defer s.mu.Unlock()
	known, exists := s.requests[request.ID]
	return !s.alive && s.lineage.closed.Load() && exists &&
		request.Lineage == s.lineage.id && known.GetSequenceNumber() == request.GetSequenceNumber()
}
func (t *transmittable) reportStoreError(err error) {
	if t.settings.OnReceivingError != nil {
		invokeCallback(&t.callbacks, func() { t.settings.OnReceivingError(err) })
	}
}

type completedRequestError struct{ error }

func (e *completedRequestError) Unwrap() error { return e.error }

func (t *transmittable) listRequests() []Request {
	requests, _ := t.requestSnapshot()
	return requests
}

func (t *transmittable) requestSnapshot() ([]Request, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), t.settings.StoreAccessTimeOut)
	requests := t.requestStore.List(ctx)
	if ctx.Err() != nil {
		t.lineage.orphaned.Store(true)
	}
	complete := ctx.Err() == nil
	cancel()
	return requests, complete
}
func (t *transmittable) deleteRequest(request Request) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), t.settings.StoreAccessTimeOut)
	defer cancel()
	return t.deleteRequestContext(ctx, request)
}

func (t *transmittable) deleteRequestContext(ctx context.Context, request Request) (bool, error) {
	removed, err := t.requestStore.Delete(ctx, request)
	if err == nil {
		claimed := settleRequest(request, removed)
		if removed && !claimed {
			removed = false
		}
	}
	return removed, err
}

const cleanupParallelism = 8

type cleanupResult struct {
	request   Request
	completed bool
	err       error
}

// Parallel store operations share an elapsed-time budget, each with its own
// deadline. An unavailable row cannot serialize all otherwise healthy work.
func (t *transmittable) cleanupRequests(ctx context.Context, requests []Request, operation func(context.Context, Request) cleanupResult) ([]cleanupResult, int) {
	results := make([]cleanupResult, len(requests))
	var next atomic.Int64
	var workers sync.WaitGroup
	for range min(cleanupParallelism, len(requests)) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			defer registerCallbackWorker(&t.callbacks)()
			for ctx.Err() == nil {
				index := int(next.Add(1) - 1)
				if index >= len(requests) {
					return
				}
				opCtx, cancel := context.WithTimeout(ctx, t.settings.StoreAccessTimeOut)
				results[index] = operation(opCtx, requests[index])
				cancel()
			}
		}()
	}
	workers.Wait()
	count := min(int(next.Load()), len(requests))
	return results[:count], count
}

func (t *transmittable) cleanupListedRequest(ctx context.Context, request Request) cleanupResult {
	result := cleanupResult{request: request}
	foreign := t.foreignClosed(request)
	abandoned := t.lineage.discarded(request)
	if !t.ownsDead(request) && !foreign && (!abandoned || request.Owner != t.owner) {
		// Repair metadata only while the original local write still exists.
		if request.WrittenAt.IsZero() {
			if at := requestWrittenTime(request); !at.IsZero() {
				_, result.err = t.requestStore.MarkWritten(ctx, request, at)
				if result.err == nil {
					requestMetadataWritten(request)
				}
			}
		} else {
			requestMetadataWritten(request)
		}
		return result
	}
	removed, err := t.deleteRequestContext(ctx, request)
	result.err = err
	result.completed = removed && !foreign && !t.lineage.discarded(request)
	return result
}

// A finally closed owner with retained identities supplies the shared-store
// cleanup hint. Reuse its registry entry until cleanup settles the last ID.
func (t *transmittable) pendingStoreCleanup() bool {
	pending := false
	liveRequestOwners.Range(func(_, value any) bool {
		state := value.(*requestOwnerState)
		state.mu.Lock()
		pending = !state.alive && state.lineage.closed.Load() && len(state.requests) != 0 && sameRequestStore(state.store, t.requestStore)
		state.mu.Unlock()
		return !pending
	})
	return pending
}

func (t *transmittable) closedStoreLineages() []*requestLineage {
	visited := map[*requestLineage]bool{t.lineage: true}
	var lineages []*requestLineage
	liveRequestOwners.Range(func(_, value any) bool {
		state := value.(*requestOwnerState)
		state.mu.Lock()
		lineage := state.lineage
		eligible := !state.alive && lineage.closed.Load() && sameRequestStore(state.store, t.requestStore)
		state.mu.Unlock()
		if eligible && !visited[lineage] {
			visited[lineage] = true
			lineages = append(lineages, lineage)
		}
		return true
	})
	slices.SortFunc(lineages, func(a, b *requestLineage) int { return cmp.Compare(a.id, b.id) })
	return lineages
}

// One asynchronous scan can run at a time. Full-window traffic without an orphan
// hint is rate-limited; slow stores and callbacks never stall the writer.
func (t *transmittable) reclaimDeadRequests() {
	t.reclaimMu.Lock()
	now := time.Now()
	if t.reclaimRunning || now.Before(t.nextReclaim) {
		t.reclaimMu.Unlock()
		return
	}
	t.reclaimRunning = true
	interval := t.settings.ExpireCheckTimer
	if interval <= 0 {
		interval = time.Second
	}
	t.nextReclaim = now.Add(interval)
	t.reclaimWG.Add(1)
	t.reclaimMu.Unlock()
	go func() {
		defer t.reclaimWG.Done()
		defer registerCallbackWorker(&t.callbacks)()
		defer func() { t.reclaimMu.Lock(); t.reclaimRunning = false; t.reclaimMu.Unlock() }()
		t.lineage.orphaned.Store(false)
		// Own-lineage bookkeeping precedes the budget and the first backend
		// operation. Closed foreign lineages must not delay current admissions.
		own := uncertainSnapshot(t.lineage)
		lineages := t.closedStoreLineages()
		ctx, cancel := context.WithTimeout(context.Background(), t.settings.StoreAccessTimeOut)
		var completed []Request
		gets := 0
		// Budget-limited scans rotate their starting phase. Each phase can then
		// use a fresh operation budget, rather than repeatedly getting only the
		// preceding phase's leftovers. Healthy scans start with our own lineage.
		t.lineage.mu.Lock()
		firstPhase := t.lineage.cleanupPhase
		t.lineage.mu.Unlock()
		var failures []error
		for offset := range 3 {
			if ctx.Err() != nil {
				break
			}
			phase := (firstPhase + offset) % 3
			switch phase {
			case 0:
				allowance := maxCleanupGets
				if len(lineages) != 0 {
					allowance /= 2
				}
				failures = append(failures, t.resolveUncertainSnapshot(ctx, t.lineage, own, &gets, min(maxCleanupGets, gets+allowance))...)
			case 1:
				opCtx, opCancel := context.WithTimeout(ctx, t.settings.StoreAccessTimeOut)
				requests := t.requestStore.List(opCtx)
				if err := opCtx.Err(); err != nil {
					failures = append(failures, err)
					t.lineage.orphaned.Store(true)
				}
				opCancel()
				slices.SortFunc(requests, func(a, b Request) int { return cmp.Compare(a.ID, b.ID) })
				t.lineage.mu.Lock()
				start := 0
				for start < len(requests) && requests[start].ID <= t.lineage.cleanupRow {
					start++
				}
				t.lineage.mu.Unlock()
				requests = append(requests[start:], requests[:start]...)
				results, count := t.cleanupRequests(ctx, requests, t.cleanupListedRequest)
				if count != 0 {
					t.lineage.mu.Lock()
					t.lineage.cleanupRow = requests[count-1].ID
					t.lineage.mu.Unlock()
				}
				for _, result := range results {
					if result.err != nil {
						failures = append(failures, result.err)
					}
					if result.completed {
						completed = append(completed, result.request)
					}
				}
			case 2:
				limit := maxCleanupGets
				if len(own) != 0 {
					limit = min(limit, gets+maxCleanupGets/2)
				}
				t.lineage.mu.Lock()
				start := 0
				for start < len(lineages) && lineages[start].id <= t.lineage.cleanupLineage {
					start++
				}
				t.lineage.mu.Unlock()
				for offset := range len(lineages) {
					if gets >= limit || ctx.Err() != nil {
						break
					}
					lineage := lineages[(start+offset)%len(lineages)]
					t.lineage.mu.Lock()
					t.lineage.cleanupLineage = lineage.id
					t.lineage.mu.Unlock()
					failures = append(failures, t.resolveUncertainSnapshot(ctx, lineage, uncertainSnapshot(lineage), &gets, limit)...)
				}
			}
		}
		t.lineage.mu.Lock()
		if ctx.Err() != nil {
			t.lineage.cleanupPhase = (firstPhase + 1) % 3
		} else {
			t.lineage.cleanupPhase = 0
		}
		t.lineage.mu.Unlock()
		// List can omit rows even without an error. The ledger carries every
		// unfinished operation into a later scan, including foreign leftovers.
		if ctx.Err() != nil || len(failures) != 0 || t.lineage.orphans.Load() > 0 || t.lineage.repairs.Load() > 0 {
			t.lineage.orphaned.Store(true)
		}
		t.lineage.abandoned.Range(func(_, _ any) bool { t.lineage.orphaned.Store(true); return false })
		cancel()
		for _, err := range failures {
			t.reportStoreError(err)
		}
		if t.settings.OnClosePduRequest != nil {
			for _, request := range completed {
				invokeCallback(&t.callbacks, func() { t.settings.OnClosePduRequest(request.PDU) })
			}
		}
	}()
}

// A different wrapper may address another backend even when it lists no rows.
// Only the identical comparable store instance establishes safe absence.
func sameRequestStore(first, second RequestStore) bool {
	return first != nil && reflect.ValueOf(first).Comparable() && reflect.ValueOf(second).Comparable() && first == second
}

const maxCleanupGets = 256

func uncertainSnapshot(lineage *requestLineage) []Request {
	var requests []Request
	lineage.abandoned.Range(func(_, value any) bool {
		request, ok := value.(Request)
		if ok {
			requests = append(requests, request)
		}
		return true
	})
	slices.SortFunc(requests, func(a, b Request) int { return cmp.Compare(a.ID, b.ID) })
	lineage.mu.Lock()
	start := 0
	for start < len(requests) && requests[start].ID <= lineage.cleanupID {
		start++
	}
	lineage.mu.Unlock()
	return append(requests[start:], requests[:start]...)
}

func (t *transmittable) resolveUncertainRequests(ctx context.Context, lineage *requestLineage, gets *int) []error {
	return t.resolveUncertainSnapshot(ctx, lineage, uncertainSnapshot(lineage), gets, maxCleanupGets)
}

func (t *transmittable) resolveUncertainSnapshot(ctx context.Context, lineage *requestLineage, requests []Request, gets *int, limit int) []error {
	var failures []error
	for _, request := range requests {
		if (limit > 0 && *gets >= limit) || ctx.Err() != nil {
			break
		}
		lineage.mu.Lock()
		lineage.cleanupID = request.ID
		lineage.mu.Unlock()
		*gets++
		opCtx, cancel := context.WithTimeout(ctx, t.settings.StoreAccessTimeOut)
		current, exists := t.requestStore.Get(opCtx, request.GetSequenceNumber())
		err := opCtx.Err()
		cancel()
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if exists && sameReservation(current, request) && current.Lineage == request.Lineage {
			if ctx.Err() != nil {
				break
			}
			opCtx, cancel := context.WithTimeout(ctx, t.settings.StoreAccessTimeOut)
			_, err := t.deleteRequestContext(opCtx, request)
			cancel()
			if err != nil {
				failures = append(failures, err)
			}
		} else {
			settleRequest(request, false)
		}
	}
	return failures
}

func (t *transmittable) rememberMetadataFailure(request Request) {
	s := t.ownerState
	s.mu.Lock()
	defer s.mu.Unlock()
	if current, exists := s.requests[request.ID]; !exists || !current.transmitting {
		return
	}
	if s.pendingMetadata == nil {
		s.pendingMetadata = make(map[uint64]struct{})
	}
	if _, pending := s.pendingMetadata[request.ID]; !pending {
		s.pendingMetadata[request.ID] = struct{}{}
		s.lineage.repairs.Add(1)
	}
}
func requestMetadataWritten(request Request) {
	value, ok := liveRequestOwners.Load(request.Owner)
	if !ok {
		return
	}
	s := value.(*requestOwnerState)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, pending := s.pendingMetadata[request.ID]; pending {
		delete(s.pendingMetadata, request.ID)
		s.lineage.repairs.Add(-1)
	}
}

// Permits bound admitted application work, including queued PDUs, independently
// of the written-only count exposed by the request store.
func (t *transmittable) acquireRequestPermit(ctx context.Context, request Request, callerID *uint64) (bool, error) {
	s := t.ownerState
	select {
	case s.permits <- struct{}{}:
	default:
		release, allowed := t.beginSubmitWait(callerID, true)
		if !allowed {
			return false, ErrSubmitQueueFull
		}
		defer release()
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-t.stopped:
			return false, ErrConnectionClosing
		case s.permits <- struct{}{}:
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.requests[request.ID]; !exists {
		<-s.permits
		return false, nil
	}
	s.admitted[request.ID] = struct{}{}
	return true, nil
}
func (s *requestOwnerState) releasePermit(id uint64) {
	if _, held := s.admitted[id]; held {
		delete(s.admitted, id)
		<-s.permits
	}
}
func releaseRequestPermit(request Request) {
	value, ok := liveRequestOwners.Load(request.Owner)
	if !ok {
		return
	}
	s := value.(*requestOwnerState)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.releasePermit(request.ID)
}
func (t *transmittable) beginSubmitWait(callerID *uint64, window bool) (func(), bool) {
	if *callerID == 0 {
		*callerID = callbackGoroutineID()
	}
	if t.writerID.Load() == *callerID || (window && callbackWorkerRegistered(*callerID)) {
		return nil, false
	}
	return beginCallbackQueueWait(&t.callbacks, *callerID)
}
