package gosmpp

import (
	"fmt"
	"testing"
	"time"
)

func lifecycleIdleRootMembers(count int) *callbackScope {
	root := &callbackScope{members: make(map[uint64]uint32, count)}
	for i := range count {
		root.members[^uint64(0)-uint64(i)] = 1
	}
	return root
}

func lifecycleQueueWaitCost(t *testing.T, root *callbackScope, id uint64) time.Duration {
	t.Helper()
	const repeats, samples = 5, 256
	best := time.Duration(1<<63 - 1)
	for range repeats {
		start := time.Now()
		for range samples {
			release, allowed := beginCallbackQueueWait(root, id)
			if !allowed {
				t.Fatal("an external queue waiter unexpectedly formed a cycle")
			}
			release()
		}
		if elapsed := time.Since(start) / samples; elapsed < best {
			best = elapsed
		}
	}
	return best
}

func TestLifecycleQueueWaitDoesNotScanIdleMembers(t *testing.T) {
	id := callbackGoroutineID()
	small := lifecycleQueueWaitCost(t, lifecycleIdleRootMembers(8), id)
	large := lifecycleQueueWaitCost(t, lifecycleIdleRootMembers(10000), id)
	// Compare roots on this machine, leaving room for instrumentation and host
	// load. Idle connection workers must not make queue admission linear in
	// the number of connections; the benchmark provides the precise cost.
	if large > 64*small && large > 10*time.Microsecond {
		t.Fatalf("queue admission scales with idle workers: 8 members=%s, 10000 members=%s", small, large)
	}
}

func TestLifecycleJoinDoesNotScanIdleMembers(t *testing.T) {
	id := callbackGoroutineID()
	measure := func(root *callbackScope) time.Duration {
		const repeats, samples = 5, 256
		best := time.Duration(1<<63 - 1)
		for range repeats {
			start := time.Now()
			for range samples {
				callbackRegistry.Lock()
				cycle := callbackDependsOnCaller(root, id, make(map[*callbackScope]bool))
				callbackRegistry.Unlock()
				if cycle {
					t.Fatal("idle connections unexpectedly depend on an external caller")
				}
			}
			if elapsed := time.Since(start) / samples; elapsed < best {
				best = elapsed
			}
		}
		return best
	}
	small, large := measure(lifecycleIdleRootMembers(8)), measure(lifecycleIdleRootMembers(10000))
	if large > 64*small && large > 10*time.Microsecond {
		t.Fatalf("shutdown dependency walk scales with idle workers: 8 members=%s, 10000 members=%s", small, large)
	}
}

func lifecycleAssertDependsOnCaller(t *testing.T, scope *callbackScope, id uint64, want bool) {
	t.Helper()
	callbackRegistry.Lock()
	got := callbackDependsOnCaller(scope.instance(), id, make(map[*callbackScope]bool))
	callbackRegistry.Unlock()
	if got != want {
		t.Fatalf("callback dependency = %t, want %t", got, want)
	}
}

func TestLifecycleWaitIndexTracksAllMembershipRoots(t *testing.T) {
	const workerID = ^uint64(0) - 100
	a, b, c := &callbackScope{}, &callbackScope{}, &callbackScope{}
	callerID := callbackGoroutineID()
	defer registerCallbackWorkerID(a, callerID)()
	outer := registerCallbackWorkerID(b, workerID)
	defer outer()
	release, allowed := beginCallbackQueueWait(a, workerID)
	if !allowed {
		t.Fatal("initial queue wait formed a cycle")
	}
	defer release()
	// A synchronous fallback can add membership after the goroutine has
	// registered a wait. Both roots must inherit that same dependency.
	nested := registerCallbackWorkerID(b, workerID)
	defer func() {
		if nested != nil {
			nested()
		}
	}()
	secondRoot := registerCallbackWorkerID(c, workerID)
	defer func() {
		if secondRoot != nil {
			secondRoot()
		}
	}()
	lifecycleAssertDependsOnCaller(t, b, callerID, true)
	lifecycleAssertDependsOnCaller(t, c, callerID, true)
	nested()
	nested = nil
	lifecycleAssertDependsOnCaller(t, b, callerID, true)
	secondRoot()
	secondRoot = nil
	lifecycleAssertDependsOnCaller(t, c, callerID, false)
	release()
	lifecycleAssertDependsOnCaller(t, b, callerID, false)
}

func TestLifecycleWaitIndexReleasesMembershipReferences(t *testing.T) {
	const workerID = ^uint64(0) - 102
	a, b := &callbackScope{}, &callbackScope{}
	first := registerCallbackWorkerID(a, workerID)
	second := registerCallbackWorkerID(b, workerID)
	release, allowed := beginCallbackQueueWait(&callbackScope{}, workerID)
	if !allowed {
		first()
		second()
		t.Fatal("external target unexpectedly formed a cycle")
	}
	defer release()
	first()
	callbackRegistry.Lock()
	remaining := len(callbackRegistry.owners[workerID])
	firstWaits := len(a.waitingMembers)
	secondWaits := len(b.waitingMembers)
	callbackRegistry.Unlock()
	second()
	callbackRegistry.Lock()
	rootsRetained := callbackRegistry.owners[workerID] != nil
	workersRetained := callbackRegistry.workers[workerID] != 0
	secondWaitsAfterExit := len(b.waitingMembers)
	callbackRegistry.Unlock()
	if remaining != 1 || firstWaits != 0 || secondWaits != 1 {
		t.Errorf("first membership exit: roots=%d first waits=%d second waits=%d", remaining, firstWaits, secondWaits)
	}
	if rootsRetained || workersRetained || secondWaitsAfterExit != 0 {
		t.Errorf("last membership exit retained references: roots=%t worker=%t waits=%d", rootsRetained, workersRetained, secondWaitsAfterExit)
	}
}

func TestLifecycleJoinPublishesActiveWaitForOtherRoots(t *testing.T) {
	a, b := &callbackScope{}, &callbackScope{}
	done := make(chan struct{})
	finished := make(chan struct{})
	joined := make(chan bool, 1)
	ids := make(chan uint64, 1)
	go func() {
		defer close(finished)
		defer registerCallbackWorker(b)()
		ids <- callbackGoroutineID()
		joined <- joinCallbackScope(a, done)
	}()
	defer func() {
		select {
		case <-done:
		default:
			close(done)
		}
		lifecycleWait(t, finished)
	}()
	lifecycleAwaitWait(t, <-ids, a)
	id := callbackGoroutineID()
	defer registerCallbackWorker(a)()
	// B's worker is waiting for A. A's callback closing B must see the edge
	// published by the blocking shutdown join, not only outbound queue waits.
	lifecycleAssertDependsOnCaller(t, b, id, true)
	close(done)
	if !<-joined {
		t.Fatal("initial join escaped a cycle that did not yet exist")
	}
}

func TestLifecycleWaitIndexTracksWaitStartedAfterNestedMembership(t *testing.T) {
	const workerID = ^uint64(0) - 101
	a, b, c := &callbackScope{}, &callbackScope{}, &callbackScope{}
	callerID := callbackGoroutineID()
	defer registerCallbackWorkerID(a, callerID)()
	defer registerCallbackWorkerID(b, workerID)()
	defer registerCallbackWorkerID(c, workerID)()
	release, allowed := beginCallbackQueueWait(a, workerID)
	if !allowed {
		t.Fatal("initial queue wait formed a cycle")
	}
	defer release()
	lifecycleAssertDependsOnCaller(t, b, callerID, true)
	lifecycleAssertDependsOnCaller(t, c, callerID, true)
	release()
	lifecycleAssertDependsOnCaller(t, b, callerID, false)
	lifecycleAssertDependsOnCaller(t, c, callerID, false)
	callbackRegistry.Lock()
	remaining := callbackRegistry.waiting[workerID]
	callbackRegistry.Unlock()
	if remaining != nil {
		t.Fatal("released queue wait retained its global edge")
	}
}

func TestLifecycleQueueWaitExcludesCallersCurrentWaitEdge(t *testing.T) {
	scope := &callbackScope{}
	defer registerCallbackWorker(scope)()
	id := callbackGoroutineID()
	release, allowed := beginCallbackQueueWait(scope, id)
	if !allowed {
		t.Fatal("worker could not wait for its own writer")
	}
	defer release()
	// Queue admission may examine an existing wait while retrying. The
	// caller's own edge is not a second worker that can close a wait cycle.
	second, allowed := beginCallbackQueueWait(scope, id)
	if !allowed {
		t.Fatal("worker's own queue edge was treated as a cross-worker cycle")
	}
	second()
}

func BenchmarkLifecycleQueueWaitMembers(b *testing.B) {
	for _, count := range []int{8, 1000, 10000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			root := lifecycleIdleRootMembers(count)
			id := callbackGoroutineID()
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				release, allowed := beginCallbackQueueWait(root, id)
				if !allowed {
					b.Fatal("external queue waiter unexpectedly formed a cycle")
				}
				release()
			}
		})
	}
}
