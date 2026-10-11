package main

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/allegro/bigcache/v3"
	gosmpp "github.com/gomaja/go-smpp"
	"github.com/gomaja/go-smpp/pdu"
)

func newTestCustomStore(t *testing.T) CustomStore {
	t.Helper()
	config := bigcache.DefaultConfig(time.Hour)
	config.Shards = 1
	config.MaxEntriesInWindow = 16
	config.MaxEntrySize = 256
	config.CleanWindow = 0
	return newTestCustomStoreWithConfig(t, config)
}

func newTestCustomStoreWithConfig(t *testing.T, config bigcache.Config) CustomStore {
	t.Helper()
	cache, err := bigcache.New(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := cache.Close(); err != nil {
			t.Errorf("close cache: %v", err)
		}
	})
	return CustomStore{store: cache, mu: &sync.RWMutex{}, reservations: make(map[string][]byte), written: &writtenCounts{owners: make(map[uint64]int)}}
}

func TestCustomStoreRetainsReservationsAfterCacheEviction(t *testing.T) {
	expired := make(chan string, 1)
	config := bigcache.DefaultConfig(time.Second)
	config.Shards = 1
	config.MaxEntriesInWindow = 16
	config.MaxEntrySize = 256
	config.CleanWindow = 0
	config.OnRemoveWithReason = func(key string, _ []byte, reason bigcache.RemoveReason) {
		if reason == bigcache.Expired {
			expired <- key
		}
	}
	store := newTestCustomStoreWithConfig(t, config)
	request := gosmpp.Request{PDU: pdu.NewEnquireLink(), TimeSent: time.Now(), Owner: 53, ID: 59}
	if err := store.Set(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	// bigcache stores second-resolution timestamps. Wait for the next second,
	// then Set triggers expiry without a background cleanup worker.
	timer := time.NewTimer(time.Until(time.Now().Truncate(time.Second).Add(time.Second)))
	defer timer.Stop()
	<-timer.C
	trigger := gosmpp.Request{PDU: pdu.NewEnquireLink(), TimeSent: time.Now(), Owner: 53, ID: 61}
	if err := store.Set(context.Background(), trigger); err != nil {
		t.Fatal(err)
	}
	select {
	case key := <-expired:
		if key != strconv.Itoa(int(request.GetSequenceNumber())) {
			t.Fatalf("wrong cache entry expired: %q", key)
		}
	default:
		t.Fatal("cache expiry probe did not evict the pending reservation")
	}
	if _, err := store.store.Get(strconv.Itoa(int(request.GetSequenceNumber()))); !errors.Is(err, bigcache.ErrEntryNotFound) {
		t.Fatalf("underlying cache still contains expired entry: %v", err)
	}
	stored, ok := store.Get(context.Background(), request.GetSequenceNumber())
	if !ok || stored.Owner != request.Owner || stored.ID != request.ID || !stored.TimeSent.Equal(request.TimeSent) {
		t.Fatalf("cache eviction lost a pending reservation: %+v, found=%t", stored, ok)
	}
	if length, err := store.Length(context.Background()); err != nil || length != 2 {
		t.Errorf("pending reservations after cache eviction = %d, %v, want 2", length, err)
	}
	if requests := store.List(context.Background()); len(requests) != 2 {
		t.Errorf("cache eviction dropped List reservations: %+v", requests)
	}
	removed, err := store.Delete(context.Background(), request)
	if err != nil || !removed {
		t.Errorf("claim evicted reservation = %t, %v", removed, err)
	}
}

func TestCustomStorePreservesRequestTimestamp(t *testing.T) {
	store := newTestCustomStore(t)
	request := gosmpp.Request{PDU: pdu.NewEnquireLink(), TimeSent: time.Unix(123456789, 987654321)}
	if err := store.Set(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	stored, ok := store.Get(context.Background(), request.GetSequenceNumber())
	if !ok {
		t.Fatal("stored request is missing")
	}
	if !stored.TimeSent.Equal(request.TimeSent) {
		t.Errorf("stored timestamp = %v, want %v", stored.TimeSent, request.TimeSent)
	}
}

func TestCustomStoreRejectsEncodingFailureWithoutAddingRequest(t *testing.T) {
	store := newTestCustomStore(t)
	request := gosmpp.Request{PDU: pdu.NewEnquireLink(), TimeSent: time.Unix(123456789, 0)}
	if err := store.Set(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	invalid := pdu.NewSubmitSM().(*pdu.SubmitSM)
	invalid.SetSequenceNumber(request.GetSequenceNumber() + 1)
	invalid.ServiceType = "invalid\x00service"
	if err := store.Set(context.Background(), gosmpp.Request{PDU: invalid, TimeSent: request.TimeSent}); !errors.Is(err, pdu.ErrEncode) {
		t.Errorf("Set error = %v, want encoding error", err)
	}
	stored, ok := store.Get(context.Background(), request.GetSequenceNumber())
	if !ok {
		t.Fatal("encoding failure replaced the valid request with corrupt data")
	}
	if length, err := store.Length(context.Background()); err != nil || length != 1 {
		t.Errorf("encoding failure added reservation: length=%d error=%v", length, err)
	}
	if _, ok := stored.PDU.(*pdu.EnquireLink); !ok {
		t.Errorf("encoding failure replaced the valid request with %T", stored.PDU)
	}
}

func TestCustomStoreListSkipsCorruptEntry(t *testing.T) {
	store := newTestCustomStore(t)
	request := gosmpp.Request{PDU: pdu.NewEnquireLink(), TimeSent: time.Unix(123456789, 0)}
	if err := store.Set(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if err := store.store.Set("corrupt", []byte{0xff}); err != nil {
		t.Fatal(err)
	}
	store.reservations["corrupt"] = []byte{0xff}
	requests := store.List(context.Background())
	if len(requests) != 1 || requests[0].PDU == nil {
		t.Errorf("List returned invalid reservations: %+v", requests)
	}
}

func TestCustomStoreClearRemovesRetainedReservations(t *testing.T) {
	store := newTestCustomStore(t)
	request := gosmpp.Request{PDU: pdu.NewEnquireLink(), TimeSent: time.Now(), Owner: 67, ID: 71}
	if err := store.Set(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if err := store.store.Delete(strconv.Itoa(int(request.GetSequenceNumber()))); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Get(context.Background(), request.GetSequenceNumber()); !ok {
		t.Fatal("cache eviction lost the retained reservation")
	}
	if err := store.Clear(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Get(context.Background(), request.GetSequenceNumber()); ok {
		t.Error("Clear retained a pending reservation")
	}
	if length, err := store.Length(context.Background()); err != nil || length != 0 {
		t.Errorf("Length after Clear = %d, %v", length, err)
	}
	if requests := store.List(context.Background()); len(requests) != 0 {
		t.Errorf("List after Clear = %+v", requests)
	}
	if removed, err := store.Delete(context.Background(), request); err != nil || removed {
		t.Errorf("Delete after Clear = %t, %v", removed, err)
	}
}

func TestCustomStorePreservesReservationMetadata(t *testing.T) {
	store := newTestCustomStore(t)
	request := gosmpp.Request{
		PDU:      pdu.NewEnquireLink(),
		TimeSent: time.Now(),
		Owner:    1<<63 + 17,
		ID:       1<<63 + 99,
	}
	if err := store.Set(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	stored, ok := store.Get(context.Background(), request.GetSequenceNumber())
	if !ok || stored.Owner != request.Owner || stored.ID != request.ID || !stored.TimeSent.Equal(request.TimeSent) {
		t.Fatalf("serialized reservation = %+v, want %+v", stored, request)
	}
	listed := store.List(context.Background())
	if len(listed) != 1 || listed[0].Owner != request.Owner || listed[0].ID != request.ID || !listed[0].TimeSent.Equal(request.TimeSent) {
		t.Errorf("listed reservation = %+v, want %+v", listed, request)
	}
	removed, err := store.Delete(context.Background(), request)
	if err != nil || !removed {
		t.Errorf("delete original reservation after serialization = %t, %v", removed, err)
	}
}

func TestCustomStoreConditionalDeletePreservesReplacement(t *testing.T) {
	changes := []struct {
		name   string
		change func(*gosmpp.Request)
	}{
		{name: "connection owner", change: func(request *gosmpp.Request) { request.Owner++ }},
		{name: "reservation ID", change: func(request *gosmpp.Request) { request.ID++ }},
	}
	for _, change := range changes {
		t.Run(change.name, func(t *testing.T) {
			store := newTestCustomStore(t)
			original := gosmpp.Request{PDU: pdu.NewEnquireLink(), TimeSent: time.Unix(123456789, 0), Owner: 3, ID: 4}
			if err := store.Set(context.Background(), original); err != nil {
				t.Fatal(err)
			}
			if removed, err := store.Delete(context.Background(), original); err != nil || !removed {
				t.Fatalf("remove original before replacement = %t, %v", removed, err)
			}
			replacement := original
			change.change(&replacement)
			if err := store.Set(context.Background(), replacement); err != nil {
				t.Fatal(err)
			}
			removed, err := store.Delete(context.Background(), original)
			if err != nil || removed {
				t.Errorf("stale reservation removed replacement = %t, %v", removed, err)
			}
			stored, ok := store.Get(context.Background(), original.GetSequenceNumber())
			if !ok || stored.Owner != replacement.Owner || stored.ID != replacement.ID || !stored.TimeSent.Equal(replacement.TimeSent) {
				t.Fatalf("replacement was lost: %+v", stored)
			}
			removed, err = store.Delete(context.Background(), replacement)
			if err != nil || !removed {
				t.Errorf("current reservation removal = %t, %v", removed, err)
			}
			removed, err = store.Delete(context.Background(), replacement)
			if err != nil || removed {
				t.Errorf("absent reservation removal = %t, %v", removed, err)
			}
		})
	}
}

func TestCustomStoreConditionalDeleteHasOneWinner(t *testing.T) {
	store := newTestCustomStore(t)
	request := gosmpp.Request{PDU: pdu.NewEnquireLink(), TimeSent: time.Now(), Owner: 31, ID: 47}
	if err := store.Set(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	type result struct {
		removed bool
		err     error
	}
	const contenders = 64
	start := make(chan struct{})
	results := make(chan result, contenders)
	var workers sync.WaitGroup
	workers.Add(contenders)
	for range contenders {
		copiedStore := store
		go func() {
			defer workers.Done()
			<-start
			removed, err := copiedStore.Delete(context.Background(), request)
			results <- result{removed: removed, err: err}
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	winners := 0
	for result := range results {
		if result.err != nil {
			t.Errorf("conditional deletion: %v", result.err)
		}
		if result.removed {
			winners++
		}
	}
	if winners != 1 {
		t.Errorf("completion winners = %d, want exactly one", winners)
	}
}
