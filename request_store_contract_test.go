package gosmpp

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gomaja/go-smpp/pdu"
)

func TestDefaultStoreDuplicateSequenceHasOneReservation(t *testing.T) {
	store := NewDefaultStore()
	const contenders = 64
	type result struct {
		request Request
		err     error
	}
	results := make(chan result, contenders)
	start := make(chan struct{})
	var workers sync.WaitGroup
	for i := range contenders {
		request := Request{PDU: pdu.NewEnquireLink(), Owner: 73, ID: uint64(i + 1), TimeSent: time.Unix(100, int64(i))}
		request.SetSequenceNumber(71)
		copiedStore := store
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			results <- result{request, copiedStore.Set(context.Background(), request)}
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	winners := 0
	var winner Request
	for result := range results {
		if result.err == nil {
			winners++
			winner = result.request
		} else if !errors.Is(result.err, ErrSequenceInUse) {
			t.Errorf("duplicate Set error = %v, want ErrSequenceInUse", result.err)
		}
	}
	if winners != 1 {
		t.Fatalf("successful duplicate reservations = %d, want 1", winners)
	}
	stored, exists := store.Get(context.Background(), 71)
	if !exists || stored.Owner != winner.Owner || stored.ID != winner.ID || !stored.TimeSent.Equal(winner.TimeSent) {
		t.Fatalf("winning reservation replaced: %+v", stored)
	}
}

func TestDefaultStoreDeletePendingSnapshotAfterWritten(t *testing.T) {
	store := NewDefaultStore()
	reserved := Request{PDU: pdu.NewEnquireLink(), Owner: 79, ID: 83}
	if err := store.Set(context.Background(), reserved); err != nil {
		t.Fatal(err)
	}
	snapshot, exists := store.Get(context.Background(), reserved.GetSequenceNumber())
	if !exists {
		t.Fatal("reservation missing")
	}
	if marked, err := store.MarkWritten(context.Background(), reserved, time.Unix(120, 7)); err != nil || !marked {
		t.Fatalf("mark written = %t, %v", marked, err)
	}
	if removed, err := store.Delete(context.Background(), snapshot); err != nil || !removed {
		t.Fatalf("pending snapshot must delete its written identity = %t, %v", removed, err)
	}
	if marked, err := store.MarkWritten(context.Background(), reserved, time.Now()); err != nil || marked {
		t.Fatalf("removed reservation resurrected = %t, %v", marked, err)
	}
}

func TestDefaultStoreWrittenAtPreservesReservationTime(t *testing.T) {
	store := NewDefaultStore()
	reserved := Request{PDU: pdu.NewEnquireLink(), TimeSent: time.Unix(123, 456), Owner: 89, ID: 97, Lineage: 91}
	if err := store.Set(context.Background(), reserved); err != nil {
		t.Fatal(err)
	}
	pending, exists := store.Get(context.Background(), reserved.GetSequenceNumber())
	if !exists || !pending.WrittenAt.IsZero() {
		t.Fatal("new reservation is already written")
	}
	writtenAt := reserved.TimeSent.Add(time.Second)
	if marked, err := store.MarkWritten(context.Background(), reserved, writtenAt); err != nil || !marked {
		t.Fatalf("timestamp transition = %t, %v", marked, err)
	}
	written, exists := store.Get(context.Background(), reserved.GetSequenceNumber())
	if !exists || written.Lineage != reserved.Lineage || !written.TimeSent.Equal(reserved.TimeSent) || !written.WrittenAt.Equal(writtenAt) {
		t.Fatalf("timestamp transition changed reservation metadata: %+v", written)
	}
	listed := store.List(context.Background())
	if len(listed) != 1 || listed[0].Lineage != reserved.Lineage || !listed[0].TimeSent.Equal(reserved.TimeSent) || !listed[0].WrittenAt.Equal(writtenAt) {
		t.Fatalf("List lost written metadata: %+v", listed)
	}
	if marked, err := store.MarkWritten(context.Background(), reserved, writtenAt.Add(time.Second)); err != nil || marked {
		t.Fatalf("second timestamp transition = %t, %v", marked, err)
	}
}

func TestDefaultStoreReservationIdentityGuardsCompletion(t *testing.T) {
	changes := []struct {
		name   string
		change func(*Request)
	}{
		{"owner", func(r *Request) { r.Owner++ }},
		{"reservation ID", func(r *Request) { r.ID++ }},
		{"sequence", func(r *Request) {
			other := pdu.NewEnquireLink()
			other.SetSequenceNumber(r.GetSequenceNumber() + 1)
			r.PDU = other
		}},
	}
	for _, change := range changes {
		t.Run(change.name, func(t *testing.T) {
			store := NewDefaultStore()
			reserved := Request{PDU: pdu.NewEnquireLink(), Owner: 101, ID: 103}
			if err := store.Set(context.Background(), reserved); err != nil {
				t.Fatal(err)
			}
			stale := reserved
			change.change(&stale)
			if marked, err := store.MarkWritten(context.Background(), stale, time.Now()); err != nil || marked {
				t.Fatalf("different identity marked written = %t, %v", marked, err)
			}
			if removed, err := store.Delete(context.Background(), stale); err != nil || removed {
				t.Fatalf("different identity removed = %t, %v", removed, err)
			}
			if stored, exists := store.Get(context.Background(), reserved.GetSequenceNumber()); !exists || stored.Owner != reserved.Owner || stored.ID != reserved.ID {
				t.Fatalf("identity guard lost original: %+v", stored)
			}
		})
	}
}

func TestDefaultStoreSetHonorsCancellation(t *testing.T) {
	store := NewDefaultStore()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request := Request{PDU: pdu.NewEnquireLink(), Owner: 107, ID: 109}
	if err := store.Set(ctx, request); err != context.Canceled {
		t.Fatalf("canceled Set = %v", err)
	}
	if length, err := store.Length(context.Background()); err != nil || length != 0 {
		t.Fatalf("canceled Set changed store = %d, %v", length, err)
	}
}

func TestDefaultStoreMarkWrittenDoesNotChangeReplacement(t *testing.T) {
	store := NewDefaultStore()
	original := Request{PDU: pdu.NewEnquireLink(), Owner: 113, ID: 127}
	if err := store.Set(context.Background(), original); err != nil {
		t.Fatal(err)
	}
	if removed, err := store.Delete(context.Background(), original); err != nil || !removed {
		t.Fatalf("remove original = %t, %v", removed, err)
	}
	replacement := original
	replacement.ID++
	if err := store.Set(context.Background(), replacement); err != nil {
		t.Fatal(err)
	}
	if marked, err := store.MarkWritten(context.Background(), original, time.Now()); err != nil || marked {
		t.Fatalf("stale write changed replacement = %t, %v", marked, err)
	}
	if removed, err := store.Delete(context.Background(), original); err != nil || removed {
		t.Fatalf("stale delete removed replacement = %t, %v", removed, err)
	}
	if stored, exists := store.Get(context.Background(), replacement.GetSequenceNumber()); !exists || stored.ID != replacement.ID || !stored.TimeSent.Equal(replacement.TimeSent) {
		t.Fatalf("replacement lost: %+v", stored)
	}
}

func TestDefaultStoreCompletionUsesIdentityWithoutTimestamp(t *testing.T) {
	store := NewDefaultStore()
	reserved := Request{PDU: pdu.NewEnquireLink(), TimeSent: time.Unix(130, 7), Owner: 131, ID: 137}
	if err := store.Set(context.Background(), reserved); err != nil {
		t.Fatal(err)
	}
	snapshot := reserved
	snapshot.TimeSent = snapshot.TimeSent.Add(time.Second)
	if marked, err := store.MarkWritten(context.Background(), snapshot, time.Unix(140, 11)); err != nil || !marked {
		t.Fatalf("timestamp metadata changed identity = %t, %v", marked, err)
	}
	if stored, exists := store.Get(context.Background(), reserved.GetSequenceNumber()); !exists || !stored.TimeSent.Equal(reserved.TimeSent) {
		t.Fatalf("mark changed stored reservation time: %+v", stored)
	}
	if removed, err := store.Delete(context.Background(), snapshot); err != nil || !removed {
		t.Fatalf("timestamp metadata prevented identity deletion = %t, %v", removed, err)
	}
}

func TestDefaultStoreMarkWrittenHasOneWinner(t *testing.T) {
	store := NewDefaultStore()
	request := Request{PDU: pdu.NewEnquireLink(), Owner: 23, ID: 29}
	if err := store.Set(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	const contenders = 32
	start := make(chan struct{})
	results := make(chan bool, contenders)
	for range contenders {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			marked, err := store.MarkWritten(context.Background(), request, time.Now())
			if err != nil {
				t.Errorf("mark written: %v", err)
			}
			results <- marked
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
		t.Fatalf("written-state winners=%d, want1", winners)
	}
}
