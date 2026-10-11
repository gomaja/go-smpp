package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	gosmpp "github.com/gomaja/go-smpp"
	"github.com/gomaja/go-smpp/pdu"
)

type writtenCounterStore interface {
	Set(context.Context, gosmpp.Request) error
	MarkWritten(context.Context, gosmpp.Request, time.Time) (bool, error)
	Delete(context.Context, gosmpp.Request) (bool, error)
	Clear(context.Context) error
	WrittenLength(context.Context, uint64) (int, error)
}

func newCustomCounterContractStore(t *testing.T) writtenCounterStore {
	t.Helper()
	store, ok := any(newTestCustomStore(t)).(writtenCounterStore)
	if !ok {
		t.Fatal("RequestStore must support WrittenLength for completed application writes")
	}
	return store
}

func assertWrittenCounter(t *testing.T, store writtenCounterStore, owner uint64, want int) {
	t.Helper()
	n, err := store.WrittenLength(context.Background(), owner)
	if err != nil || n != want {
		t.Fatalf("WrittenLength excluding owner %d = %d, %v; want %d", owner, n, err, want)
	}
}

func TestCustomStoreWrittenLengthTracksTransitions(t *testing.T) {
	store := newCustomCounterContractStore(t)
	ctx := context.Background()
	pending := gosmpp.Request{PDU: pdu.NewSubmitSM(), Owner: 101, ID: 103, TimeSent: time.Unix(1, 0)}
	other := gosmpp.Request{PDU: pdu.NewSubmitSM(), Owner: 107, ID: 109, WrittenAt: time.Unix(2, 0)}
	enquiry := gosmpp.Request{PDU: pdu.NewEnquireLink(), Owner: 101, ID: 113, WrittenAt: time.Unix(2, 0)}
	for _, r := range []gosmpp.Request{pending, enquiry} {
		if err := store.Set(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	assertWrittenCounter(t, store, 0, 0)
	if err := store.Set(ctx, other); err != nil {
		t.Fatal(err)
	}
	assertWrittenCounter(t, store, 0, 1)
	assertWrittenCounter(t, store, 101, 1)
	assertWrittenCounter(t, store, 107, 0)
	assertWrittenCounter(t, store, 999, 1)
	if marked, err := store.MarkWritten(ctx, pending, time.Unix(3, 0)); err != nil || !marked {
		t.Fatalf("mark pending = %t, %v", marked, err)
	}
	assertWrittenCounter(t, store, 0, 2)
	assertWrittenCounter(t, store, 101, 1)
	assertWrittenCounter(t, store, 107, 1)
	if err := store.Set(ctx, pending); !errors.Is(err, gosmpp.ErrSequenceInUse) {
		t.Fatalf("duplicate Set=%v", err)
	}
	if marked, err := store.MarkWritten(ctx, pending, time.Unix(4, 0)); err != nil || marked {
		t.Fatalf("repeat mark = %t, %v", marked, err)
	}
	assertWrittenCounter(t, store, 0, 2)
	stale := pending
	stale.ID++
	if removed, err := store.Delete(ctx, stale); err != nil || removed {
		t.Fatalf("stale deletion = %t, %v", removed, err)
	}
	assertWrittenCounter(t, store, 0, 2)
	if removed, err := store.Delete(ctx, pending); err != nil || !removed {
		t.Fatalf("pending snapshot deletion = %t, %v", removed, err)
	}
	assertWrittenCounter(t, store, 0, 1)
	assertWrittenCounter(t, store, 101, 1)
	if removed, err := store.Delete(ctx, pending); err != nil || removed {
		t.Fatalf("repeated deletion = %t, %v", removed, err)
	}
	if removed, err := store.Delete(ctx, enquiry); err != nil || !removed {
		t.Fatalf("enquiry deletion = %t, %v", removed, err)
	}
	assertWrittenCounter(t, store, 0, 1)
	legacy := gosmpp.Request{PDU: pdu.NewSubmitSM(), ID: 127, WrittenAt: time.Unix(5, 0)}
	if err := store.Set(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	assertWrittenCounter(t, store, 0, 2)
	assertWrittenCounter(t, store, 107, 1)
	if err := store.Clear(ctx); err != nil {
		t.Fatal(err)
	}
	assertWrittenCounter(t, store, 0, 0)
	assertWrittenCounter(t, store, 107, 0)
}

func TestCustomStoreWrittenLengthHonorsCancellation(t *testing.T) {
	store := newCustomCounterContractStore(t)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	written := gosmpp.Request{PDU: pdu.NewSubmitSM(), Owner: 131, ID: 137, WrittenAt: time.Unix(6, 0)}
	if err := store.Set(canceled, written); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Set=%v", err)
	}
	assertWrittenCounter(t, store, 0, 0)
	if err := store.Set(context.Background(), written); err != nil {
		t.Fatal(err)
	}
	pending := gosmpp.Request{PDU: pdu.NewSubmitSM(), Owner: 131, ID: 139}
	if err := store.Set(context.Background(), pending); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkWritten(canceled, pending, time.Unix(7, 0)); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled MarkWritten=%v", err)
	}
	if _, err := store.Delete(canceled, written); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Delete=%v", err)
	}
	if err := store.Clear(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Clear=%v", err)
	}
	if _, err := store.WrittenLength(canceled, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled WrittenLength=%v", err)
	}
	assertWrittenCounter(t, store, 0, 1)
	assertWrittenCounter(t, store, 131, 0)
}

func TestCustomStoreWrittenLengthHasOneConcurrentTransition(t *testing.T) {
	store := newCustomCounterContractStore(t)
	request := gosmpp.Request{PDU: pdu.NewSubmitSM(), Owner: 149, ID: 151}
	if err := store.Set(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	const contenders = 32
	for _, deleting := range []bool{false, true} {
		start := make(chan struct{})
		results := make(chan bool, contenders)
		var workers sync.WaitGroup
		for range contenders {
			workers.Add(1)
			go func() {
				defer workers.Done()
				<-start
				var won bool
				var err error
				if deleting {
					won, err = store.Delete(context.Background(), request)
				} else {
					won, err = store.MarkWritten(context.Background(), request, time.Now())
				}
				if err != nil {
					t.Errorf("concurrent transition: %v", err)
				}
				results <- won
			}()
		}
		close(start)
		workers.Wait()
		close(results)
		winners := 0
		for won := range results {
			if won {
				winners++
			}
		}
		if winners != 1 {
			t.Fatalf("deleting=%t transition winners=%d, want 1", deleting, winners)
		}
		want := 1
		if deleting {
			want = 0
		}
		assertWrittenCounter(t, store, 0, want)
	}
}
