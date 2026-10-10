package gosmpp

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gomaja/go-smpp/pdu"
)

// Failed acknowledgements leave a remote operation which the test can commit
// after a complete recovery scan has observed its absence.
type windowUncertaintyLateStore struct {
	DefaultStore
	outage bool
	late   Request
}

func (s *windowUncertaintyLateStore) Set(ctx context.Context, request Request) error {
	if s.outage {
		s.late = request
		return context.DeadlineExceeded
	}
	return s.DefaultStore.Set(ctx, request)
}
func (s *windowUncertaintyLateStore) Delete(ctx context.Context, request Request) (bool, error) {
	if s.outage {
		return false, context.DeadlineExceeded
	}
	return s.DefaultStore.Delete(ctx, request)
}

func TestWindowUncertaintyLateCommitAfterAbsenceIsSilent(t *testing.T) {
	for _, cleanupPath := range []string{"recovery", "expiry", "close"} {
		t.Run(cleanupPath, func(t *testing.T) {
			conn, cleanup := newPipePair()
			defer cleanup()
			store := &windowUncertaintyLateStore{DefaultStore: NewDefaultStore(), outage: true}
			settings := lifecycleSettings()
			var closed, expired, responded atomic.Int32
			settings.OnClosePduRequest = func(pdu.PDU) { closed.Add(1) }
			settings.OnExpiredPduRequest = func(pdu.PDU) bool { expired.Add(1); return false }
			settings.OnExpectedPduResponse = func(Response) { responded.Add(1) }
			v := newTransceivable(conn, settings, store)
			defer func() { _ = v.Close() }()
			rejected := pdu.NewSubmitSM()
			if err := v.Submit(rejected); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("ambiguous remote Set: got %v, want deadline error", err)
			}
			store.outage = false
			forceCleanupScan(v.out)
			if _, present := store.Get(context.Background(), rejected.GetSequenceNumber()); present {
				t.Fatal("remote operation committed before absence recovery")
			}
			if err := store.DefaultStore.Set(context.Background(), store.late); err != nil {
				t.Fatal(err)
			}
			// An unsolicited response cannot turn the rejected admission into a
			// successful completion, even after the remote reservation appears.
			v.in.handleWindowPdu(rejected.GetResponse())
			switch cleanupPath {
			case "recovery":
				forceCleanupScan(v.out)
			case "expiry":
				v.expireRequests()
			case "close":
				_ = v.Close()
			}
			if _, present := store.Get(context.Background(), rejected.GetSequenceNumber()); present {
				t.Errorf("late remote reservation survived %s cleanup", cleanupPath)
			}
			_ = v.Close()
			if closed.Load() != 0 || expired.Load() != 0 || responded.Load() != 0 {
				t.Errorf("rejected submission produced completion callbacks: close=%d expiry=%d response=%d", closed.Load(), expired.Load(), responded.Load())
			}
		})
	}
}

type windowUncertaintyArrivalStore struct {
	DefaultStore
	arrived chan struct{}
}

func (s *windowUncertaintyArrivalStore) Set(ctx context.Context, request Request) error {
	err := s.DefaultStore.Set(ctx, request)
	s.arrived <- struct{}{}
	return err
}

func TestWindowUncertaintyHealthyConcurrentProducersWait(t *testing.T) {
	const producers = 1200
	client, peer := net.Pipe()
	store := &windowUncertaintyArrivalStore{DefaultStore: NewDefaultStore(), arrived: make(chan struct{}, producers)}
	settings := lifecycleSettings()
	settings.MaxWindowSize = 10
	settings.StoreAccessTimeOut = 15 * time.Second
	settings.WriteTimeout = 15 * time.Second
	responses := make(chan struct{}, producers)
	settings.OnExpectedPduResponse = func(Response) { responses <- struct{}{} }
	v := newTransceivable(NewConnection(client), settings, store)
	v.start()
	ctx, cancel := context.WithCancel(context.Background())
	frames := make(chan pdu.PDU, producers)
	releaseResponses := make(chan struct{})
	var release sync.Once
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		defer close(frames)
		for {
			packet, err := pdu.Parse(peer)
			if err != nil {
				return
			}
			select {
			case frames <- packet:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		defer workers.Done()
		select {
		case <-releaseResponses:
		case <-ctx.Done():
			return
		}
		for packet := range frames {
			if !writeTestPDU(peer, packet.GetResponse()) {
				return
			}
		}
	}()
	var submitting sync.WaitGroup
	results := make(chan error, producers)
	start := make(chan struct{})
	for range producers {
		submitting.Add(1)
		go func() {
			defer submitting.Done()
			<-start
			err := v.SubmitContext(ctx, pdu.NewSubmitSM())
			if errors.Is(err, ErrRequestCleanupFull) {
				store.arrived <- struct{}{}
			}
			results <- err
		}()
	}
	t.Cleanup(func() {
		cancel()
		release.Do(func() { close(releaseResponses) })
		_ = peer.Close()
		_ = v.Close()
		submitting.Wait()
		workers.Wait()
	})
	close(start)
	// Every producer has either reserved or hit the old cleanup cap before
	// the peer releases any capacity. Scheduling cannot hide excess producers.
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	for range producers {
		select {
		case <-store.arrived:
		case <-deadline.C:
			t.Fatal("producers did not reach reservation or admission")
		}
	}
	release.Do(func() { close(releaseResponses) })
	accepted, cleanupFull, other := 0, 0, 0
	for range producers {
		select {
		case err := <-results:
			switch {
			case err == nil:
				accepted++
			case errors.Is(err, ErrRequestCleanupFull):
				cleanupFull++
			default:
				other++
				t.Logf("submit error: %v", err)
			}
		case <-deadline.C:
			t.Fatal("healthy producers did not finish")
		}
	}
	for range accepted {
		select {
		case <-responses:
		case <-deadline.C:
			t.Fatal("accepted requests did not all receive responses")
		}
	}
	if accepted != producers || cleanupFull != 0 || other != 0 {
		t.Fatalf("healthy window backpressure: accepted=%d cleanup-full=%d other=%d, want %d/0/0", accepted, cleanupFull, other, producers)
	}
}

type windowUncertaintyTruncatedStore struct {
	DefaultStore
	outage       bool
	replaceOnGet *Request
}

func (s *windowUncertaintyTruncatedStore) Set(ctx context.Context, request Request) error {
	if err := s.DefaultStore.Set(ctx, request); err != nil {
		return err
	}
	if s.outage {
		return context.DeadlineExceeded
	}
	return nil
}
func (s *windowUncertaintyTruncatedStore) Delete(ctx context.Context, request Request) (bool, error) {
	if s.outage {
		return false, context.DeadlineExceeded
	}
	return s.DefaultStore.Delete(ctx, request)
}
func (*windowUncertaintyTruncatedStore) List(context.Context) []Request { return nil }
func (s *windowUncertaintyTruncatedStore) Get(ctx context.Context, sequence int32) (Request, bool) {
	request, exists := s.DefaultStore.Get(ctx, sequence)
	if exists && s.replaceOnGet != nil {
		replacement := *s.replaceOnGet
		s.replaceOnGet = nil
		_, _ = s.DefaultStore.Delete(context.Background(), request)
		_ = s.DefaultStore.Set(context.Background(), replacement)
	}
	return request, exists
}

func TestWindowUncertaintyTruncatedListCleansExactReservation(t *testing.T) {
	for _, replaced := range []bool{false, true} {
		name := "present"
		if replaced {
			name = "replaced_after_get"
		}
		t.Run(name, func(t *testing.T) {
			conn, cleanup := newPipePair()
			defer cleanup()
			store := &windowUncertaintyTruncatedStore{DefaultStore: NewDefaultStore(), outage: true}
			settings := lifecycleSettings()
			var completed atomic.Int32
			settings.OnClosePduRequest = func(pdu.PDU) { completed.Add(1) }
			settings.OnExpectedPduResponse = func(Response) { completed.Add(1) }
			settings.OnExpiredPduRequest = func(pdu.PDU) bool { completed.Add(1); return false }
			out := newTransmittable(conn, settings, store)
			defer func() { _ = out.close(ExplicitClosing) }()
			message := pdu.NewSubmitSM()
			if err := out.Submit(message); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("ambiguous Set: %v", err)
			}
			original, exists := store.DefaultStore.Get(context.Background(), message.GetSequenceNumber())
			if !exists {
				t.Fatal("remote Set did not commit")
			}
			replacement := original
			replacement.ID++
			if replaced {
				store.replaceOnGet = &replacement
			}
			store.outage = false
			forceCleanupScan(out)
			current, remains := store.DefaultStore.Get(context.Background(), message.GetSequenceNumber())
			if replaced {
				if !remains || current != replacement {
					t.Fatal("identity-conditional cleanup removed or changed replacement")
				}
			} else if remains {
				t.Error("Get found the abandoned reservation omitted by List, but recovery did not delete it")
			}
			if _, abandoned := out.lineage.abandoned.Load(original.ID); abandoned {
				t.Error("successful identity-conditional cleanup retained abandoned reservation")
			}
			if completed.Load() != 0 {
				t.Fatalf("rejected submission received %d completion callbacks", completed.Load())
			}
		})
	}
}

func TestWindowUncertaintyLateCommitCleanupPreservesOtherIdentity(t *testing.T) {
	for _, field := range []string{"Owner", "ID", "Sequence", "Lineage"} {
		t.Run(field, func(t *testing.T) {
			conn, cleanup := newPipePair()
			defer cleanup()
			store := &windowUncertaintyLateStore{DefaultStore: NewDefaultStore(), outage: true}
			out := newTransmittable(conn, lifecycleSettings(), store)
			defer func() { _ = out.close(ExplicitClosing) }()
			if err := out.Submit(pdu.NewSubmitSM()); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("ambiguous Set: %v", err)
			}
			store.outage = false
			forceCleanupScan(out)
			replacement := store.late
			switch field {
			case "Owner":
				replacement.Owner++
				replacement.Lineage++
			case "ID":
				replacement.ID++
			case "Sequence":
				replacement.PDU = pdu.NewSubmitSM()
			case "Lineage":
				replacement.Lineage++
			}
			if err := store.DefaultStore.Set(context.Background(), replacement); err != nil {
				t.Fatal(err)
			}
			defer func() { _, _ = store.DefaultStore.Delete(context.Background(), replacement) }()
			forceCleanupScan(out)
			if current, exists := store.Get(context.Background(), replacement.GetSequenceNumber()); !exists || current != replacement {
				t.Fatalf("late-commit cleanup removed or modified reservation with different %s", field)
			}
		})
	}
}

func TestWindowUncertaintyLateCommitRetentionIsBounded(t *testing.T) {
	conn, cleanup := newPipePair()
	defer cleanup()
	store := &windowUncertaintyLateStore{DefaultStore: NewDefaultStore()}
	settings := lifecycleSettings()
	var completed atomic.Int32
	settings.OnClosePduRequest = func(pdu.PDU) { completed.Add(1) }
	settings.OnExpectedPduResponse = func(Response) { completed.Add(1) }
	settings.OnExpiredPduRequest = func(pdu.PDU) bool { completed.Add(1); return false }
	out := newTransmittable(conn, settings, store)
	defer func() { _ = out.close(ExplicitClosing) }()
	var rejected []Request
	for range 1100 {
		store.outage = true
		if err := out.Submit(pdu.NewSubmitSM()); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("ambiguous Set stopped admitting attempts after recovery: %v", err)
		}
		rejected = append(rejected, store.late)
		store.outage = false
		forceCleanupScan(out)
	}
	retained := 0
	for _, request := range rejected {
		if out.lineage.discarded(request) {
			retained++
		}
	}
	if retained > 1024 {
		t.Fatalf("late-commit cleanup retains %d identities after 1100 settled failures", retained)
	}
	latest := rejected[len(rejected)-1]
	if !out.lineage.discarded(latest) {
		t.Fatal("bounded retention dropped the newest rejected identity")
	}
	if err := store.DefaultStore.Set(context.Background(), latest); err != nil {
		t.Fatal(err)
	}
	forceCleanupScan(out)
	if _, exists := store.Get(context.Background(), latest.GetSequenceNumber()); exists {
		t.Fatal("newest late commit survived bounded cleanup")
	}
	_ = out.close(ExplicitClosing)
	if completed.Load() != 0 {
		t.Fatalf("bounded cleanup completed rejected requests %d times", completed.Load())
	}
}

type windowUncertaintyFailingAdmissionStore struct {
	DefaultStore
	entered    chan struct{}
	release    chan struct{}
	failDelete atomic.Bool
}

func (s *windowUncertaintyFailingAdmissionStore) Set(ctx context.Context, _ Request) error {
	s.entered <- struct{}{}
	select {
	case <-s.release:
		return context.DeadlineExceeded
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (s *windowUncertaintyFailingAdmissionStore) Delete(ctx context.Context, request Request) (bool, error) {
	if s.failDelete.Load() {
		return false, context.DeadlineExceeded
	}
	return s.DefaultStore.Delete(ctx, request)
}

func TestWindowUncertaintyConcurrentFailedAdmissionsStayBounded(t *testing.T) {
	const producers = 1200
	conn, cleanup := newPipePair()
	defer cleanup()
	store := &windowUncertaintyFailingAdmissionStore{
		DefaultStore: NewDefaultStore(), entered: make(chan struct{}, producers), release: make(chan struct{}),
	}
	store.failDelete.Store(true)
	settings := lifecycleSettings()
	settings.StoreAccessTimeOut = 15 * time.Second
	out := newTransmittable(conn, settings, store)
	out.reclaimMu.Lock()
	out.nextReclaim = time.Now().Add(time.Hour)
	out.reclaimMu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	var release sync.Once
	var submitting sync.WaitGroup
	results := make(chan error, producers)
	for range producers {
		submitting.Add(1)
		go func() { defer submitting.Done(); results <- out.SubmitContext(ctx, pdu.NewSubmitSM()) }()
	}
	t.Cleanup(func() {
		cancel()
		release.Do(func() { close(store.release) })
		submitting.Wait()
		store.failDelete.Store(false)
		for range 5 {
			forceCleanupScan(out)
		}
		_ = out.close(ExplicitClosing)
	})
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	// All 1200 requests pass the initial uncertainty check before any Set
	// fails. The failure transition itself must therefore enforce the bound.
	for range producers {
		select {
		case <-store.entered:
		case <-deadline.C:
			t.Fatal("concurrent admissions did not all enter Set")
		}
	}
	release.Do(func() { close(store.release) })
	for range producers {
		select {
		case err := <-results:
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("failed remote Set returned %v", err)
			}
		case <-deadline.C:
			t.Fatal("failed submissions did not finish")
		}
	}
	submitting.Wait()
	requests, abandoned := cleanupOutstanding(out)
	out.lineage.mu.Lock()
	uncertain := out.lineage.unresolved
	out.lineage.mu.Unlock()
	if requests > 1024 || abandoned > 1024 || uncertain > 1024 {
		t.Fatalf("concurrent failed admissions exceeded cleanup bound: requests=%d abandoned=%d uncertain=%d", requests, abandoned, uncertain)
	}
	if requests == 0 || abandoned != requests || uncertain != requests {
		t.Fatalf("failed admissions lost unresolved identities: requests=%d abandoned=%d uncertain=%d", requests, abandoned, uncertain)
	}
}
