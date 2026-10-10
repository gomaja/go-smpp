package gosmpp

import (
	"runtime"
	"sync"
)

// Children share their session's or server's callback ownership. These links
// are set before workers start and never change afterward.
type callbackScope struct {
	parent         *callbackScope
	members        map[uint64]uint32 // Protected by callbackRegistry; root instances only.
	waitingMembers map[uint64]*callbackScope
}

func (s *callbackScope) instance() *callbackScope {
	for s.parent != nil {
		s = s.parent
	}
	return s
}

var callbackRegistry = struct {
	sync.Mutex
	waiting map[uint64]*callbackScope
	workers map[uint64]uint32
	owners  map[uint64]map[*callbackScope]struct{}
}{waiting: make(map[uint64]*callbackScope), workers: make(map[uint64]uint32), owners: make(map[uint64]map[*callbackScope]struct{})}

var callbackHeaders = sync.Pool{New: func() any { return new([32]byte) }}

// runtime.Stack starts with "goroutine <id> ". Only that header is inspected;
// membership does not depend on stack symbols, compiler inlining or function
// names. Workers inspect the header once at entry, and joins inspect it only
// on the shutdown path. Application goroutines do not inherit membership.
func callbackGoroutineID() uint64 {
	header := callbackHeaders.Get().(*[32]byte)
	n := runtime.Stack(header[:], false)
	var id uint64
	for i := len("goroutine "); i < n; i++ {
		b := header[i]
		if b < '0' || b > '9' {
			break
		}
		id = id*10 + uint64(b-'0')
	}
	callbackHeaders.Put(header)
	if id == 0 {
		panic("cannot identify callback goroutine")
	}
	return id
}

// registerCallbackWorker registers a goroutine whose progress the instance's
// completion depends on. Its caller must defer the returned cleanup, including
// on panic or Goexit. Counts preserve nested synchronous fallback membership.
func registerCallbackWorker(scope *callbackScope) func() {
	return registerCallbackWorkerID(scope, callbackGoroutineID())
}

func registerCallbackWorkerID(scope *callbackScope, id uint64) func() {
	owner := scope.instance()
	callbackRegistry.Lock()
	if owner.members == nil {
		owner.members = make(map[uint64]uint32)
	}
	if owner.members[id] == 0 {
		if callbackRegistry.owners[id] == nil {
			callbackRegistry.owners[id] = make(map[*callbackScope]struct{})
		}
		callbackRegistry.owners[id][owner] = struct{}{}
		if target := callbackRegistry.waiting[id]; target != nil {
			owner.addWait(id, target)
		}
	}
	owner.members[id]++
	callbackRegistry.workers[id]++
	callbackRegistry.Unlock()
	return func() {
		callbackRegistry.Lock()
		if owner.members[id] == 1 {
			delete(owner.members, id)
			delete(owner.waitingMembers, id)
			delete(callbackRegistry.owners[id], owner)
			if len(callbackRegistry.owners[id]) == 0 {
				delete(callbackRegistry.owners, id)
			}
		} else {
			owner.members[id]--
		}
		if callbackRegistry.workers[id] == 1 {
			delete(callbackRegistry.workers, id)
		} else {
			callbackRegistry.workers[id]--
		}
		callbackRegistry.Unlock()
	}
}

// invokeCallback runs on an already registered worker. Callback dispatch itself
// performs no stack inspection or registry access.
func invokeCallback(_ *callbackScope, fn func()) { fn() }

// invokeExternalCallback covers synchronous error callbacks on API callers.
// Most callbacks run on workers and use invokeCallback instead.
func invokeExternalCallback(scope *callbackScope, fn func()) {
	defer registerCallbackWorker(scope)()
	fn()
}

func insideCallback(scope *callbackScope) bool {
	return callbackScopeContainsID(scope, callbackGoroutineID())
}

func callbackScopeContainsID(scope *callbackScope, id uint64) bool {
	owner := scope.instance()
	callbackRegistry.Lock()
	defer callbackRegistry.Unlock()
	return owner.members[id] != 0
}

// callbackWorkerRegistered is used only when window admission would block.
// Waiting for a remote response from any library callback can prevent that
// callback's instance from shutting down, even after a close-cycle escape.
// Registration updates this count once at worker entry and exit; dispatch
// remains free of stack inspection and registry access.
func callbackWorkerRegistered(id uint64) bool {
	callbackRegistry.Lock()
	defer callbackRegistry.Unlock()
	return callbackRegistry.workers[id] != 0
}

// callbackDependsOnCaller follows dependency edges while callbackRegistry is
// locked. Membership and waits are tracked for the whole Session or Server.
// Only members with active waits contribute edges, so idle connections do not
// increase the cost of a queue admission or shutdown join.
func callbackDependsOnCaller(target *callbackScope, id uint64, visited map[*callbackScope]bool) bool {
	if target.members[id] != 0 {
		return true
	}
	if visited[target] {
		return false
	}
	visited[target] = true
	for _, next := range target.waitingMembers {
		if callbackDependsOnCaller(next, id, visited) {
			return true
		}
	}
	return false
}

// addWait and setCallbackWait require callbackRegistry to be locked. Root
// indexes are updated both when a wait starts and when a synchronous fallback
// adds another root membership to a goroutine whose wait already exists.
func (s *callbackScope) addWait(id uint64, target *callbackScope) {
	if s.waitingMembers == nil {
		s.waitingMembers = make(map[uint64]*callbackScope)
	}
	s.waitingMembers[id] = target
}

func setCallbackWait(id uint64, target *callbackScope) {
	callbackRegistry.waiting[id] = target
	for owner := range callbackRegistry.owners[id] {
		owner.addWait(id, target)
	}
}

func clearCallbackWait(id uint64) {
	callbackRegistry.Lock()
	delete(callbackRegistry.waiting, id)
	for owner := range callbackRegistry.owners[id] {
		delete(owner.waitingMembers, id)
	}
	callbackRegistry.Unlock()
}

// beginCallbackQueueWait records outbound backpressure before blocking. Unlike
// a shutdown join, a reader or cleanup worker may wait for its own writer: their
// membership alone is not a cycle. A path from the target's waiting workers
// back to this caller is a cycle and must reject admission instead.
func beginCallbackQueueWait(scope *callbackScope, id uint64) (release func(), allowed bool) {
	owner := scope.instance()
	callbackRegistry.Lock()
	visited := make(map[*callbackScope]bool)
	for member, next := range owner.waitingMembers {
		if member == id {
			continue
		}
		if callbackDependsOnCaller(next, id, visited) {
			callbackRegistry.Unlock()
			return nil, false
		}
	}
	setCallbackWait(id, owner)
	callbackRegistry.Unlock()
	return func() { clearCallbackWait(id) }, true
}

// joinCallbackScope atomically admits a wait only when it cannot depend on the
// caller's progress. A self join or a path through other instances back to this
// worker returns false after shutdown has been initiated. External goroutines
// have no membership, so their joins always wait for completion.
func joinCallbackScope(scope *callbackScope, done <-chan struct{}) bool {
	select {
	case <-done:
		return true
	default:
	}
	id := callbackGoroutineID()
	owner := scope.instance()
	callbackRegistry.Lock()
	visited := make(map[*callbackScope]bool)
	if callbackDependsOnCaller(owner, id, visited) {
		callbackRegistry.Unlock()
		return false
	}
	setCallbackWait(id, owner)
	callbackRegistry.Unlock()
	defer clearCallbackWait(id)
	<-done
	return true
}
