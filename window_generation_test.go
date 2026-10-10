package gosmpp

import (
	"context"
	"errors"
	"io"
	"net"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gomaja/go-smpp/pdu"
)

type windowOperationStore struct {
	DefaultStore
	deletes     chan context.Context
	failDeletes atomic.Int32
}

func (s *windowOperationStore) Delete(ctx context.Context, request Request) (bool, error) {
	if s.failDeletes.CompareAndSwap(1, 0) {
		return false, context.DeadlineExceeded
	}
	removed, err := s.DefaultStore.Delete(ctx, request)
	if s.deletes != nil {
		s.deletes <- ctx
	}
	return removed, err
}

// Install both halves of a written request, as the writer does before replies
// can claim the local completion ledger.
func seedWindowRequest(t *testing.T, out *transmittable, request Request) {
	t.Helper()
	writtenAt := request.WrittenAt
	request.WrittenAt = time.Time{}
	if err := out.rememberRequest(request); err != nil {
		t.Fatal(err)
	}
	out.beginTransmission(request)
	if !writtenAt.IsZero() {
		out.rememberWritten(request, writtenAt)
	}
	request.WrittenAt = writtenAt
	if err := out.requestStore.Set(context.Background(), request); err != nil {
		t.Fatal(err)
	}
}

// Leave a dead generation whose backend cleanup failed, before final Close
// claims its local completions. Recovery and expiry can still arbitrate them.
func retireWindowGeneration(t *testing.T, old *transceivable) {
	t.Helper()
	for _, request := range old.out.listRequests() {
		_, err := old.out.deleteRequest(request)
		if err == nil {
			t.Fatal("fixture cleanup unexpectedly succeeded")
		}
		old.out.reportStoreError(err)
	}
	old.out.retireRequestOwner()
	t.Cleanup(func() { liveRequestOwners.Delete(old.out.owner) })
}

func TestWindowCloseFlushCallbacksDoNotConsumeStoreDeadline(t *testing.T) {
	conn, cleanup := newPipePair()
	defer cleanup()
	store := &windowOperationStore{DefaultStore: NewDefaultStore(), deletes: make(chan context.Context, 16)}
	settings := lifecycleSettings()
	settings.StoreAccessTimeOut = 20 * time.Millisecond
	var closed, submitErrors atomic.Int32
	settings.OnClosePduRequest = func(pdu.PDU) { closed.Add(1); <-(<-store.deletes).Done() }
	settings.OnSubmitError = func(pdu.PDU, error) { submitErrors.Add(1) }
	v := newTransceivable(conn, settings, store)
	for range 3 {
		seedWindowRequest(t, v.out, v.out.newRequest(pdu.NewSubmitSM()))
	}
	if err := v.Close(); err != nil {
		t.Fatal(err)
	}
	n, _ := store.Length(context.Background())
	if n != 0 || closed.Load() != 3 || submitErrors.Load() != 0 {
		t.Fatalf("close callbacks=%d window=%d submit errors=%d; callbacks consumed later deletions' deadline", closed.Load(), n, submitErrors.Load())
	}
}

func TestWindowExpiryUsesSeparateStoreContexts(t *testing.T) {
	conn, cleanup := newPipePair()
	defer cleanup()
	store := &windowOperationStore{DefaultStore: NewDefaultStore(), deletes: make(chan context.Context, 16)}
	settings := lifecycleSettings()
	settings.StoreAccessTimeOut = 20 * time.Millisecond
	settings.PduExpireTimeOut = time.Nanosecond
	var expired atomic.Int32
	settings.OnExpiredPduRequest = func(pdu.PDU) bool { expired.Add(1); <-(<-store.deletes).Done(); return false }
	v := newTransceivable(conn, settings, store)
	defer func() { _ = v.Close() }()
	for range 3 {
		request := v.out.newRequest(pdu.NewSubmitSM())
		request.WrittenAt = time.Now().Add(-time.Hour)
		seedWindowRequest(t, v.out, request)
	}
	v.expireRequests()
	n, _ := store.Length(context.Background())
	if n != 0 || expired.Load() != 3 {
		t.Fatalf("expiry callbacks=%d window=%d; callbacks consumed later deletions' deadline", expired.Load(), n)
	}
}

func TestWindowLiveGenerationExpiresDeadOwnerReservations(t *testing.T) {
	for _, pending := range []bool{false, true} {
		name := "written"
		if pending {
			name = "pending"
		}
		t.Run(name, func(t *testing.T) {
			oldConn, oldClean := newPipePair()
			defer oldClean()
			liveConn, liveClean := newPipePair()
			defer liveClean()
			store := &windowOperationStore{DefaultStore: NewDefaultStore()}
			store.failDeletes.Store(1)
			settings := lifecycleSettings()
			settings.PduExpireTimeOut = time.Nanosecond
			var expired, submitErrors, receivingErrors atomic.Int32
			settings.OnExpiredPduRequest = func(pdu.PDU) bool { expired.Add(1); return false }
			settings.OnSubmitError = func(pdu.PDU, error) { submitErrors.Add(1) }
			settings.OnReceivingError = func(error) { receivingErrors.Add(1) }
			old := newTransceivable(oldConn, settings, store)
			request := Request{PDU: pdu.NewSubmitSM(), Owner: old.out.owner, Lineage: old.out.lineage.id, ID: 1, TimeSent: time.Now().Add(-time.Hour), WrittenAt: time.Now().Add(-time.Hour)}
			if pending {
				request.WrittenAt = time.Time{}
			}
			seedWindowRequest(t, old.out, request)
			retireWindowGeneration(t, old)
			live := newTransceivable(liveConn, settings, store)
			live.out.lineage = old.out.lineage
			defer func() { _ = live.Close() }()
			live.expireRequests()
			n, _ := store.Length(context.Background())
			if n != 0 || expired.Load() != 1 {
				t.Fatalf("dead generation permanently owns reservation: window=%d expiry=%d", n, expired.Load())
			}
			if submitErrors.Load() != 0 || receivingErrors.Load() != 1 {
				t.Fatalf("store deletion failure reported as a failed submission: submit=%d receiving=%d", submitErrors.Load(), receivingErrors.Load())
			}
		})
	}
}

type windowWriteGate struct {
	net.Conn
	entered, release chan struct{}
	fail             error
	forward          bool
	once             sync.Once
}

func (c *windowWriteGate) Write(b []byte) (int, error) {
	c.once.Do(func() { close(c.entered) })
	<-c.release
	if c.fail != nil {
		return 0, c.fail
	}
	if c.forward {
		return c.Conn.Write(b)
	}
	return len(b), nil
}

func newWindowGatedWriter(t *testing.T, settings Settings, fail error) (*transceivable, *windowWriteGate, DefaultStore) {
	t.Helper()
	raw, peer := net.Pipe()
	gate := &windowWriteGate{Conn: raw, entered: make(chan struct{}), release: make(chan struct{}), fail: fail}
	store := NewDefaultStore()
	v := newTransceivable(NewConnection(gate), settings, store)
	t.Cleanup(func() { _ = peer.Close(); _ = raw.Close(); _ = v.Close() })
	return v, gate, store
}

func TestWindowPendingWriteDoesNotExpire(t *testing.T) {
	settings := lifecycleSettings()
	settings.PduExpireTimeOut = time.Nanosecond
	var expired atomic.Int32
	settings.OnExpiredPduRequest = func(pdu.PDU) bool { expired.Add(1); return false }
	v, gate, store := newWindowGatedWriter(t, settings, nil)
	request := pdu.NewSubmitSM()
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := v.out.write(request); err != nil {
			t.Errorf("write: %v", err)
		}
	}()
	lifecycleWait(t, gate.entered)
	v.expireRequests()
	n, _ := store.Length(context.Background())
	if expired.Load() != 0 || n != 1 {
		t.Errorf("in-flight request expired before its write completed: expiry=%d window=%d", expired.Load(), n)
	}
	sentAfter := time.Now()
	close(gate.release)
	lifecycleWait(t, done)
	written, ok := store.Get(context.Background(), request.GetSequenceNumber())
	if !ok || written.WrittenAt.Before(sentAfter) {
		t.Errorf("expiry clock did not begin after write: entry=%v sent=%v completion gate=%v", ok, written.WrittenAt, sentAfter)
	}
}

func TestWindowPendingWriteFailureHasOneCompletion(t *testing.T) {
	settings := lifecycleSettings()
	settings.PduExpireTimeOut = time.Nanosecond
	var expired, submitted, closed atomic.Int32
	settings.OnExpiredPduRequest = func(pdu.PDU) bool { expired.Add(1); return false }
	settings.OnSubmitError = func(pdu.PDU, error) { submitted.Add(1) }
	settings.OnClosePduRequest = func(pdu.PDU) { closed.Add(1) }
	v, gate, _ := newWindowGatedWriter(t, settings, io.ErrClosedPipe)
	request := pdu.NewSubmitSM()
	done := make(chan struct{})
	go func() { defer close(done); n, err := v.out.write(request); v.out.check(request, n, err) }()
	lifecycleWait(t, gate.entered)
	v.expireRequests()
	close(gate.release)
	lifecycleWait(t, done)
	if err := v.Close(); err != nil {
		t.Fatal(err)
	}
	if expired.Load() != 0 || submitted.Load() != 1 || closed.Load() != 0 {
		t.Fatalf("one PDU completed twice: expiry=%d submit=%d close=%d", expired.Load(), submitted.Load(), closed.Load())
	}
}

func TestWindowRollbackLosesCompletionSuppressesSubmitError(t *testing.T) {
	settings := lifecycleSettings()
	var responded, submitted atomic.Int32
	settings.OnExpectedPduResponse = func(Response) { responded.Add(1) }
	settings.OnSubmitError = func(pdu.PDU, error) { submitted.Add(1) }
	v, gate, store := newWindowGatedWriter(t, settings, io.ErrClosedPipe)
	request := pdu.NewSubmitSM()
	done := make(chan struct{})
	go func() { defer close(done); n, err := v.out.write(request); v.out.check(request, n, err) }()
	lifecycleWait(t, gate.entered)
	v.in.completeResponse(request.GetResponse(), request.GetSequenceNumber(), nil)
	close(gate.release)
	lifecycleWait(t, done)
	if err := v.Close(); err != nil {
		t.Fatal(err)
	}
	n, _ := store.Length(context.Background())
	if responded.Load() != 1 || submitted.Load() != 0 || n != 0 {
		t.Fatalf("lost rollback delivered another completion: response=%d submit=%d window=%d", responded.Load(), submitted.Load(), n)
	}
}

func TestWindowWrittenTransitionDoesNotResurrectResponse(t *testing.T) {
	settings := lifecycleSettings()
	responses := make(chan Response, 1)
	settings.OnExpectedPduResponse = func(response Response) { responses <- response }
	v, gate, store := newWindowGatedWriter(t, settings, nil)
	request := pdu.NewSubmitSM()
	done := make(chan struct{})
	go func() { defer close(done); _, _ = v.out.write(request) }()
	lifecycleWait(t, gate.entered)
	v.in.completeResponse(request.GetResponse(), request.GetSequenceNumber(), nil)
	select {
	case response := <-responses:
		if response.OriginalRequest.TimeSent.IsZero() {
			t.Error("response before MarkWritten lost original submission time")
		}
	default:
		t.Error("pending reservation response was not delivered")
	}
	close(gate.release)
	lifecycleWait(t, done)
	n, _ := store.Length(context.Background())
	if n != 0 {
		t.Fatalf("successful write restored request already completed by response: window=%d", n)
	}
	if size, err := v.GetWindowSize(); err != nil || size != 0 {
		t.Fatalf("response-before-write resurrected local occupancy: size=%d error=%v", size, err)
	}
}

func TestWindowSharedStoreExpiryProtectsOtherLiveGeneration(t *testing.T) {
	aConn, aClean := newPipePair()
	defer aClean()
	bConn, bClean := newPipePair()
	defer bClean()
	store := NewDefaultStore()
	settings := lifecycleSettings()
	settings.PduExpireTimeOut = time.Nanosecond
	var expired atomic.Int32
	settings.OnExpiredPduRequest = func(pdu.PDU) bool { expired.Add(1); return false }
	a := newTransceivable(aConn, settings, store)
	b := newTransceivable(bConn, settings, store)
	a.start()
	b.start()
	defer func() { _ = a.Close(); _ = b.Close() }()
	request := Request{PDU: pdu.NewSubmitSM(), Owner: b.out.owner, ID: 3, TimeSent: time.Now().Add(-time.Hour), WrittenAt: time.Now().Add(-time.Hour)}
	seedWindowRequest(t, b.out, request)
	a.expireRequests()
	n, _ := store.Length(context.Background())
	if n != 1 || expired.Load() != 0 {
		t.Fatalf("one live generation expired another's reservation: window=%d expiry=%d", n, expired.Load())
	}
	b.expireRequests()
	n, _ = store.Length(context.Background())
	if n != 0 || expired.Load() != 1 {
		t.Fatalf("own generation did not expire reservation: window=%d expiry=%d", n, expired.Load())
	}
}

func TestWindowSharedStoreResponseProtectsOtherLiveGeneration(t *testing.T) {
	aConn, aClean := newPipePair()
	defer aClean()
	bConn, bClean := newPipePair()
	defer bClean()
	store := NewDefaultStore()
	settings := lifecycleSettings()
	var responses atomic.Int32
	settings.OnExpectedPduResponse = func(Response) { responses.Add(1) }
	a := newTransceivable(aConn, settings, store)
	b := newTransceivable(bConn, settings, store)
	a.start()
	b.start()
	defer func() { _ = a.Close(); _ = b.Close() }()
	message := pdu.NewSubmitSM()
	if _, err := b.out.write(message); err != nil {
		t.Fatal(err)
	}
	request, ok := store.Get(context.Background(), message.GetSequenceNumber())
	if !ok {
		t.Fatal("written reservation missing")
	}
	a.in.completeResponse(request.GetResponse(), request.GetSequenceNumber(), nil)
	n, _ := store.Length(context.Background())
	if n != 1 || responses.Load() != 0 {
		t.Fatalf("one live generation completed another's response: window=%d responses=%d", n, responses.Load())
	}
	b.in.completeResponse(request.GetResponse(), request.GetSequenceNumber(), nil)
	n, _ = store.Length(context.Background())
	if n != 0 || responses.Load() != 1 {
		t.Fatalf("own generation did not complete response: window=%d responses=%d", n, responses.Load())
	}
}

func TestWindowStoreMarkWrittenIsConditional(t *testing.T) {
	store := NewDefaultStore()
	marker := store
	request := Request{PDU: pdu.NewSubmitSM(), Owner: 1, ID: 1}
	if err := store.Set(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Delete(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if marked, err := marker.MarkWritten(context.Background(), request, time.Now()); err != nil || marked {
		t.Fatalf("absent request resurrected: marked=%v error=%v", marked, err)
	}
	replacement := request
	replacement.ID++
	if err := store.Set(context.Background(), replacement); err != nil {
		t.Fatal(err)
	}
	if marked, err := marker.MarkWritten(context.Background(), request, time.Now()); err != nil || marked {
		t.Fatalf("replaced request marked: marked=%v error=%v", marked, err)
	}
	sent := time.Now()
	if marked, err := marker.MarkWritten(context.Background(), replacement, sent); err != nil || !marked {
		t.Fatalf("pending request not marked: marked=%v error=%v", marked, err)
	}
	written, _ := store.Get(context.Background(), request.GetSequenceNumber())
	if !written.WrittenAt.Equal(sent) {
		t.Fatalf("timestamp=%v, want %v", written.WrittenAt, sent)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := marker.MarkWritten(canceled, written, time.Now()); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled operation: %v", err)
	}
}

func TestWindowBlockedRequestIsDeliveredWithoutPriorExpiry(t *testing.T) {
	settings := lifecycleSettings()
	settings.PduExpireTimeOut = time.Nanosecond
	var expired, expected, unexpected atomic.Int32
	settings.OnExpiredPduRequest = func(pdu.PDU) bool { expired.Add(1); return false }
	settings.OnExpectedPduResponse = func(Response) { expected.Add(1) }
	settings.OnUnexpectedPduResponse = func(pdu.PDU) { unexpected.Add(1) }
	client, peer := net.Pipe()
	gate := &windowWriteGate{Conn: client, entered: make(chan struct{}), release: make(chan struct{}), forward: true}
	store := NewDefaultStore()
	v := newTransceivable(NewConnection(gate), settings, store)
	defer func() { _ = peer.Close(); _ = v.Close() }()
	request := pdu.NewSubmitSM()
	written := make(chan struct{})
	go func() { defer close(written); _, _ = v.out.write(request) }()
	lifecycleWait(t, gate.entered)
	v.expireRequests()
	close(gate.release)
	received, err := pdu.Parse(peer)
	if err != nil {
		t.Fatal(err)
	}
	lifecycleWait(t, written)
	v.in.completeResponse(received.GetResponse(), received.GetSequenceNumber(), nil)
	if expired.Load() != 0 || expected.Load() != 1 || unexpected.Load() != 0 {
		t.Fatalf("blocked PDU expired and was then delivered: expiry=%d expected=%d unexpected=%d", expired.Load(), expected.Load(), unexpected.Load())
	}
}

type windowMarkFailureStore struct{ DefaultStore }

func (s windowMarkFailureStore) MarkWritten(context.Context, Request, time.Time) (bool, error) {
	return false, context.DeadlineExceeded
}

func TestWindowWrittenStoreFailureDoesNotReportUnsentPDU(t *testing.T) {
	settings := lifecycleSettings()
	var errorsReported, submits, closes atomic.Int32
	settings.OnReceivingError = func(error) { errorsReported.Add(1) }
	settings.OnSubmitError = func(pdu.PDU, error) { submits.Add(1) }
	settings.OnClosePduRequest = func(pdu.PDU) { closes.Add(1) }
	conn, cleanup := newPipePair()
	defer cleanup()
	store := windowMarkFailureStore{NewDefaultStore()}
	v := newTransceivable(conn, settings, store)
	request := pdu.NewSubmitSM()
	n, err := v.out.write(request)
	if err != nil {
		t.Fatalf("successful write returned metadata error: %v", err)
	}
	if v.out.check(request, n, err) {
		t.Fatal("metadata error closed healthy generation")
	}
	if err := v.Close(); err != nil {
		t.Fatal(err)
	}
	if errorsReported.Load() < 1 || submits.Load() != 0 || closes.Load() != 1 {
		t.Fatalf("written PDU reported unsent: store errors=%d submit errors=%d close completions=%d", errorsReported.Load(), submits.Load(), closes.Load())
	}
}

func TestWindowCloseReleasesGenerationRegistryAndWorkers(t *testing.T) {
	baseline := runtime.NumGoroutine()
	for range 10 {
		conn, cleanup := newPipePair()
		v := newTransceivable(conn, lifecycleSettings(), NewDefaultStore())
		v.start()
		if !requestOwnerAlive(v.out.owner) {
			t.Fatal("started generation was not registered")
		}
		if err := v.Close(); err != nil {
			t.Fatal(err)
		}
		cleanup()
		if requestOwnerAlive(v.out.owner) {
			t.Fatal("closed generation remained in the live-owner registry")
		}
	}
	deadline := time.Now().Add(time.Second)
	for runtime.NumGoroutine() > baseline && time.Now().Before(deadline) {
		runtime.Gosched()
	}
	if got := runtime.NumGoroutine(); got > baseline {
		stacks := make([]byte, 1<<20)
		n := runtime.Stack(stacks, true)
		t.Fatalf("close left workers: baseline=%d current=%d\n%s", baseline, got, stacks[:n])
	}
}

func TestWindowStoreMarkWrittenRejectsStaleMetadata(t *testing.T) {
	changes := []struct {
		name   string
		change func(*Request)
	}{
		{"owner", func(r *Request) { r.Owner++ }},
		{"identity", func(r *Request) { r.ID++ }},
	}
	for _, change := range changes {
		t.Run(change.name, func(t *testing.T) {
			store := NewDefaultStore()
			pending := Request{PDU: pdu.NewEnquireLink(), Owner: 41, ID: 43}
			if err := store.Set(context.Background(), pending); err != nil {
				t.Fatal(err)
			}
			stale := pending
			change.change(&stale)
			if marked, err := store.MarkWritten(context.Background(), stale, time.Now()); err != nil || marked {
				t.Fatalf("stale %s metadata marked request: marked=%v error=%v", change.name, marked, err)
			}
		})
	}
	store := NewDefaultStore()
	written := Request{PDU: pdu.NewEnquireLink(), TimeSent: time.Now(), WrittenAt: time.Now(), Owner: 47, ID: 53}
	if err := store.Set(context.Background(), written); err != nil {
		t.Fatal(err)
	}
	if marked, err := store.MarkWritten(context.Background(), written, time.Now()); err != nil || marked {
		t.Fatalf("written request timestamp changed twice: marked=%v error=%v", marked, err)
	}
}
