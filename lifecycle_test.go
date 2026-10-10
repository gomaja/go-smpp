package gosmpp

import (
	"context"
	"errors"
	"net"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/gomaja/go-smpp/data"
	"github.com/gomaja/go-smpp/pdu"
)

func lifecycleWait(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("lifecycle operation did not finish")
	}
}

func lifecycleSettings() Settings {
	return Settings{ReadTimeout: time.Hour, WriteTimeout: time.Second,
		WindowedRequestTracking: &WindowedRequestTracking{MaxWindowSize: 10, StoreAccessTimeOut: time.Second}}
}

type lifecycleWriteConn struct {
	net.Conn
	entered chan struct{}
	once    sync.Once
}

func (c *lifecycleWriteConn) Write(b []byte) (int, error) {
	c.once.Do(func() { close(c.entered) })
	return c.Conn.Write(b)
}

func TestLifecycleCloseInterruptsWriteAndSubmit(t *testing.T) {
	c, peer := net.Pipe()
	defer func() { _ = c.Close() }()
	defer func() { _ = peer.Close() }()
	entered := make(chan struct{})
	v := newTransceivable(NewConnection(&lifecycleWriteConn{Conn: c, entered: entered}), Settings{ReadTimeout: time.Hour}, nil)
	defer func() { _ = v.Close() }()
	v.start()
	if err := v.Submit(pdu.NewEnquireLink()); err != nil {
		t.Fatal(err)
	}
	lifecycleWait(t, entered)
	if err := v.Submit(pdu.NewEnquireLink()); err != nil {
		t.Fatal(err)
	}
	submitted := make(chan error, 1)
	ids := make(chan uint64, 1)
	go func() {
		ids <- callbackGoroutineID()
		submitted <- v.Submit(pdu.NewEnquireLink())
	}()
	lifecycleAwaitWait(t, <-ids, v.out.callbacks.instance())
	done := make(chan struct{})
	go func() { _ = v.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Error("Close blocked on socket Write before interrupting I/O")
		_ = peer.Close()
		lifecycleWait(t, done)
	}
	select {
	case err := <-submitted:
		if !errors.Is(err, ErrConnectionClosing) {
			t.Errorf("blocked Submit: %v", err)
		}
	case <-time.After(time.Second):
		t.Error("queued Submit did not unblock")
	}
}

func TestLifecycleSubmitResumesWhenWriterDrainsQueue(t *testing.T) {
	c, peer := net.Pipe()
	defer func() { _ = peer.Close() }()
	entered := make(chan struct{})
	v := newTransceivable(NewConnection(&lifecycleWriteConn{Conn: c, entered: entered}), Settings{ReadTimeout: time.Hour}, nil)
	defer func() { _ = v.Close() }()
	v.start()
	first := pdu.NewEnquireLink()
	if err := v.Submit(first); err != nil {
		t.Fatal(err)
	}
	lifecycleWait(t, entered)
	if err := v.Submit(pdu.NewEnquireLink()); err != nil {
		t.Fatal(err)
	}
	ids, submitted := make(chan uint64, 1), make(chan error, 1)
	go func() {
		ids <- callbackGoroutineID()
		submitted <- v.Submit(pdu.NewEnquireLink())
	}()
	lifecycleAwaitWait(t, <-ids, v.out.callbacks.instance())
	if err := peer.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	received, err := pdu.Parse(peer)
	if err != nil || received.GetSequenceNumber() != first.GetSequenceNumber() {
		t.Fatalf("first queued PDU: %v, %v", received, err)
	}
	select {
	case err := <-submitted:
		if err != nil {
			t.Fatalf("available queue rejected waiting Submit: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("writer drained the queue without waking Submit")
	}
}

type lifecycleGatedReservationStore struct {
	DefaultStore
	entered chan struct{}
	release chan struct{}
}

func (s *lifecycleGatedReservationStore) Set(ctx context.Context, request Request) error {
	if err := s.DefaultStore.Set(ctx, request); err != nil {
		return err
	}
	close(s.entered)
	<-s.release
	return nil
}

func lifecycleAwaitQueueAdmissionLock(t *testing.T, id uint64, returned <-chan error) {
	t.Helper()
	header := "goroutine " + strconv.FormatUint(id, 10) + " "
	deadline := time.Now().Add(2 * time.Second)
	for {
		select {
		case err := <-returned:
			t.Fatalf("queue admission bypassed shutdown lock: %v", err)
		default:
		}
		stack := make([]byte, 64<<10)
		n := runtime.Stack(stack, true)
		dump := string(stack[:n])
		if index := strings.Index(dump, header); index >= 0 {
			worker := strings.SplitN(dump[index:], "\n\n", 2)[0]
			if strings.Contains(worker, "(*transmittable).tryEnqueue") && strings.Contains(worker, "Mutex") {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("producer did not reach the queue admission lock")
		}
		runtime.Gosched()
	}
}

func TestLifecycleQueueAdmissionSharesShutdownLock(t *testing.T) {
	conn, cleanup := newPipePair()
	defer cleanup()
	store := &lifecycleGatedReservationStore{DefaultStore: NewDefaultStore(), entered: make(chan struct{}), release: make(chan struct{})}
	out := newTransmittable(conn, lifecycleSettings(), store)
	defer func() { _ = out.close(ConnectionIssue) }()
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(store.release) }) }
	defer release()
	ids, returned := make(chan uint64, 1), make(chan error, 1)
	go func() {
		ids <- callbackGoroutineID()
		returned <- out.Submit(pdu.NewSubmitSM())
	}()
	id := <-ids
	lifecycleWait(t, store.entered)
	// Reservation has passed the initial admission check. Hold the shutdown
	// lock so this producer must wait before committing to the available queue.
	out.submitMu.Lock()
	var unlockOnce sync.Once
	unlock := func() { unlockOnce.Do(out.submitMu.Unlock) }
	defer unlock()
	release()
	lifecycleAwaitQueueAdmissionLock(t, id, returned)
	atomic.StoreInt32(&out.aliveState, Closed)
	unlock()
	select {
	case err := <-returned:
		if !errors.Is(err, ErrConnectionClosing) {
			t.Fatalf("stopped queue admitted waiting producer: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown lock did not release waiting producer")
	}
}

func TestLifecycleCanceledReservationCannotEnterQueue(t *testing.T) {
	conn, cleanup := newPipePair()
	defer cleanup()
	store := &lifecycleGatedReservationStore{DefaultStore: NewDefaultStore(), entered: make(chan struct{}), release: make(chan struct{})}
	settings := lifecycleSettings()
	var failures atomic.Int32
	settings.OnSubmitError = func(pdu.PDU, error) { failures.Add(1) }
	out := newTransmittable(conn, settings, store)
	defer func() { _ = out.close(ConnectionIssue) }()
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(store.release) }) }
	defer release()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	returned := make(chan error, 1)
	go func() { returned <- out.SubmitContext(ctx, pdu.NewSubmitSM()) }()
	lifecycleWait(t, store.entered)
	cancel()
	release()
	select {
	case err := <-returned:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled reservation admitted to queue: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled reservation did not finish admission")
	}
	if err := out.close(ConnectionIssue); err != nil {
		t.Fatal(err)
	}
	length, err := store.Length(context.Background())
	if err != nil || length != 0 || len(out.input) != 0 || failures.Load() != 0 {
		t.Fatalf("canceled admission: reservations=%d queued=%d submit callbacks=%d error=%v", length, len(out.input), failures.Load(), err)
	}
}

type lifecycleGatedDoneContext struct {
	context.Context
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (c *lifecycleGatedDoneContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.entered) })
	<-c.release
	return c.Context.Done()
}

func TestLifecycleCanceledQueueWaiterWakesNextProducer(t *testing.T) {
	conn, cleanup := newPipePair()
	defer cleanup()
	writerEntered, writerGate := make(chan struct{}), make(chan struct{})
	out := newTransmittable(conn, Settings{OnSubmitError: func(_ pdu.PDU, err error) {
		if errors.Is(err, pdu.ErrEncode) {
			close(writerEntered)
			<-writerGate
		}
	}}, nil)
	defer func() { _ = out.close(ConnectionIssue) }()
	var releaseWriterOnce sync.Once
	releaseWriter := func() { releaseWriterOnce.Do(func() { close(writerGate) }) }
	defer releaseWriter()
	out.start()
	invalid := pdu.NewSubmitSM().(*pdu.SubmitSM)
	invalid.ServiceType = "invalid\x00service"
	if err := out.Submit(invalid); err != nil {
		t.Fatal(err)
	}
	lifecycleWait(t, writerEntered)
	if err := out.Submit(pdu.NewSubmitSM()); err != nil {
		t.Fatal(err)
	}
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	defer cancelFirst()
	nextCtx, cancelNext := context.WithCancel(context.Background())
	defer cancelNext()
	next := &lifecycleGatedDoneContext{Context: nextCtx, entered: make(chan struct{}), release: make(chan struct{})}
	var releaseNextOnce sync.Once
	releaseNext := func() { releaseNextOnce.Do(func() { close(next.release) }) }
	defer releaseNext()
	firstIDs, nextIDs := make(chan uint64, 1), make(chan uint64, 1)
	firstResult, nextResult := make(chan error, 1), make(chan error, 1)
	go func() {
		firstIDs <- callbackGoroutineID()
		firstResult <- out.SubmitContext(firstCtx, pdu.NewSubmitSM())
	}()
	firstID := <-firstIDs
	lifecycleAwaitSubmitWait(t, firstID)
	go func() {
		nextIDs <- callbackGoroutineID()
		nextResult <- out.SubmitContext(next, pdu.NewSubmitSM())
	}()
	nextID := <-nextIDs
	// The second producer is held before select registers its receive, so the
	// first producer is the only possible consumer of the writer's notification.
	lifecycleWait(t, next.entered)
	out.submitMu.Lock()
	var unlockOnce sync.Once
	unlock := func() { unlockOnce.Do(out.submitMu.Unlock) }
	defer unlock()
	releaseWriter()
	lifecycleAwaitQueueAdmissionLock(t, firstID, firstResult)
	releaseNext()
	lifecycleAwaitSubmitWait(t, nextID)
	cancelFirst()
	unlock()
	select {
	case err := <-firstResult:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled notification consumer: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled notification consumer did not return")
	}
	select {
	case err := <-nextResult:
		if err != nil {
			t.Fatalf("live producer lost queue admission: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("available queue lost its notification after the first waiter canceled")
	}
}

type lifecycleStopDuringReservationStore struct {
	DefaultStore
	stop func()
}

func (s *lifecycleStopDuringReservationStore) Set(ctx context.Context, request Request) error {
	if err := s.DefaultStore.Set(ctx, request); err != nil {
		return err
	}
	s.stop()
	return nil
}

func TestLifecycleStopDuringReservationRejectsQueueAdmission(t *testing.T) {
	conn, cleanup := newPipePair()
	defer cleanup()
	store := &lifecycleStopDuringReservationStore{DefaultStore: NewDefaultStore()}
	settings := lifecycleSettings()
	var failures atomic.Int32
	settings.OnSubmitError = func(pdu.PDU, error) { failures.Add(1) }
	out := newTransmittable(conn, settings, store)
	defer func() { _ = out.close(ConnectionIssue) }()
	store.stop = func() { out.stop(ConnectionIssue) }
	if err := out.Submit(pdu.NewSubmitSM()); !errors.Is(err, ErrConnectionClosing) {
		t.Fatalf("shutdown admitted reserved PDU: %v", err)
	}
	if err := out.close(ConnectionIssue); err != nil {
		t.Fatal(err)
	}
	length, err := store.Length(context.Background())
	if err != nil || length != 0 || len(out.input) != 0 || failures.Load() != 0 {
		t.Fatalf("rejected admission: reservations=%d queued=%d submit callbacks=%d error=%v", length, len(out.input), failures.Load(), err)
	}
}

func TestLifecycleExpiryCanClose(t *testing.T) {
	conn, cleanup := newPipePair()
	defer cleanup()
	store := NewDefaultStore()
	_ = store.Set(context.Background(), Request{PDU: pdu.NewSubmitSM(), TimeSent: time.Now().Add(-time.Hour), WrittenAt: time.Now().Add(-time.Hour)})
	s := lifecycleSettings()
	s.PduExpireTimeOut, s.ExpireCheckTimer = time.Millisecond, time.Millisecond
	closed := make(chan struct{})
	s.OnExpiredPduRequest = func(pdu.PDU) bool { return true }
	s.OnClosed = func(State) { close(closed) }
	v := newTransceivable(conn, s, store)
	v.start()
	lifecycleWait(t, closed)
	done := make(chan struct{})
	go func() { v.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Error("expiry worker waits for itself during shutdown")
	}
}

func TestLifecycleCloseFromPDUCallback(t *testing.T) {
	c, peer := net.Pipe()
	defer func() { _ = c.Close() }()
	defer func() { _ = peer.Close() }()
	done := make(chan struct{})
	var v *transceivable
	v = newTransceivable(NewConnection(c), Settings{ReadTimeout: time.Hour,
		OnPDU: func(pdu.PDU, bool) { _ = v.Close(); close(done) }}, nil)
	v.start()
	writeWire(t, peer, pdu.NewEnquireLinkResp())
	select {
	case <-done:
		_ = v.Close()
	case <-time.After(200 * time.Millisecond):
		t.Error("Close from OnPDU waits for its own reader")
	}
}

type lifecycleConnector struct{ connect func() (*Connection, error) }

func (c lifecycleConnector) Connect() (*Connection, error) { return c.connect() }
func (c lifecycleConnector) GetBindType() pdu.BindingType  { return pdu.Transceiver }

func TestSessionCloseDiscardsLateRebind(t *testing.T) {
	old, cleanOld := newPipePair()
	defer cleanOld()
	fresh, cleanFresh := newPipePair()
	defer cleanFresh()
	entered, release, rebound := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	s, err := NewSession(lifecycleConnector{connect: func() (*Connection, error) {
		if calls.Add(1) == 1 {
			return old, nil
		}
		close(entered)
		<-release
		return fresh, nil
	}}, Settings{ReadTimeout: time.Hour}, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	go func() { s.rebind(); close(rebound) }()
	lifecycleWait(t, entered)
	closed := make(chan struct{})
	go func() { _ = s.Close(); close(closed) }()
	deadline := time.Now().Add(time.Second)
	for atomic.LoadInt32(&s.state) == Alive && time.Now().Before(deadline) {
		runtime.Gosched()
	}
	select {
	case <-closed:
		t.Error("Close returned before the in-flight rebind finished")
	default:
	}
	close(release)
	lifecycleWait(t, rebound)
	lifecycleWait(t, closed)
	if s.bound().isAlive() {
		t.Error("late rebind resurrected a closed Session")
	}
	_ = s.bound().Close()
}

func TestRebindRetriesConnectionLostDuringOnRebind(t *testing.T) {
	old, cleanOld := newPipePair()
	defer cleanOld()
	freshConn, freshPeer := net.Pipe()
	defer func() { _ = freshPeer.Close() }()
	third, cleanThird := newPipePair()
	defer cleanThird()
	entered, release, failed := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var attempts atomic.Int32
	var callbackCount atomic.Int32
	s, err := NewSession(lifecycleConnector{connect: func() (*Connection, error) {
		switch attempts.Add(1) {
		case 1:
			return old, nil
		case 2:
			return NewConnection(freshConn), nil
		default:
			return third, nil
		}
	}}, Settings{ReadTimeout: time.Hour,
		OnRebind: func() {
			if callbackCount.Add(1) == 1 {
				close(entered)
				<-release
			}
		},
		OnClosed: func(state State) {
			if state == InvalidStreaming {
				close(failed)
			}
		},
	}, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	done := make(chan struct{})
	go func() { s.rebind(); close(done) }()
	lifecycleWait(t, entered)
	_ = freshPeer.Close()
	lifecycleWait(t, failed)
	// Wait for OnClosed's rebind request to finish before releasing OnRebind.
	bound := s.bound()
	closed := make(chan struct{})
	go func() { _ = bound.Close(); close(closed) }()
	lifecycleWait(t, closed)
	close(release)
	lifecycleWait(t, done)
	deadline := time.Now().Add(200 * time.Millisecond)
	for (attempts.Load() < 3 || !s.IsAlive()) && time.Now().Before(deadline) {
		runtime.Gosched()
	}
	if attempts.Load() < 3 || !s.IsAlive() {
		t.Error("connection failure during OnRebind lost the next rebind")
	}
}

func TestWindowOldConnectionCannotFlushNewRequests(t *testing.T) {
	old, cleanOld := newPipePair()
	defer cleanOld()
	fresh, cleanFresh := newPipePair()
	defer cleanFresh()
	store := NewDefaultStore()
	s := lifecycleSettings()
	var flushed atomic.Int32
	s.OnClosePduRequest = func(pdu.PDU) { flushed.Add(1) }
	oldWriter := newTransmittable(old, s, store)
	newWriter := newTransmittable(fresh, s, store)
	if _, err := newWriter.write(pdu.NewSubmitSM()); err != nil {
		t.Fatal(err)
	}
	_ = oldWriter.close(ConnectionIssue)
	n, _ := store.Length(context.Background())
	if n != 1 || flushed.Load() != 0 {
		t.Errorf("old cleanup deleted fresh request: window=%d flushed=%d", n, flushed.Load())
	}
	_ = newWriter.close(ExplicitClosing)
}

type lifecycleSetGate struct {
	DefaultStore
	entered, release chan struct{}
}

func (s *lifecycleSetGate) Set(ctx context.Context, request Request) error {
	close(s.entered)
	<-s.release
	return s.DefaultStore.Set(ctx, request)
}

func TestWindowReservesBeforeWrite(t *testing.T) {
	c, peer := net.Pipe()
	defer func() { _ = c.Close() }()
	defer func() { _ = peer.Close() }()
	entered := make(chan struct{})
	store := &lifecycleSetGate{DefaultStore: NewDefaultStore(), entered: make(chan struct{}), release: make(chan struct{})}
	s := lifecycleSettings()
	responses := make(chan Response, 1)
	s.OnExpectedPduResponse = func(r Response) { responses <- r }
	v := newTransceivable(NewConnection(&lifecycleWriteConn{Conn: c, entered: entered}), s, store)
	v.start()
	peerDone := make(chan struct{})
	go func() {
		defer close(peerDone)
		r, err := pdu.Parse(peer)
		if err != nil {
			return
		}
		b := pdu.NewBuffer(nil)
		r.GetResponse().Marshal(b)
		_, _ = peer.Write(b.Bytes())
	}()
	submitted := make(chan error, 1)
	go func() { submitted <- v.Submit(pdu.NewSubmitSM()) }()
	lifecycleWait(t, store.entered)
	select {
	case <-entered:
		t.Error("request reached socket before its window reservation")
	default:
	}
	close(store.release)
	if err := <-submitted; err != nil {
		t.Fatal(err)
	}
	select {
	case <-responses:
	case <-time.After(200 * time.Millisecond):
		t.Error("immediate response was not correlated")
	}
	_ = v.Close()
	lifecycleWait(t, peerDone)
}

func TestWindowRetiresWithoutResponseCallback(t *testing.T) {
	store := NewDefaultStore()
	r := pdu.NewSubmitSM()
	_ = store.Set(context.Background(), Request{PDU: r, TimeSent: time.Now()})
	v := newReceivable(nil, lifecycleSettings(), store)
	v.handleWindowPdu(r.GetResponse())
	n, _ := store.Length(context.Background())
	if n != 0 {
		t.Errorf("answered request still occupies window: %d", n)
	}
}

func TestWindowReceiveLoopRetiresWithoutResponseCallback(t *testing.T) {
	c, peer := net.Pipe()
	defer func() { _ = c.Close() }()
	defer func() { _ = peer.Close() }()
	store := NewDefaultStore()
	request := pdu.NewSubmitSM()
	_ = store.Set(context.Background(), Request{PDU: request, TimeSent: time.Now()})
	v := newTransceivable(NewConnection(c), lifecycleSettings(), store)
	v.start()
	defer func() { _ = v.Close() }()
	writeWire(t, peer, request.GetResponse())
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if n, _ := store.Length(context.Background()); n == 0 {
			return
		}
		runtime.Gosched()
	}
	t.Error("receive loop left an answered request in the window without a callback")
}

func TestWindowGenericNackCompletesRequest(t *testing.T) {
	store := NewDefaultStore()
	r := pdu.NewSubmitSM()
	_ = store.Set(context.Background(), Request{PDU: r, TimeSent: time.Now()})
	s := lifecycleSettings()
	var expected, received int
	s.OnExpectedPduResponse = func(Response) { expected++ }
	s.OnReceivedPduRequest = func(pdu.PDU) (pdu.PDU, bool) { received++; return nil, false }
	s.response = func(pdu.PDU) {}
	v := newReceivable(nil, s, store)
	nack := pdu.NewGenericNack()
	nack.SetSequenceNumber(r.GetSequenceNumber())
	nack.SetCommandStatus(data.ESME_RINVCMDID)
	v.handleWindowPdu(nack)
	n, _ := store.Length(context.Background())
	if n != 0 || expected != 1 || received != 0 {
		t.Errorf("generic_nack: window=%d response=%d request=%d", n, expected, received)
	}
}

type lifecycleSnapshotStore struct {
	DefaultStore
	snapshot, release, resumed chan struct{}
	once                       sync.Once
}

func (s *lifecycleSnapshotStore) List(ctx context.Context) []Request {
	requests := s.DefaultStore.List(ctx)
	s.once.Do(func() { close(s.snapshot); <-s.release; close(s.resumed) })
	return requests
}

func TestWindowResponseWinsExpirySnapshot(t *testing.T) {
	conn, cleanup := newPipePair()
	defer cleanup()
	store := &lifecycleSnapshotStore{DefaultStore: NewDefaultStore(), snapshot: make(chan struct{}), release: make(chan struct{}), resumed: make(chan struct{})}
	r := pdu.NewSubmitSM()
	_ = store.Set(context.Background(), Request{PDU: r, TimeSent: time.Now().Add(-time.Hour), WrittenAt: time.Now().Add(-time.Hour)})
	s := lifecycleSettings()
	s.PduExpireTimeOut, s.ExpireCheckTimer = time.Millisecond, time.Millisecond
	var expired, responses atomic.Int32
	s.OnExpectedPduResponse = func(Response) { responses.Add(1) }
	s.OnExpiredPduRequest = func(pdu.PDU) bool { expired.Add(1); return false }
	v := newTransceivable(conn, s, store)
	v.start()
	lifecycleWait(t, store.snapshot)
	v.in.handleWindowPdu(r.GetResponse())
	close(store.release)
	lifecycleWait(t, store.resumed)
	_ = v.Close()
	if expired.Load() != 0 || responses.Load() != 1 {
		t.Errorf("response and expiry both claimed request: response=%d expiry=%d", responses.Load(), expired.Load())
	}
}

func TestWindowZeroExpiryDisabled(t *testing.T) {
	conn, cleanup := newPipePair()
	defer cleanup()
	store := NewDefaultStore()
	_ = store.Set(context.Background(), Request{PDU: pdu.NewSubmitSM(), TimeSent: time.Now().Add(-time.Hour), WrittenAt: time.Now().Add(-time.Hour)})
	s := lifecycleSettings()
	s.ExpireCheckTimer = time.Millisecond
	var expired atomic.Int32
	s.OnExpiredPduRequest = func(pdu.PDU) bool { expired.Add(1); return false }
	v := newTransceivable(conn, s, store)
	v.start()
	// A bounded observation verifies the absence of an expiry daemon; no ordering depends on this timer.
	<-time.After(20 * time.Millisecond)
	_ = v.Close()
	if expired.Load() != 0 {
		t.Error("zero expiry timeout expired requests")
	}
}

type lifecyclePartialConn struct {
	net.Conn
	n int
}

func (c lifecyclePartialConn) Write([]byte) (int, error) {
	return c.n, &net.OpError{Op: "write", Net: "tcp", Err: syscall.EPIPE}
}

func TestLifecycleWriteFailurePreservesCountAndCloses(t *testing.T) {
	for _, n := range []int{0, 8} {
		t.Run(string(rune('0'+n)), func(t *testing.T) {
			c, peer := net.Pipe()
			defer func() { _ = c.Close() }()
			defer func() { _ = peer.Close() }()
			v := newTransmittable(NewConnection(lifecyclePartialConn{Conn: c, n: n}), lifecycleSettings(), NewDefaultStore())
			p := pdu.NewSubmitSM()
			got, err := v.write(p)
			if got != n || !v.check(p, got, err) {
				t.Errorf("permanent write failure: n=%d want=%d closing=false err=%v", got, n, err)
			}
			if pending, _ := v.requestStore.Length(context.Background()); pending != 0 {
				t.Errorf("failed write left %d window reservations", pending)
			}
			_ = v.close(ExplicitClosing)
		})
	}
}

func TestRebindCloseLeavesNoWorkers(t *testing.T) {
	baseline := runtime.NumGoroutine()
	for range 5 {
		var cleanups []func()
		s, err := NewSession(lifecycleConnector{connect: func() (*Connection, error) {
			conn, cleanup := newPipePair()
			cleanups = append(cleanups, cleanup)
			return conn, nil
		}}, Settings{ReadTimeout: time.Hour}, time.Millisecond)
		if err != nil {
			t.Fatal(err)
		}
		s.rebind()
		_ = s.Close()
		for _, cleanup := range cleanups {
			cleanup()
		}
	}
	deadline := time.Now().Add(time.Second)
	for runtime.NumGoroutine() > baseline && time.Now().Before(deadline) {
		runtime.Gosched()
	}
	if got := runtime.NumGoroutine(); got > baseline {
		b := make([]byte, 1<<20)
		n := runtime.Stack(b, true)
		t.Fatalf("rebind workers did not return to baseline %d: got %d\n%s", baseline, got, strings.TrimSpace(string(b[:n])))
	}
}

func TestLifecycleCloseJoinsExistingAsyncClose(t *testing.T) {
	conn, cleanup := newPipePair()
	defer cleanup()
	entered, release := make(chan struct{}), make(chan struct{})
	var callbacks atomic.Int32
	v := newTransceivable(conn, Settings{ReadTimeout: time.Hour, OnClosed: func(State) {
		if callbacks.Add(1) == 1 {
			close(entered)
			<-release
		}
	}}, nil)
	v.start()
	v.in.closing(InvalidStreaming)
	lifecycleWait(t, entered)
	done := make(chan struct{})
	go func() { _ = v.Close(); close(done) }()
	select {
	case <-done:
		t.Error("Close returned while OnClosed was still executing")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	lifecycleWait(t, done)
}

func TestLifecycleCloseLeavesNoWorkers(t *testing.T) {
	baseline := runtime.NumGoroutine()
	for range 10 {
		conn, cleanup := newPipePair()
		v := newTransceivable(conn, lifecycleSettings(), NewDefaultStore())
		v.start()
		_ = v.Close()
		cleanup()
	}
	deadline := time.Now().Add(time.Second)
	for runtime.NumGoroutine() > baseline && time.Now().Before(deadline) {
		runtime.Gosched()
	}
	if got := runtime.NumGoroutine(); got > baseline {
		b := make([]byte, 1<<20)
		n := runtime.Stack(b, true)
		t.Fatalf("goroutines did not return to baseline %d: got %d\n%s", baseline, got, strings.TrimSpace(string(b[:n])))
	}
}
