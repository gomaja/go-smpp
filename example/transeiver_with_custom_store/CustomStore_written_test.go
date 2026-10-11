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

func TestCustomStoreMarkWrittenPreservesReservation(t *testing.T) {
	store := newTestCustomStore(t)
	marker, ok := any(store).(interface {
		MarkWritten(context.Context, gosmpp.Request, time.Time) (bool, error)
	})
	if !ok {
		t.Fatal("custom store cannot atomically transition a pending reservation to written")
	}
	request := gosmpp.Request{PDU: pdu.NewEnquireLink(), TimeSent: time.Unix(123456780, 0), Owner: 11, ID: 13}
	if err := store.Set(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	sent := time.Unix(123456789, 987654321)
	if marked, err := marker.MarkWritten(context.Background(), request, sent); err != nil || !marked {
		t.Fatalf("pending reservation: marked=%v error=%v", marked, err)
	}
	written, ok := store.Get(context.Background(), request.GetSequenceNumber())
	if !ok || written.Owner != request.Owner || written.ID != request.ID || !written.TimeSent.Equal(request.TimeSent) || !written.WrittenAt.Equal(sent) {
		t.Fatalf("written metadata did not survive serialization: %+v", written)
	}
	if marked, err := marker.MarkWritten(context.Background(), written, time.Now()); err != nil || marked {
		t.Fatalf("written timestamp changed twice: marked=%v error=%v", marked, err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := marker.MarkWritten(canceled, written, time.Now()); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled operation: %v", err)
	}
}

func TestCustomStoreMarkWrittenDoesNotRestoreCompletedRequest(t *testing.T) {
	store := newTestCustomStore(t)
	marker, ok := any(store).(interface {
		MarkWritten(context.Context, gosmpp.Request, time.Time) (bool, error)
	})
	if !ok {
		t.Fatal("custom store cannot atomically transition a pending reservation to written")
	}
	request := gosmpp.Request{PDU: pdu.NewEnquireLink(), Owner: 17, ID: 19}
	if marked, err := marker.MarkWritten(context.Background(), request, time.Now()); err != nil || marked {
		t.Fatalf("absent request resurrected: marked=%v error=%v", marked, err)
	}
	changes := []struct {
		name   string
		change func(*gosmpp.Request)
	}{
		{"owner", func(r *gosmpp.Request) { r.Owner++ }},
		{"identity", func(r *gosmpp.Request) { r.ID++ }},
	}
	for _, change := range changes {
		t.Run(change.name, func(t *testing.T) {
			store := newTestCustomStore(t)
			marker := store
			replacement := request
			change.change(&replacement)
			if err := store.Set(context.Background(), replacement); err != nil {
				t.Fatal(err)
			}
			if marked, err := marker.MarkWritten(context.Background(), request, time.Now()); err != nil || marked {
				t.Fatalf("replacement changed: marked=%v error=%v", marked, err)
			}
			stored, ok := store.Get(context.Background(), request.GetSequenceNumber())
			if !ok || stored.Owner != replacement.Owner || stored.ID != replacement.ID || !stored.TimeSent.Equal(replacement.TimeSent) {
				t.Fatal("stale write changed replacement metadata")
			}
		})
	}
}

func TestCustomStoreMarkWrittenHasOneWinner(t *testing.T) {
	store := newTestCustomStore(t)
	request := gosmpp.Request{PDU: pdu.NewEnquireLink(), Owner: 23, ID: 29}
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
