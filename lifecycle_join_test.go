package gosmpp

import (
	"runtime"
	"testing"
	"time"
)

func lifecycleAwaitWait(t *testing.T, id uint64, target *callbackScope) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		callbackRegistry.Lock()
		waiting := callbackRegistry.waiting[id]
		callbackRegistry.Unlock()
		if waiting == target {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("shutdown join was not registered")
		}
		runtime.Gosched()
	}
}

func TestLifecycleJoinTracksExternalWaitAndRemovesIt(t *testing.T) {
	scope := &callbackScope{}
	done := make(chan struct{})
	ids := make(chan uint64, 1)
	joined := make(chan bool, 1)
	go func() {
		ids <- callbackGoroutineID()
		joined <- joinCallbackScope(scope, done)
	}()
	id := <-ids
	lifecycleAwaitWait(t, id, scope)
	select {
	case <-joined:
		t.Fatal("external shutdown escaped before completion")
	default:
	}
	close(done)
	select {
	case waited := <-joined:
		if !waited {
			t.Fatal("external shutdown reported a cycle")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("external shutdown did not return after completion")
	}
	callbackRegistry.Lock()
	remaining := callbackRegistry.waiting[id]
	callbackRegistry.Unlock()
	if remaining != nil {
		t.Fatal("completed shutdown retained a wait edge")
	}
}

func TestLifecycleJoinTraversesUnrelatedCycle(t *testing.T) {
	// Synthetic graph members are deliberately outside the runtime's goroutine
	// ID range. A cycle that cannot reach this external caller must not escape.
	const first, second = ^uint64(0), ^uint64(0) - 1
	a, b := &callbackScope{}, &callbackScope{}
	defer registerCallbackWorkerID(a, first)()
	defer registerCallbackWorkerID(b, second)()
	callbackRegistry.Lock()
	setCallbackWait(first, b)
	setCallbackWait(second, a)
	callbackRegistry.Unlock()
	defer clearCallbackWait(first)
	defer clearCallbackWait(second)
	done := make(chan struct{})
	ids := make(chan uint64, 1)
	joined := make(chan bool, 1)
	go func() {
		ids <- callbackGoroutineID()
		joined <- joinCallbackScope(a, done)
	}()
	id := <-ids
	lifecycleAwaitWait(t, id, a)
	select {
	case <-joined:
		t.Fatal("unrelated wait cycle made an external caller escape")
	default:
	}
	close(done)
	if !<-joined {
		t.Fatal("external caller did not join the unrelated cycle")
	}
}

func TestLifecycleNestedFallbackKeepsWorkerMembership(t *testing.T) {
	scope := &callbackScope{}
	unregister := registerCallbackWorker(scope)
	invokeExternalCallback(scope, func() {
		invokeExternalCallback(scope, func() {
			if joinCallbackScope(scope, make(chan struct{})) {
				t.Error("nested callback joined its own worker")
			}
		})
		if !insideCallback(scope) {
			t.Error("inner callback removed outer membership")
		}
	})
	if !insideCallback(scope) {
		t.Error("fallback callback removed permanent worker membership")
	}
	unregister()
	if insideCallback(scope) {
		t.Error("worker membership survived exit")
	}
}

func TestLifecycleWorkerCallbackDoesNotLockRegistry(t *testing.T) {
	scope := &callbackScope{}
	registered := make(chan struct{})
	invoke := make(chan struct{})
	called := make(chan struct{})
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		defer registerCallbackWorker(scope)()
		close(registered)
		<-invoke
		invokeCallback(scope, func() { close(called) })
	}()
	lifecycleWait(t, registered)
	callbackRegistry.Lock()
	close(invoke)
	completed := false
	select {
	case <-called:
		completed = true
	case <-time.After(2 * time.Second):
	}
	callbackRegistry.Unlock()
	lifecycleWait(t, exited)
	if !completed {
		t.Fatal("registered worker callback waited for the global registry lock")
	}
}
