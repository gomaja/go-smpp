package gosmpp

import (
	"runtime"
	"testing"
)

func TestLifecycleCallbackOwnershipAndCleanup(t *testing.T) {
	a, b := &callbackScope{}, &callbackScope{}
	child := &callbackScope{parent: a}
	invokeExternalCallback(child, func() {
		if !insideCallback(a) || insideCallback(b) {
			t.Error("callback ownership crossed independent instances")
		}
		invokeExternalCallback(b, func() {
			if !insideCallback(a) || !insideCallback(b) {
				t.Error("nested callback lost an active owner")
			}
		})
		if insideCallback(b) {
			t.Error("returned callback retained its owner")
		}
	})
	if insideCallback(a) {
		t.Error("callback ownership survived return")
	}
	func() {
		defer func() { _ = recover() }()
		invokeExternalCallback(a, func() { panic("callback failure") })
	}()
	if insideCallback(a) {
		t.Error("callback ownership survived panic")
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		invokeExternalCallback(a, runtime.Goexit)
	}()
	lifecycleWait(t, done)
	callbackRegistry.Lock()
	remaining := len(a.members) + len(b.members)
	for _, target := range callbackRegistry.waiting {
		if target == a || target == b {
			remaining++
		}
	}
	callbackRegistry.Unlock()
	if remaining != 0 {
		t.Errorf("callback registry retained %d exited goroutines", remaining)
	}
}

func BenchmarkLifecycleCallbackDispatch(b *testing.B) {
	scope := &callbackScope{}
	fn := func() {}
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		defer registerCallbackWorker(scope)()
		for pb.Next() {
			invokeCallback(scope, fn)
		}
	})
}

func BenchmarkLifecycleDirectCallback(b *testing.B) {
	fn := func() {}
	b.ReportAllocs()
	for b.Loop() {
		fn()
	}
}
