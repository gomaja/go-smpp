package gosmpp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gomaja/go-smpp/pdu"
)

type windowCountStore struct {
	DefaultStore
	lists atomic.Int32
}

func (s *windowCountStore) List(ctx context.Context) []Request {
	s.lists.Add(1)
	return s.DefaultStore.List(ctx)
}

func TestWindowOrdinaryWriteDoesNotScanStore(t *testing.T) {
	conn, cleanup := newPipePair()
	defer cleanup()
	store := &windowCountStore{DefaultStore: NewDefaultStore()}
	out := newTransmittable(conn, lifecycleSettings(), store)
	defer func() { _ = out.close(ExplicitClosing) }()
	for range 3 {
		if _, err := out.write(pdu.NewSubmitSM()); err != nil {
			t.Fatal(err)
		}
	}
	if got := store.lists.Load(); got != 0 {
		t.Fatalf("ordinary writes scanned store %d times", got)
	}
}

func TestWindowReservationPreservesSubmissionTime(t *testing.T) {
	v, gate, store := newWindowGatedWriter(t, lifecycleSettings(), nil)
	request := pdu.NewSubmitSM()
	done := make(chan struct{})
	before := time.Now()
	go func() { defer close(done); _, _ = v.out.write(request) }()
	lifecycleWait(t, gate.entered)
	reserved, exists := store.Get(context.Background(), request.GetSequenceNumber())
	if !exists || reserved.TimeSent.Before(before) || !reserved.WrittenAt.IsZero() {
		t.Errorf("reservation lacks submission timestamp: %+v", reserved)
	}
	writeReleased := time.Now()
	close(gate.release)
	lifecycleWait(t, done)
	written, exists := store.Get(context.Background(), request.GetSequenceNumber())
	if !exists || !written.TimeSent.Equal(reserved.TimeSent) || written.WrittenAt.Before(writeReleased) {
		t.Errorf("write completion changed original submission time: before=%v after=%v", reserved.TimeSent, written.TimeSent)
	}
}

func TestWindowWriterCallbackRejectsOwnFullQueue(t *testing.T) {
	for _, useContext := range []bool{false, true} {
		t.Run(fmt.Sprint(useContext), func(t *testing.T) {
			conn, cleanup := newPipePair()
			defer cleanup()
			settings := lifecycleSettings()
			settings.MaxWindowSize = 1
			store := NewDefaultStore()
			if err := store.Set(context.Background(), Request{PDU: pdu.NewSubmitSM(), WrittenAt: time.Now()}); err != nil {
				t.Fatal(err)
			}
			entered, release, returned := make(chan struct{}), make(chan struct{}), make(chan error, 1)
			var out *transmittable
			var once atomic.Bool
			settings.OnSubmitError = func(pdu.PDU, error) {
				if !once.CompareAndSwap(false, true) {
					return
				}
				close(entered)
				<-release
				if useContext {
					ctx, cancel := context.WithTimeout(context.Background(), time.Second)
					defer cancel()
					returned <- out.SubmitContext(ctx, pdu.NewSubmitSM())
				} else {
					returned <- out.Submit(pdu.NewSubmitSM())
				}
			}
			out = newTransmittable(conn, settings, store)
			out.start()
			defer func() { _ = out.close(ExplicitClosing) }()
			if err := out.Submit(pdu.NewSubmitSM()); err != nil {
				t.Fatal(err)
			}
			lifecycleWait(t, entered)
			if err := out.Submit(pdu.NewSubmitSM()); err != nil {
				t.Fatal(err)
			}
			close(release)
			select {
			case err := <-returned:
				if !errors.Is(err, ErrSubmitQueueFull) {
					t.Fatalf("own full queue must immediately reject, got %v", err)
				}
			case <-time.After(300 * time.Millisecond):
				out.stop(ExplicitClosing)
				t.Fatalf("writer callback waited for its own queue; after stop it returned %v", <-returned)
			}
		})
	}
}

func TestWindowDuplicateSequenceRetainsOriginalReservation(t *testing.T) {
	conn, cleanup := newPipePair()
	defer cleanup()
	store := NewDefaultStore()
	out := newTransmittable(conn, lifecycleSettings(), store)
	defer func() { _ = out.close(ExplicitClosing) }()
	first, second := pdu.NewSubmitSM(), pdu.NewSubmitSM()
	second.SetSequenceNumber(first.GetSequenceNumber())
	if _, err := out.write(first); err != nil {
		t.Fatal(err)
	}
	original, _ := store.Get(context.Background(), first.GetSequenceNumber())
	n, err := out.write(second)
	if !errors.Is(err, ErrSequenceInUse) || n != 0 {
		t.Errorf("duplicate sequence result: bytes=%d error=%v", n, err)
	} else if out.check(second, n, err) {
		t.Error("duplicate sequence closed healthy transport")
	}
	retained, ok := store.Get(context.Background(), first.GetSequenceNumber())
	if !ok || retained.ID != original.ID || retained.PDU != first {
		t.Errorf("original correlation replaced: %+v", retained)
	}
}

func BenchmarkTrackedWrite(b *testing.B) {
	for _, occupancy := range []int{0, 100, 250} {
		b.Run(fmt.Sprint(occupancy), func(b *testing.B) {
			conn, cleanup := newPipePair()
			defer cleanup()
			settings := lifecycleSettings()
			settings.MaxWindowSize = 255
			store := NewDefaultStore()
			out := newTransmittable(conn, settings, store)
			defer func() { _ = out.close(ExplicitClosing) }()
			ctx := context.Background()
			for range occupancy {
				if err := store.Set(ctx, Request{PDU: pdu.NewSubmitSM(), TimeSent: time.Now(), WrittenAt: time.Now()}); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				request := pdu.NewSubmitSM()
				if _, err := out.write(request); err != nil {
					b.Fatal(err)
				}
				entry, ok := store.Get(ctx, request.GetSequenceNumber())
				if !ok {
					b.Fatal("reservation missing")
				}
				if _, err := store.Delete(ctx, entry); err != nil {
					b.Fatal(err)
				}
				forgetRequest(entry)
			}
			b.StopTimer()
		})
	}
}

func TestWindowSharedSessionsPreserveOrphanLineageAcrossRebind(t *testing.T) {
	store := &windowDeadOwnerStore{DefaultStore: NewDefaultStore()}
	firstConn, cleanFirst := newPipePair()
	defer cleanFirst()
	reboundConn, cleanRebound := newPipePair()
	defer cleanRebound()
	otherConn, cleanOther := newPipePair()
	defer cleanOther()
	var calls atomic.Int32
	connector := lifecycleConnector{connect: func() (*Connection, error) {
		if calls.Add(1) == 1 {
			return firstConn, nil
		}
		return reboundConn, nil
	}}
	settings := lifecycleSettings()
	settings.MaxWindowSize = 1
	orphan := pdu.NewSubmitSM()
	reclaimed := make(chan struct{}, 1)
	settings.OnClosePduRequest = func(request pdu.PDU) {
		if request == orphan {
			reclaimed <- struct{}{}
		}
	}
	first, err := NewSession(connector, settings, 0, WithRequestStore(store))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Close() }()
	var stolen atomic.Int32
	otherWindow := *settings.WindowedRequestTracking
	settings.WindowedRequestTracking = &otherWindow
	settings.OnClosePduRequest = func(request pdu.PDU) {
		if request == orphan {
			stolen.Add(1)
		}
	}
	settings.PduExpireTimeOut = time.Nanosecond
	settings.ExpireCheckTimer = time.Hour
	settings.OnExpiredPduRequest = func(request pdu.PDU) bool {
		if request == orphan {
			stolen.Add(1)
		}
		return false
	}
	other, err := NewSession(lifecycleConnector{connect: func() (*Connection, error) { return otherConn, nil }}, settings, 0, WithRequestStore(store))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	old := first.bound()
	if _, err = old.out.write(orphan); err != nil {
		t.Fatal(err)
	}
	store.failDeletes.Store(true)
	if err = old.Close(); err != nil {
		t.Fatal(err)
	}
	store.failDeletes.Store(false)
	other.bound().expireRequests()
	if _, err = other.bound().out.write(pdu.NewSubmitSM()); !errors.Is(err, ErrWindowsFull) {
		t.Errorf("other Session reclaimed orphan capacity: %v", err)
	}
	if stolen.Load() != 0 {
		t.Errorf("other Session delivered %d orphan callbacks", stolen.Load())
	}
	first.rebind()
	if first.bound() == old {
		t.Fatal("Session did not publish rebound transport")
	}
	first.bound().out.reclaimDeadRequests()
	first.bound().out.reclaimWG.Wait()
	if _, err = first.bound().out.write(pdu.NewSubmitSM()); err != nil {
		t.Fatalf("own Session failed to reclaim previous generation: %v", err)
	}
	select {
	case <-reclaimed:
	case <-time.After(time.Second):
		t.Fatal("rebound Session lost orphan completion")
	}
	if stolen.Load() != 0 {
		t.Fatalf("foreign callbacks=%d", stolen.Load())
	}
}

func TestWindowOrphanCallbackCanSubmitBeyondQueueCapacity(t *testing.T) {
	store := &windowDeadOwnerStore{DefaultStore: NewDefaultStore()}
	firstConn, cleanFirst := newPipePair()
	defer cleanFirst()
	nextConn, cleanNext := newPipePair()
	defer cleanNext()
	settings := lifecycleSettings()
	orphan := pdu.NewSubmitSM()
	old := newTransceivable(firstConn, settings, store)
	if _, err := old.out.write(orphan); err != nil {
		t.Fatal(err)
	}
	store.failDeletes.Store(true)
	retireWindowGeneration(t, old)
	store.failDeletes.Store(false)
	results := make(chan error, 1)
	var live *transceivable
	settings.OnClosePduRequest = func(request pdu.PDU) {
		if request != orphan {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		for range 2 {
			if err := live.SubmitContext(ctx, pdu.NewSubmitSM()); err != nil {
				results <- err
				return
			}
		}
		results <- nil
	}
	live = newTransceivable(nextConn, settings, store)
	live.out.lineage = old.out.lineage
	live.start()
	defer func() { _ = live.Close() }()
	if err := live.Submit(pdu.NewSubmitSM()); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-results:
		if err != nil {
			t.Fatalf("orphan callback blocked writer while refilling queue: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("orphan callback did not finish")
	}
}

func TestWindowSubmitRejectsDuplicateReservationSynchronously(t *testing.T) {
	conn, cleanup := newPipePair()
	defer cleanup()
	out := newTransmittable(conn, lifecycleSettings(), NewDefaultStore())
	defer func() { _ = out.close(ExplicitClosing) }()
	original := pdu.NewSubmitSM()
	if _, err := out.write(original); err != nil {
		t.Fatal(err)
	}
	const contenders = 12
	start := make(chan struct{})
	results := make(chan error, contenders)
	for range contenders {
		go func() {
			<-start
			duplicate := pdu.NewSubmitSM()
			duplicate.SetSequenceNumber(original.GetSequenceNumber())
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			results <- out.SubmitContext(ctx, duplicate)
		}()
	}
	close(start)
	for range contenders {
		if err := <-results; !errors.Is(err, ErrSequenceInUse) {
			t.Errorf("duplicate Submit was accepted or waited: %v", err)
		}
	}
}

type windowAdmissionStore struct {
	*windowDeadOwnerStore
	admissions chan Request
}

func (s *windowAdmissionStore) Set(ctx context.Context, request Request) error {
	err := s.DefaultStore.Set(ctx, request)
	if err == nil {
		s.admissions <- request
	}
	return err
}

func TestWindowCanceledAdmissionNeverCompletesAsAccepted(t *testing.T) {
	for _, testCase := range []struct{ failDelete, response bool }{{false, false}, {true, false}, {true, true}} {
		t.Run(fmt.Sprintf("delete_failure=%v/response=%v", testCase.failDelete, testCase.response), func(t *testing.T) {
			conn, cleanup := newPipePair()
			defer cleanup()
			store := &windowAdmissionStore{windowDeadOwnerStore: &windowDeadOwnerStore{DefaultStore: NewDefaultStore()}, admissions: make(chan Request, 2)}
			settings := lifecycleSettings()
			var submits, closes, responses atomic.Int32
			settings.OnSubmitError = func(pdu.PDU, error) { submits.Add(1) }
			settings.OnClosePduRequest = func(pdu.PDU) { closes.Add(1) }
			settings.OnExpectedPduResponse = func(Response) { responses.Add(1) }
			v := newTransceivable(conn, settings, store)
			out := v.out
			first, second := pdu.NewSubmitSM(), pdu.NewSubmitSM()
			if err := out.Submit(first); err != nil {
				t.Fatal(err)
			}
			<-store.admissions
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan error, 1)
			go func() { result <- out.SubmitContext(ctx, second) }()
			var reservation Request
			select {
			case reservation = <-store.admissions:
			case <-time.After(time.Second):
				t.Fatal("waiting producer never reserved sequence")
			}
			if !reservation.WrittenAt.IsZero() || reservation.TimeSent.IsZero() {
				t.Fatalf("pending admission metadata: %+v", reservation)
			}
			store.failDeletes.Store(testCase.failDelete)
			cancel()
			if err := <-result; !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled admission: %v", err)
			}
			store.failDeletes.Store(false)
			if testCase.response {
				v.in.completeResponse(second.GetResponse(), second.GetSequenceNumber(), nil)
			}
			if err := v.Close(); err != nil {
				t.Fatal(err)
			}
			if size, _ := store.Length(context.Background()); size != 0 {
				t.Fatalf("canceled or drained admission stranded %d reservations", size)
			}
			if submits.Load() != 1 || closes.Load() != 0 || responses.Load() != 0 {
				t.Fatalf("completion counts submit=%d close=%d response=%d", submits.Load(), closes.Load(), responses.Load())
			}
		})
	}
}

func TestWindowConcurrentSharedSessionSubmitHasSingleSequenceWinner(t *testing.T) {
	store := NewDefaultStore()
	aConn, cleanA := newPipePair()
	defer cleanA()
	bConn, cleanB := newPipePair()
	defer cleanB()
	settings := lifecycleSettings()
	var submitErrors atomic.Int32
	settings.OnSubmitError = func(pdu.PDU, error) { submitErrors.Add(1) }
	a, err := NewSession(lifecycleConnector{connect: func() (*Connection, error) { return aConn, nil }}, settings, 0, WithRequestStore(store))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close() }()
	b, err := NewSession(lifecycleConnector{connect: func() (*Connection, error) { return bConn, nil }}, settings, 0, WithRequestStore(store))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, session := range []*Session{a, b} {
		go func() {
			<-start
			p := pdu.NewSubmitSM()
			p.SetSequenceNumber(87431)
			results <- session.Transceiver().Submit(p)
		}()
	}
	close(start)
	successes, duplicates := 0, 0
	for range 2 {
		err = <-results
		if err == nil {
			successes++
		} else if errors.Is(err, ErrSequenceInUse) {
			duplicates++
		} else {
			t.Fatalf("Submit: %v", err)
		}
	}
	if successes != 1 || duplicates != 1 || submitErrors.Load() != 0 {
		t.Fatalf("success=%d duplicate=%d callback=%d", successes, duplicates, submitErrors.Load())
	}
	if size, _ := store.Length(context.Background()); size != 1 {
		t.Fatalf("shared sequence stored %d reservations", size)
	}
}

func TestWindowUnsentWriteReportsFailureDespiteDeleteError(t *testing.T) {
	for _, recovery := range []string{"reclaim", "expiry"} {
		t.Run(recovery, func(t *testing.T) {
			conn, cleanup := newPipePair()
			defer cleanup()
			gate := &windowWriteGate{Conn: conn.conn, entered: make(chan struct{}), release: make(chan struct{}), fail: io.ErrClosedPipe}
			close(gate.release)
			store := &windowDeadOwnerStore{DefaultStore: NewDefaultStore()}
			settings := lifecycleSettings()
			var submits, closes, expired atomic.Int32
			settings.OnSubmitError = func(pdu.PDU, error) { submits.Add(1) }
			settings.OnClosePduRequest = func(pdu.PDU) { closes.Add(1) }
			settings.OnExpiredPduRequest = func(pdu.PDU) bool { expired.Add(1); return false }
			out := newTransmittable(NewConnection(gate), settings, store)
			// A replacement generation belongs to a still-live Session lineage.
			out.lineage.managed = true
			request := pdu.NewSubmitSM()
			store.failDeletes.Store(true)
			n, err := out.write(request)
			if !errors.Is(err, io.ErrClosedPipe) {
				t.Fatalf("write error: %v", err)
			}
			out.check(request, n, err)
			if err := out.close(ExplicitClosing); err != nil {
				t.Fatal(err)
			}
			store.failDeletes.Store(false)
			nextConn, cleanNext := newPipePair()
			defer cleanNext()
			v := newTransceivable(nextConn, settings, store)
			next := v.out
			next.lineage = out.lineage
			defer func() { _ = v.Close() }()
			if recovery == "expiry" {
				v.settings.PduExpireTimeOut = time.Nanosecond
				v.expireRequests()
			}
			if _, err := next.write(pdu.NewSubmitSM()); err != nil {
				t.Fatal(err)
			}
			next.reclaimWG.Wait()
			if submits.Load() != 1 || closes.Load() != 0 || expired.Load() != 0 {
				t.Fatalf("unsent completion submit=%d close=%d expiry=%d", submits.Load(), closes.Load(), expired.Load())
			}
		})
	}
}

func TestWindowReceivingErrorWriterCallbackRejectsOwnFullQueue(t *testing.T) {
	conn, cleanup := newPipePair()
	defer cleanup()
	settings := lifecycleSettings()
	store := windowMarkFailureStore{NewDefaultStore()}
	entered, release := make(chan struct{}), make(chan struct{})
	result := make(chan error, 1)
	var out *transmittable
	var once atomic.Bool
	settings.OnReceivingError = func(error) {
		if !once.CompareAndSwap(false, true) {
			return
		}
		close(entered)
		<-release
		result <- out.Submit(pdu.NewSubmitSM())
	}
	out = newTransmittable(conn, settings, store)
	out.start()
	defer func() { _ = out.close(ExplicitClosing) }()
	if err := out.Submit(pdu.NewSubmitSM()); err != nil {
		t.Fatal(err)
	}
	lifecycleWait(t, entered)
	if err := out.Submit(pdu.NewSubmitSM()); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case err := <-result:
		if !errors.Is(err, ErrSubmitQueueFull) {
			t.Fatalf("writer receiving callback submission: %v", err)
		}
	case <-time.After(300 * time.Millisecond):
		out.stop(ExplicitClosing)
		t.Fatalf("receiving callback waited for its own writer; after stop it returned %v", <-result)
	}
}

func TestWindowDuplicateSubmitNeverReachesWire(t *testing.T) {
	client, peer := net.Pipe()
	frames := make(chan pdu.PDU, 4)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			packet, err := pdu.Parse(peer)
			if err != nil {
				return
			}
			frames <- packet
		}
	}()
	store := NewDefaultStore()
	session, err := NewSession(lifecycleConnector{connect: func() (*Connection, error) { return NewConnection(client), nil }}, lifecycleSettings(), 0, WithRequestStore(store))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close(); _ = peer.Close(); <-readerDone }()
	receive := func() pdu.PDU {
		select {
		case packet := <-frames:
			return packet
		case <-time.After(time.Second):
			t.Fatal("wire PDU missing")
			return nil
		}
	}
	first := pdu.NewSubmitSM()
	if err = session.Transceiver().Submit(first); err != nil {
		t.Fatal(err)
	}
	if got := receive(); got.GetSequenceNumber() != first.GetSequenceNumber() {
		t.Fatalf("initial sequence %d", got.GetSequenceNumber())
	}
	duplicate := pdu.NewSubmitSM()
	duplicate.SetSequenceNumber(first.GetSequenceNumber())
	if err = session.Transceiver().Submit(duplicate); !errors.Is(err, ErrSequenceInUse) {
		t.Fatalf("duplicate Submit: %v", err)
	}
	sentinel := pdu.NewSubmitSM()
	if err = session.Transceiver().Submit(sentinel); err != nil {
		t.Fatal(err)
	}
	if got := receive(); got.GetSequenceNumber() != sentinel.GetSequenceNumber() {
		t.Fatalf("duplicate reached wire before sentinel: sequence %d", got.GetSequenceNumber())
	}
	stored, exists := store.Get(context.Background(), first.GetSequenceNumber())
	if !exists || stored.PDU != first {
		t.Fatalf("duplicate replaced original correlation: %+v", stored)
	}
}

func TestWindowQueuedReservationUsesItsOwnSlot(t *testing.T) {
	client, peer := net.Pipe()
	settings := lifecycleSettings()
	settings.MaxWindowSize = 1
	out := newTransmittable(NewConnection(client), settings, NewDefaultStore())
	out.start()
	defer func() { _ = peer.Close(); _ = out.close(ExplicitClosing) }()
	request := pdu.NewSubmitSM()
	if err := out.Submit(request); err != nil {
		t.Fatal(err)
	}
	if err := peer.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	got, err := pdu.Parse(peer)
	if err != nil {
		t.Fatalf("own pending reservation filled the only window slot: %v", err)
	}
	if got.GetSequenceNumber() != request.GetSequenceNumber() {
		t.Fatalf("sequence=%d", got.GetSequenceNumber())
	}
}

func TestWindowDrainDeleteFailureDoesNotDoubleComplete(t *testing.T) {
	conn, cleanup := newPipePair()
	defer cleanup()
	store := &windowOperationStore{DefaultStore: NewDefaultStore()}
	settings := lifecycleSettings()
	var submitted, closed, reported atomic.Int32
	settings.OnSubmitError = func(pdu.PDU, error) { submitted.Add(1) }
	settings.OnClosePduRequest = func(pdu.PDU) { closed.Add(1) }
	settings.OnReceivingError = func(error) { reported.Add(1) }
	out := newTransmittable(conn, settings, store)
	if err := out.Submit(pdu.NewSubmitSM()); err != nil {
		t.Fatal(err)
	}
	store.failDeletes.Store(1)
	if err := out.close(ExplicitClosing); err != nil {
		t.Fatal(err)
	}
	if size, _ := store.Length(context.Background()); size != 0 {
		t.Fatalf("retry left %d reservations", size)
	}
	if submitted.Load() != 1 || closed.Load() != 0 || reported.Load() != 1 {
		t.Fatalf("drain completions submit=%d close=%d storeError=%d", submitted.Load(), closed.Load(), reported.Load())
	}
}
