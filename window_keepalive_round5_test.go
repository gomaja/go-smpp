package gosmpp

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gomaja/go-smpp/pdu"
)

func TestWindowApplicationEnquiriesRetirePreviousUnansweredRequest(t *testing.T) {
	conn, cleanup := newPipePair()
	defer cleanup()
	store := NewDefaultStore()
	out := newTransmittable(conn, lifecycleSettings(), store)
	defer func() { _ = out.close(ExplicitClosing) }()
	var previous pdu.PDU
	for range 50 {
		current := pdu.NewEnquireLink()
		if _, err := out.write(current); err != nil {
			t.Fatal(err)
		}
		if previous != nil {
			if _, exists := store.Get(context.Background(), previous.GetSequenceNumber()); exists {
				t.Fatal("previous unanswered enquiry remained tracked")
			}
		}
		previous = current
	}
	if n, err := store.Length(context.Background()); err != nil || n != 1 {
		t.Fatalf("unanswered enquiry reservations=%d, %v", n, err)
	}
	if written := out.writtenCount(); written != 0 {
		t.Fatalf("enquiries consumed application window: %d", written)
	}
}

func TestWindowHousekeepingEnquirySurvivesStoreOutage(t *testing.T) {
	conn, _, frames := windowResponsePeer(t)
	store := &cleanupOutageStore{DefaultStore: NewDefaultStore()}
	settings := lifecycleSettings()
	settings.EnquireLink = time.Millisecond
	closed, failures := make(chan State, 1), make(chan error, 8)
	var unsent atomic.Int32
	settings.OnClosed = func(state State) { closed <- state }
	settings.OnReceivingError = func(err error) { failures <- err }
	settings.OnSubmitError = func(pdu.PDU, error) { unsent.Add(1) }
	out := newTransmittable(conn, settings, store)
	defer func() { store.outage.Store(false); _ = out.close(ExplicitClosing) }()
	// Install a real application enquiry before starting the ticker, then make
	// its retirement unavailable. The next keepalive must still reach the peer.
	if _, err := out.write(pdu.NewEnquireLink()); err != nil {
		t.Fatal(err)
	}
	_ = windowResponseFrame(t, frames)
	store.outage.Store(true)
	out.start()
	select {
	case frame := <-frames:
		if _, ok := frame.(*pdu.EnquireLink); !ok {
			t.Fatalf("housekeeping PDU=%T", frame)
		}
	case state := <-closed:
		t.Fatalf("store outage closed bind at keepalive: %v", state)
	case <-time.After(2 * time.Second):
		t.Fatal("housekeeping enquiry did not reach the peer")
	}
	select {
	case <-failures:
	case <-time.After(time.Second):
		t.Fatal("failed retirement was not reported via OnReceivingError")
	}
	if store.sets.Load() != 1 || unsent.Load() != 0 || atomic.LoadInt32(&out.aliveState) != Alive {
		t.Fatalf("housekeeping used request store or dropped bind: Set=%d unsent=%d alive=%d", store.sets.Load(), unsent.Load(), atomic.LoadInt32(&out.aliveState))
	}
}

func TestWindowHousekeepingEnquiriesRemainUntracked(t *testing.T) {
	conn, _, frames := windowResponsePeer(t)
	store := &cleanupOutageStore{DefaultStore: NewDefaultStore()}
	settings := lifecycleSettings()
	settings.EnquireLink = time.Millisecond
	out := newTransmittable(conn, settings, store)
	out.start()
	defer func() { _ = out.close(ExplicitClosing) }()
	for range 5 {
		if _, ok := windowResponseFrame(t, frames).(*pdu.EnquireLink); !ok {
			t.Fatal("not an enquiry")
		}
	}
	if store.sets.Load() != 0 {
		t.Fatalf("housekeeping enquiries created %d reservations", store.sets.Load())
	}
	if n, _ := store.Length(context.Background()); n != 0 {
		t.Fatalf("housekeeping left %d rows", n)
	}
}
