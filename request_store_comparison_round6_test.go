package gosmpp

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gomaja/go-smpp/pdu"
)

type comparisonDecoratorStore struct{ RequestStore }

type comparisonMapStore struct {
	RequestStore
	tags map[string]int
}

type comparisonSliceStore struct {
	RequestStore
	tags []string
}

type comparisonFunctionStore struct {
	RequestStore
	log func()
}

type comparisonArrayStore struct {
	RequestStore
	metadata [1]any
}

func TestSameRequestStoreComparesDynamicValuesSafely(t *testing.T) {
	backend := NewDefaultStore()
	otherBackend := NewDefaultStore()
	decorated := comparisonDecoratorStore{RequestStore: backend}
	mapStore := comparisonMapStore{RequestStore: backend, tags: map[string]int{"synthetic": 1}}
	sliceStore := comparisonSliceStore{RequestStore: backend, tags: []string{"synthetic"}}
	functionStore := comparisonFunctionStore{RequestStore: backend, log: func() {}}
	decoratedMap := comparisonDecoratorStore{RequestStore: mapStore}
	nestedMap := comparisonDecoratorStore{RequestStore: comparisonDecoratorStore{RequestStore: decoratedMap}}
	pointerMap := &mapStore
	var nilMap *comparisonMapStore
	arrayMap := comparisonArrayStore{RequestStore: backend, metadata: [1]any{map[string]int{"synthetic": 1}}}
	for _, tc := range []struct {
		name          string
		first, second RequestStore
		want          bool
	}{
		{name: "nil", want: false},
		{name: "nil first", second: backend, want: false},
		{name: "nil second", first: backend, want: false},
		{name: "same value backend", first: backend, second: backend, want: true},
		{name: "different value backend", first: backend, second: otherBackend, want: false},
		{name: "same comparable decorator", first: decorated, second: decorated, want: true},
		{name: "different comparable decorator", first: decorated, second: comparisonDecoratorStore{RequestStore: otherBackend}, want: false},
		{name: "different dynamic types", first: backend, second: decorated, want: false},
		{name: "same map pointer", first: pointerMap, second: pointerMap, want: true},
		{name: "different map pointer", first: pointerMap, second: &comparisonMapStore{RequestStore: backend, tags: mapStore.tags}, want: false},
		{name: "typed nil pointer", first: nilMap, second: nilMap, want: true},
		{name: "map value", first: mapStore, second: mapStore, want: false},
		{name: "slice value", first: sliceStore, second: sliceStore, want: false},
		{name: "function value", first: functionStore, second: functionStore, want: false},
		{name: "decorated map value", first: decoratedMap, second: decoratedMap, want: false},
		{name: "decorated slice value", first: comparisonDecoratorStore{RequestStore: sliceStore}, second: comparisonDecoratorStore{RequestStore: sliceStore}, want: false},
		{name: "decorated function value", first: comparisonDecoratorStore{RequestStore: functionStore}, second: comparisonDecoratorStore{RequestStore: functionStore}, want: false},
		{name: "nested decorated map value", first: nestedMap, second: nestedMap, want: false},
		{name: "array interface containing map", first: arrayMap, second: arrayMap, want: false},
		{name: "comparable and uncomparable decorators", first: decorated, second: decoratedMap, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := sameRequestStore(tc.first, tc.second); got != tc.want {
				t.Fatalf("sameRequestStore(%T, %T) = %t, want %t", tc.first, tc.second, got, tc.want)
			}
			if got := sameRequestStore(tc.second, tc.first); got != tc.want {
				t.Fatalf("reversed sameRequestStore(%T, %T) = %t, want %t", tc.second, tc.first, got, tc.want)
			}
		})
	}
}

func TestWindowDecoratedUncomparableStoreRecoveryAcrossSessions(t *testing.T) {
	for _, depth := range []int{1, 3} {
		t.Run(map[int]string{1: "decorator", 3: "nested decorators"}[depth], func(t *testing.T) {
			core := &cleanupOutageStore{DefaultStore: NewDefaultStore()}
			var store RequestStore = comparisonMapStore{RequestStore: core, tags: map[string]int{"synthetic": 1}}
			for range depth {
				store = comparisonDecoratorStore{RequestStore: store}
			}
			settings := lifecycleSettings()
			settings.StoreAccessTimeOut = 10 * time.Millisecond
			var callbacks atomic.Int32
			settings.OnClosePduRequest = func(pdu.PDU) { callbacks.Add(1) }
			settings.OnExpiredPduRequest = func(pdu.PDU) bool { callbacks.Add(1); return false }
			first := windowForeignUncertainSession(t, store, settings)
			firstOut := first.bound().out
			t.Cleanup(func() { liveRequestOwners.Delete(firstOut.owner) })
			firstOut.reclaimMu.Lock()
			firstOut.nextReclaim = time.Now().Add(time.Hour)
			firstOut.reclaimMu.Unlock()
			core.outage.Store(true)
			core.getTimeout.Store(true)
			if err := first.Transceiver().Submit(pdu.NewSubmitSM()); err == nil {
				t.Fatal("outage accepted request")
			}
			if err := first.Close(); err != nil {
				t.Fatal(err)
			}
			if requests, abandoned := cleanupOutstanding(firstOut); requests != 1 || abandoned != 1 {
				t.Fatalf("closed Session lost uncertain admission: requests=%d abandoned=%d", requests, abandoned)
			}
			core.getTimeout.Store(false)
			second := windowForeignUncertainSession(t, store, settings)
			secondOut := second.bound().out
			secondOut.reclaimMu.Lock()
			secondOut.nextReclaim = time.Now().Add(time.Hour)
			secondOut.reclaimMu.Unlock()
			if err := second.Transceiver().Submit(pdu.NewSubmitSM()); err == nil {
				t.Fatal("outage accepted second Session's request")
			}
			core.outage.Store(false)
			// Join the actual recovery worker rather than waiting for elapsed time.
			windowForeignUncertainRecover(secondOut)
			if requests, abandoned := cleanupOutstanding(secondOut); requests != 0 || abandoned != 0 || secondOut.lineage.orphaned.Load() {
				t.Fatalf("own absent admission did not settle: requests=%d abandoned=%d hint=%t", requests, abandoned, secondOut.lineage.orphaned.Load())
			}
			// Value equality cannot establish that these wrappers identify the same
			// backend. Preserve the foreign cleanup obligation conservatively.
			if requests, abandoned := cleanupOutstanding(firstOut); requests != 1 || abandoned != 1 {
				t.Fatalf("uncomparable foreign store lost cleanup obligation: requests=%d abandoned=%d", requests, abandoned)
			}
			if _, retained := liveRequestOwners.Load(firstOut.owner); !retained {
				t.Fatal("foreign uncertain owner was removed without safe store identity")
			}
			if callbacks.Load() != 0 {
				t.Fatal("rejected admissions produced completion callbacks")
			}
			if err := second.Transceiver().Submit(pdu.NewSubmitSM()); err != nil {
				t.Fatalf("healthy submission after recovery = %v", err)
			}
		})
	}
}

func TestWindowReboundResponseUsesCurrentLineageDiscardedRequests(t *testing.T) {
	old, oldCleanup := newPipePair()
	t.Cleanup(oldCleanup)
	conn, peer, frames := windowResponsePeer(t)
	store := &completionRound6Store{DefaultStore: NewDefaultStore(), deleteEntered: make(chan struct{}), deleteRelease: make(chan struct{})}
	settings := lifecycleSettings()
	settings.ExpireCheckTimer = time.Hour
	settings.StoreAccessTimeOut = time.Second
	rebound, barrier := make(chan struct{}), make(chan struct{})
	var expected, connections atomic.Int32
	settings.OnExpectedPduResponse = func(Response) { expected.Add(1) }
	settings.OnUnexpectedPduResponse = func(p pdu.PDU) {
		if p.GetSequenceNumber() == 0 {
			close(barrier)
		}
	}
	settings.OnRebind = func() { close(rebound) }
	session, err := NewSession(lifecycleConnector{connect: func() (*Connection, error) {
		if connections.Add(1) == 1 {
			return old, nil
		}
		return conn, nil
	}}, settings, 0, WithRequestStore(store))
	if err != nil {
		t.Fatal(err)
	}
	var release sync.Once
	releaseDelete := func() { release.Do(func() { close(store.deleteRelease) }) }
	t.Cleanup(func() { releaseDelete(); store.failDelete.Store(false); _ = session.Close() })
	lineage := session.lineage
	session.rebind()
	lifecycleWait(t, rebound)
	if session.bound().out.lineage != lineage {
		t.Fatal("rebind replaced the Session's request lineage")
	}
	out := session.bound().out
	out.reclaimMu.Lock()
	out.nextReclaim = time.Now().Add(time.Hour)
	out.reclaimMu.Unlock()
	request := pdu.NewEnquireLink()
	if err := session.Transceiver().Submit(request); err != nil {
		t.Fatal(err)
	}
	frame := windowResponseFrame(t, frames)
	var reservation Request
	windowResponseAwait(t, func() bool {
		var found bool
		reservation, found = store.DefaultStore.Get(context.Background(), request.GetSequenceNumber())
		return found && !reservation.WrittenAt.IsZero()
	})
	store.gateID.Store(reservation.ID)
	writeWire(t, peer, frame.GetResponse())
	lifecycleWait(t, store.deleteEntered)
	// The response has removed the remote row, but is still waiting to claim
	// local completion. Retiring the enquiry abandons it in the Session lineage.
	store.failDelete.Store(true)
	if err := session.Transceiver().Submit(pdu.NewEnquireLink()); err != nil {
		t.Fatal(err)
	}
	_ = windowResponseFrame(t, frames)
	if !lineage.discarded(reservation) {
		t.Fatal("failed enquiry retirement did not remember the discarded request")
	}
	store.failDelete.Store(false)
	releaseDelete()
	sentinel := pdu.NewEnquireLink().GetResponse()
	sentinel.SetSequenceNumber(0)
	writeWire(t, peer, sentinel)
	lifecycleWait(t, barrier)
	if got := expected.Load(); got != 0 {
		t.Fatalf("rebound response completed a discarded enquiry %d times, want 0", got)
	}
}
