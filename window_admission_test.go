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

func TestWindowConcurrentTCPProducersBackpressureAtWrittenCapacity(t *testing.T) {
	const total = 500
	settings := lifecycleSettings()
	settings.MaxWindowSize = 10
	events := make(chan error, total)
	var responses, full atomic.Int32
	settings.OnExpectedPduResponse = func(Response) { responses.Add(1); events <- nil }
	settings.OnSubmitError = func(_ pdu.PDU, err error) {
		if errors.Is(err, ErrWindowsFull) {
			full.Add(1)
		}
		events <- err
	}
	session, err := NewSession(TRXConnector(NonTLSDialer, nextAuth()), settings, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	var issued atomic.Int32
	var producers sync.WaitGroup
	for range 16 {
		producers.Add(1)
		go func() {
			defer producers.Done()
			for issued.Add(1) <= total {
				if err := session.Transceiver().Submit(pdu.NewSubmitSM()); err != nil {
					events <- err
				}
			}
		}()
	}
	producerDone := make(chan struct{})
	go func() { producers.Wait(); close(producerDone) }()
	for range total {
		select {
		case err := <-events:
			if err != nil {
				t.Errorf("request rejected: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("producer batch failed to complete")
		}
	}
	lifecycleWait(t, producerDone)
	if full.Load() != 0 || responses.Load() != total {
		t.Fatalf("TCP producers: responses=%d full=%d, want %d/0", responses.Load(), full.Load(), total)
	}
}

type windowAdmissionSignalStore struct {
	DefaultStore
	reserved chan Request
}

func (s *windowAdmissionSignalStore) Set(ctx context.Context, r Request) error {
	err := s.DefaultStore.Set(ctx, r)
	if err == nil {
		s.reserved <- r
	}
	return err
}
func TestWindowPermitWaitCancelsAndKeepaliveBypasses(t *testing.T) {
	raw, peer := net.Pipe()
	defer func() { _ = peer.Close() }()
	store := &windowAdmissionSignalStore{DefaultStore: NewDefaultStore(), reserved: make(chan Request, 8)}
	settings := lifecycleSettings()
	settings.MaxWindowSize = 1
	v := newTransceivable(NewConnection(raw), settings, store)
	v.start()
	defer func() { _ = v.Close() }()
	received := make(chan pdu.PDU, 8)
	peerDone := make(chan struct{})
	go func() {
		defer close(peerDone)
		for {
			packet, err := pdu.Parse(peer)
			if err != nil {
				return
			}
			received <- packet
		}
	}()
	first := pdu.NewSubmitSM()
	if err := v.Submit(first); err != nil {
		t.Fatal(err)
	}
	<-store.reserved
	select {
	case <-received:
	case <-time.After(time.Second):
		t.Fatal("first request missing")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	second := pdu.NewSubmitSM()
	submitted := make(chan error, 1)
	go func() { submitted <- v.SubmitContext(ctx, second) }()
	<-store.reserved
	// An unanswering peer holds application capacity, but both directions of
	// enquire_link must still work while the next application producer waits.
	enquiry := pdu.NewEnquireLink()
	if err := v.Submit(enquiry); err != nil {
		t.Fatal(err)
	}
	select {
	case packet := <-received:
		if _, ok := packet.(*pdu.EnquireLink); !ok {
			t.Fatalf("keepalive suppressed by %T", packet)
		}
	case <-time.After(time.Second):
		t.Fatal("keepalive blocked by full application window")
	}
	if !writeTestPDU(peer, pdu.NewEnquireLink()) {
		t.Fatal("peer enquiry write")
	}
	select {
	case packet := <-received:
		if _, ok := packet.(*pdu.EnquireLinkResp); !ok {
			t.Fatalf("reader enquiry response got %T", packet)
		}
	case <-time.After(time.Second):
		t.Fatal("reader enquiry response blocked by full application window")
	}
	cancel()
	select {
	case err := <-submitted:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("full-window producer did not backpressure: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled permit waiter stuck")
	}
	if _, ok := store.Get(context.Background(), second.GetSequenceNumber()); ok {
		t.Fatal("canceled permit reservation retained")
	}
	_ = peer.Close()
	lifecycleWait(t, peerDone)
}

func TestWindowReaderCallbackCannotWaitForOwnFullWindow(t *testing.T) {
	raw, peer := net.Pipe()
	defer func() { _ = peer.Close() }()
	settings := lifecycleSettings()
	settings.MaxWindowSize = 1
	entered, release := make(chan struct{}), make(chan struct{})
	returned := make(chan error, 1)
	var v *transceivable
	settings.OnReceivedPduRequest = func(packet pdu.PDU) (pdu.PDU, bool) {
		close(entered)
		<-release
		returned <- v.Submit(pdu.NewSubmitSM())
		return packet.GetResponse(), false
	}
	v = newTransceivable(NewConnection(raw), settings, NewDefaultStore())
	v.start()
	defer func() { _ = v.Close() }()
	first := pdu.NewSubmitSM()
	if err := v.Submit(first); err != nil {
		t.Fatal(err)
	}
	if _, err := pdu.Parse(peer); err != nil {
		t.Fatal(err)
	}
	if !writeTestPDU(peer, pdu.NewEnquireLink()) {
		t.Fatal("write request")
	}
	lifecycleWait(t, entered)
	close(release)
	select {
	case err := <-returned:
		if !errors.Is(err, ErrSubmitQueueFull) {
			t.Fatalf("reader full-window reentry: %v", err)
		}
	case <-time.After(time.Second):
		v.stop(ExplicitClosing)
		t.Fatal("reader waited for response processing on its own worker")
	}
}

func TestWindowResponseReleasesPermitBeforeCallbackResubmission(t *testing.T) {
	settings := lifecycleSettings()
	settings.MaxWindowSize = 1
	var session *Session
	var received atomic.Int32
	resubmitted := make(chan error, 1)
	complete := make(chan struct{})
	settings.OnExpectedPduResponse = func(Response) {
		if received.Add(1) == 1 {
			resubmitted <- session.Transceiver().Submit(pdu.NewSubmitSM())
		} else {
			close(complete)
		}
	}
	var err error
	session, err = NewSession(TRXConnector(NonTLSDialer, nextAuth()), settings, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	if err = session.Transceiver().Submit(pdu.NewSubmitSM()); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-resubmitted:
		if err != nil {
			t.Fatalf("response callback retained completed permit: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("response resubmission blocked")
	}
	lifecycleWait(t, complete)
}

type windowPermitCleanupStore struct {
	*windowDeadOwnerStore
	reserved chan Request
}

func (s *windowPermitCleanupStore) Set(ctx context.Context, r Request) error {
	err := s.DefaultStore.Set(ctx, r)
	if err == nil {
		s.reserved <- r
	}
	return err
}
func TestWindowFailedAdmissionReleasesPermitDespiteDeleteError(t *testing.T) {
	conn, cleanup := newPipePair()
	defer cleanup()
	store := &windowPermitCleanupStore{windowDeadOwnerStore: &windowDeadOwnerStore{DefaultStore: NewDefaultStore()}, reserved: make(chan Request, 8)}
	settings := lifecycleSettings()
	settings.MaxWindowSize = 1
	out := newTransmittable(conn, settings, store)
	defer func() { store.failDeletes.Store(false); _ = out.close(ExplicitClosing) }()
	if err := out.Submit(pdu.NewEnquireLinkResp()); err != nil {
		t.Fatal(err)
	}
	store.failDeletes.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	returned := make(chan error, 1)
	go func() { returned <- out.SubmitContext(ctx, pdu.NewSubmitSM()) }()
	<-store.reserved
	cancel()
	if err := <-returned; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled admission: %v", err)
	}
	<-out.input
	nextCtx, nextCancel := context.WithTimeout(context.Background(), time.Second)
	defer nextCancel()
	if err := out.SubmitContext(nextCtx, pdu.NewSubmitSM()); err != nil {
		t.Fatalf("abandoned admission held application permit: %v", err)
	}
}

func TestWindowWriteRollbackReleasesPermitBeforeCallbackDespiteDeleteError(t *testing.T) {
	conn, cleanup := newPipePair()
	defer cleanup()
	store := &windowDeadOwnerStore{DefaultStore: NewDefaultStore()}
	store.failDeletes.Store(true)
	settings := lifecycleSettings()
	settings.MaxWindowSize = 1
	var out *transmittable
	resubmitted := make(chan error, 1)
	var once atomic.Bool
	settings.OnSubmitError = func(_ pdu.PDU, err error) {
		if once.CompareAndSwap(false, true) {
			if !errors.Is(err, pdu.ErrEncode) {
				t.Errorf("bad write error: %v", err)
			}
			resubmitted <- out.Submit(pdu.NewSubmitSM())
		}
	}
	out = newTransmittable(conn, settings, store)
	out.start()
	defer func() { store.failDeletes.Store(false); _ = out.close(ExplicitClosing) }()
	invalid := pdu.NewSubmitSM().(*pdu.SubmitSM)
	invalid.ServiceType = "bad\x00field"
	if err := out.Submit(invalid); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-resubmitted:
		if err != nil {
			t.Fatalf("write rollback retained permit for callback: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("rollback callback blocked")
	}
}

func TestWindowClosingReleasesFullPermitWaiter(t *testing.T) {
	raw, peer := net.Pipe()
	defer func() { _ = peer.Close() }()
	store := &windowAdmissionSignalStore{DefaultStore: NewDefaultStore(), reserved: make(chan Request, 8)}
	settings := lifecycleSettings()
	settings.MaxWindowSize = 1
	v := newTransceivable(NewConnection(raw), settings, store)
	v.start()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer func() { _ = v.Close() }()
	if err := v.Submit(pdu.NewSubmitSM()); err != nil {
		t.Fatal(err)
	}
	<-store.reserved
	if _, err := pdu.Parse(peer); err != nil {
		t.Fatal(err)
	}
	submitted := make(chan error, 1)
	go func() { submitted <- v.SubmitContext(ctx, pdu.NewSubmitSM()) }()
	<-store.reserved
	closed := make(chan struct{})
	go func() { _ = v.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		cancel()
		lifecycleWait(t, closed)
		t.Fatal("Close did not wake full-window producer")
	}
	if err := <-submitted; !errors.Is(err, ErrConnectionClosing) {
		t.Fatalf("stopped permit waiter: %v", err)
	}
}

func TestWindowDrainReleasesPermitDespiteDeleteError(t *testing.T) {
	conn, cleanup := newPipePair()
	defer cleanup()
	store := &windowDeadOwnerStore{DefaultStore: NewDefaultStore()}
	settings := lifecycleSettings()
	settings.MaxWindowSize = 1
	out := newTransmittable(conn, settings, store)
	request := pdu.NewSubmitSM()
	if err := out.Submit(request); err != nil {
		t.Fatal(err)
	}
	reserved, _ := store.Get(context.Background(), request.GetSequenceNumber())
	store.failDeletes.Store(true)
	if err := out.close(ExplicitClosing); err != nil {
		t.Fatal(err)
	}
	if held := len(out.ownerState.permits); held != 0 {
		t.Errorf("shutdown retained %d permits for discarded PDUs", held)
	}
	if held := len(out.ownerState.admitted); held != 0 {
		t.Errorf("shutdown retained %d admitted identities", held)
	}
	store.failDeletes.Store(false)
	if _, err := out.deleteRequest(reserved); err != nil {
		t.Fatal(err)
	}
}

func TestWindowForeignCallbackCannotWaitForFullPermit(t *testing.T) {
	rawB, peerB := net.Pipe()
	defer func() { _ = peerB.Close() }()
	settings := lifecycleSettings()
	settings.MaxWindowSize = 1
	b := newTransceivable(NewConnection(rawB), settings, NewDefaultStore())
	b.start()
	defer func() { _ = b.Close() }()
	if err := b.Submit(pdu.NewSubmitSM()); err != nil {
		t.Fatal(err)
	}
	if _, err := pdu.Parse(peerB); err != nil {
		t.Fatal(err)
	}
	rawA, peerA := net.Pipe()
	defer func() { _ = peerA.Close() }()
	returned := make(chan error, 1)
	settingsA := lifecycleSettings()
	settingsA.OnReceivedPduRequest = func(packet pdu.PDU) (pdu.PDU, bool) {
		returned <- b.Submit(pdu.NewSubmitSM())
		return packet.GetResponse(), false
	}
	a := newTransceivable(NewConnection(rawA), settingsA, NewDefaultStore())
	a.start()
	defer func() { _ = a.Close() }()
	if !writeTestPDU(peerA, pdu.NewEnquireLink()) {
		t.Fatal("request write")
	}
	select {
	case err := <-returned:
		if !errors.Is(err, ErrSubmitQueueFull) {
			t.Fatalf("foreign callback full-window rejection: %v", err)
		}
	case <-time.After(time.Second):
		b.stop(ExplicitClosing)
		err := <-returned
		t.Fatalf("foreign callback waited for silent peer until target closed: %v", err)
	}
}

type windowResponseDuringSetStore struct {
	DefaultStore
	response func(Request)
}

func (s *windowResponseDuringSetStore) Set(ctx context.Context, r Request) error {
	err := s.DefaultStore.Set(ctx, r)
	if err == nil && s.response != nil {
		s.response(r)
	}
	return err
}
func TestWindowResponseDuringReservationCannotReleasePermit(t *testing.T) {
	conn, cleanup := newPipePair()
	defer cleanup()
	settings := lifecycleSettings()
	settings.MaxWindowSize = 1
	var unexpected, expected atomic.Int32
	settings.OnUnexpectedPduResponse = func(pdu.PDU) { unexpected.Add(1) }
	settings.OnExpectedPduResponse = func(Response) { expected.Add(1) }
	store := &windowResponseDuringSetStore{DefaultStore: NewDefaultStore()}
	v := newTransceivable(conn, settings, store)
	defer func() { _ = v.Close() }()
	store.response = func(r Request) { v.in.completeResponse(r.GetResponse(), r.GetSequenceNumber(), nil) }
	request := pdu.NewSubmitSM()
	if err := v.Submit(request); err != nil {
		t.Fatal(err)
	}
	if n := len(v.out.input); n != 1 {
		t.Fatalf("stray response dropped queued request: %d", n)
	}
	if held := len(v.out.ownerState.permits); held != 1 {
		t.Fatalf("stray response released admission permit: %d", held)
	}
	if unexpected.Load() != 1 || expected.Load() != 0 {
		t.Fatalf("unwritten request correlation: unexpected=%d expected=%d", unexpected.Load(), expected.Load())
	}
	queued := <-v.out.input
	if _, err := v.out.writeQueued(queued); err != nil {
		t.Fatal(err)
	}
	v.in.completeResponse(request.GetResponse(), request.GetSequenceNumber(), nil)
	if held := len(v.out.ownerState.permits); held != 0 || expected.Load() != 1 {
		t.Fatalf("written response did not release permit: held=%d expected=%d", held, expected.Load())
	}
	store.response = nil
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := v.SubmitContext(ctx, pdu.NewSubmitSM()); err != nil {
		t.Fatalf("completed reservation blocked next admission: %v", err)
	}
}
