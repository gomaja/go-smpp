package gosmpp

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"

	"github.com/gomaja/go-smpp/pdu"
)

func TestLifecycleWorkerRegistrationCountsAcrossInstancesAndExit(t *testing.T) {
	a, b := &callbackScope{}, &callbackScope{}
	id := callbackGoroutineID()
	if callbackWorkerRegistered(id) {
		t.Fatal("external caller already registered as a worker")
	}
	invokeExternalCallback(a, func() {
		if !callbackWorkerRegistered(id) {
			t.Error("callback did not register worker membership")
		}
		invokeExternalCallback(b, func() {
			if !callbackWorkerRegistered(id) {
				t.Error("nested callback lost worker membership")
			}
		})
		if !callbackWorkerRegistered(id) {
			t.Error("nested callback exit removed the outer worker membership")
		}
	})
	if callbackWorkerRegistered(id) {
		t.Error("worker membership survived callback return")
	}
	func() {
		defer func() { _ = recover() }()
		invokeExternalCallback(a, func() { panic("callback failure") })
	}()
	if callbackWorkerRegistered(id) {
		t.Error("worker membership survived callback panic")
	}
	exited := make(chan uint64, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		exited <- callbackGoroutineID()
		invokeExternalCallback(a, runtime.Goexit)
	}()
	lifecycleWait(t, done)
	if callbackWorkerRegistered(<-exited) {
		t.Error("worker membership survived Goexit")
	}
}

func TestLifecycleCallbackCleanupIgnoresOtherInstances(t *testing.T) {
	const unrelatedID = ^uint64(0) - 7
	unrelated := &callbackScope{}
	defer registerCallbackWorkerID(unrelated, unrelatedID)()
	release, allowed := beginCallbackQueueWait(unrelated, unrelatedID)
	if !allowed {
		t.Fatal("unrelated worker could not register its queue wait")
	}
	defer release()
	t.Run("isolated_ownership", TestLifecycleCallbackOwnershipAndCleanup)
}

func TestLifecycleQueueWaitAllowsOwnWorkerAndCleansUp(t *testing.T) {
	scope := &callbackScope{}
	defer registerCallbackWorker(scope)()
	id := callbackGoroutineID()
	release, allowed := beginCallbackQueueWait(scope, id)
	if !allowed {
		t.Fatal("reader or cleanup worker cannot wait for its own writer")
	}
	callbackRegistry.Lock()
	target := callbackRegistry.waiting[id]
	callbackRegistry.Unlock()
	if target != scope {
		t.Error("queue admission did not register its wait")
	}
	release()
	callbackRegistry.Lock()
	target = callbackRegistry.waiting[id]
	callbackRegistry.Unlock()
	if target != nil {
		t.Error("completed queue admission retained a wait edge")
	}
}

func TestLifecycleQueueWaitRejectsThreeInstanceCycle(t *testing.T) {
	const bID, cID = ^uint64(0) - 8, ^uint64(0) - 9
	a, b, c := &callbackScope{}, &callbackScope{}, &callbackScope{}
	defer registerCallbackWorker(a)()
	defer registerCallbackWorkerID(b, bID)()
	defer registerCallbackWorkerID(c, cID)()
	bWait, allowed := beginCallbackQueueWait(c, bID)
	if !allowed {
		t.Fatal("initial queue wait formed a cycle")
	}
	defer bWait()
	cWait, allowed := beginCallbackQueueWait(a, cID)
	if !allowed {
		t.Fatal("second queue wait formed a cycle")
	}
	defer cWait()
	if release, allowed := beginCallbackQueueWait(b, callbackGoroutineID()); allowed {
		release()
		t.Fatal("queue admission completed a three-instance wait cycle")
	}
}

func TestLifecycleSubmitContextCancellationRemovesQueueWait(t *testing.T) {
	conn, cleanup := newPipePair()
	defer cleanup()
	out := newTransmittable(conn, Settings{}, nil)
	if err := out.Submit(pdu.NewSubmitSM()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ids, returned := make(chan uint64, 1), make(chan error, 1)
	go func() {
		ids <- callbackGoroutineID()
		returned <- out.SubmitContext(ctx, pdu.NewSubmitSM())
	}()
	id := <-ids
	lifecycleAwaitWait(t, id, &out.callbacks)
	cancel()
	select {
	case err := <-returned:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled admission = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled queue admission did not return")
	}
	callbackRegistry.Lock()
	target := callbackRegistry.waiting[id]
	callbackRegistry.Unlock()
	if target != nil {
		t.Error("canceled queue admission retained a wait edge")
	}
	_ = out.close(ExplicitClosing)
}
