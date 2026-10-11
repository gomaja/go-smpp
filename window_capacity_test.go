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

func TestWindowPendingReservationsDoNotConsumeCapacity(t *testing.T) {
	conn, cleanup := newPipePair()
	defer cleanup()
	store := NewDefaultStore()
	settings := lifecycleSettings()
	settings.MaxWindowSize = 10
	out := newTransmittable(conn, settings, store)
	defer func() { _ = out.close(ExplicitClosing) }()
	for range 16 {
		if err := store.Set(context.Background(), Request{PDU: pdu.NewSubmitSM(), TimeSent: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := out.write(pdu.NewSubmitSM()); err != nil {
		t.Fatalf("pending reservations rejected writable request: %v", err)
	}
}

func TestWindowKeepaliveBypassesFullApplicationWindow(t *testing.T) {
	conn, cleanup := newPipePair()
	defer cleanup()
	store := NewDefaultStore()
	settings := lifecycleSettings()
	settings.MaxWindowSize = 1
	out := newTransmittable(conn, settings, store)
	defer func() { _ = out.close(ExplicitClosing) }()
	if _, err := out.write(pdu.NewSubmitSM()); err != nil {
		t.Fatal(err)
	}
	if _, err := out.write(pdu.NewEnquireLink()); err != nil {
		t.Fatalf("full application window suppressed keepalive: %v", err)
	}
}

type windowRecoveryGateStore struct {
	DefaultStore
	entered, release chan struct{}
	once             sync.Once
	lists            atomic.Int32
}

func (s *windowRecoveryGateStore) List(ctx context.Context) []Request {
	s.lists.Add(1)
	s.once.Do(func() { close(s.entered) })
	select {
	case <-s.release:
		return s.DefaultStore.List(ctx)
	case <-ctx.Done():
		return nil
	}
}
func TestWindowFullRecoveryDoesNotWaitForList(t *testing.T) {
	conn, cleanup := newPipePair()
	defer cleanup()
	store := &windowRecoveryGateStore{DefaultStore: NewDefaultStore(), entered: make(chan struct{}), release: make(chan struct{})}
	settings := lifecycleSettings()
	settings.MaxWindowSize = 1
	settings.StoreAccessTimeOut = time.Second
	out := newTransmittable(conn, settings, store)
	defer func() { close(store.release); _ = out.close(ExplicitClosing) }()
	if err := store.Set(context.Background(), Request{PDU: pdu.NewSubmitSM(), WrittenAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	returned := make(chan error, 1)
	go func() { _, err := out.write(pdu.NewSubmitSM()); returned <- err }()
	lifecycleWait(t, store.entered)
	select {
	case err := <-returned:
		if !errors.Is(err, ErrWindowsFull) {
			t.Fatalf("write error: %v", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("writer waited for recovery List")
	}
	for range 20 {
		if _, err := out.write(pdu.NewSubmitSM()); !errors.Is(err, ErrWindowsFull) {
			t.Fatalf("full window: %v", err)
		}
	}
	if got := store.lists.Load(); got != 1 {
		t.Fatalf("full writes launched %d concurrent scans", got)
	}
}

func TestWindowFullRecoveryReclaimsOrphanWithoutHint(t *testing.T) {
	oldConn, oldCleanup := newPipePair()
	defer oldCleanup()
	conn, cleanup := newPipePair()
	defer cleanup()
	store := &windowDeadOwnerStore{DefaultStore: NewDefaultStore()}
	settings := lifecycleSettings()
	settings.MaxWindowSize = 1
	old := newTransmittable(oldConn, settings, store)
	defer func() { _ = old.close(ExplicitClosing) }()
	orphan := pdu.NewSubmitSM()
	if _, err := old.write(orphan); err != nil {
		t.Fatal(err)
	}
	store.failDeletes.Store(true)
	retireWindowGeneration(t, &transceivable{out: old})
	store.failDeletes.Store(false)
	var closed atomic.Int32
	settings.OnClosePduRequest = func(request pdu.PDU) {
		if request == orphan {
			closed.Add(1)
		}
	}
	out := newTransmittable(conn, settings, store)
	out.lineage = old.lineage
	defer func() { _ = out.close(ExplicitClosing) }()
	// A missing cleanup hint must not leave a saturated rebound window stuck.
	out.lineage.orphaned.Store(false)
	if _, err := out.write(pdu.NewSubmitSM()); !errors.Is(err, ErrWindowsFull) {
		t.Fatalf("saturated orphan window: %v", err)
	}
	out.reclaimWG.Wait()
	remaining, err := store.Length(context.Background())
	if err != nil || remaining != 0 || closed.Load() != 1 {
		t.Fatalf("full-window recovery: remaining=%d close callbacks=%d error=%v", remaining, closed.Load(), err)
	}
	if _, err := out.write(pdu.NewSubmitSM()); err != nil {
		t.Fatalf("reclaimed window was not reusable: %v", err)
	}
}

type windowMarkErrorStore struct {
	DefaultStore
	failed atomic.Bool
}

func (s *windowMarkErrorStore) MarkWritten(ctx context.Context, r Request, at time.Time) (bool, error) {
	if !s.failed.Swap(true) {
		return false, errors.New("temporary mark failure")
	}
	return s.DefaultStore.MarkWritten(ctx, r, at)
}
func TestWindowMarkFailurePreservesTransport(t *testing.T) {
	conn, cleanup := newPipePair()
	defer cleanup()
	store := &windowMarkErrorStore{DefaultStore: NewDefaultStore()}
	settings := lifecycleSettings()
	var reported atomic.Int32
	settings.OnReceivingError = func(error) { reported.Add(1) }
	out := newTransmittable(conn, settings, store)
	defer func() { _ = out.close(ExplicitClosing) }()
	request := pdu.NewSubmitSM()
	n, err := out.write(request)
	if out.check(request, n, err) {
		t.Fatalf("metadata failure closed healthy transport: %v", err)
	}
	if _, err := out.write(pdu.NewSubmitSM()); err != nil {
		t.Fatal(err)
	}
	if reported.Load() != 1 {
		t.Fatalf("store reports=%d", reported.Load())
	}
}

type windowUncertainSetStore struct {
	DefaultStore
	failDelete atomic.Bool
}

func (s *windowUncertainSetStore) Set(ctx context.Context, r Request) error {
	if err := s.DefaultStore.Set(ctx, r); err != nil {
		return err
	}
	return errors.New("set acknowledgement lost")
}
func (s *windowUncertainSetStore) Delete(ctx context.Context, r Request) (bool, error) {
	if s.failDelete.Load() {
		return false, errors.New("delete unavailable")
	}
	return s.DefaultStore.Delete(ctx, r)
}
func TestWindowSetErrorCleansUncertainReservation(t *testing.T) {
	conn, cleanup := newPipePair()
	defer cleanup()
	store := &windowUncertainSetStore{DefaultStore: NewDefaultStore()}
	out := newTransmittable(conn, lifecycleSettings(), store)
	defer func() { _ = out.close(ExplicitClosing) }()
	if err := out.Submit(pdu.NewSubmitSM()); err == nil {
		t.Fatal("Set failure accepted")
	}
	if n, _ := store.Length(context.Background()); n != 0 {
		t.Fatalf("failed admission retained %d reservations", n)
	}
}

func TestWindowSetErrorDeferredCleanupIsSilent(t *testing.T) {
	conn, cleanup := newPipePair()
	defer cleanup()
	store := &windowUncertainSetStore{DefaultStore: NewDefaultStore()}
	store.failDelete.Store(true)
	settings := lifecycleSettings()
	var callbacks atomic.Int32
	settings.OnClosePduRequest = func(pdu.PDU) { callbacks.Add(1) }
	out := newTransmittable(conn, settings, store)
	if err := out.Submit(pdu.NewSubmitSM()); err == nil {
		t.Fatal("Set failure accepted")
	}
	store.failDelete.Store(false)
	if err := out.close(ExplicitClosing); err != nil {
		t.Fatal(err)
	}
	if callbacks.Load() != 0 {
		t.Fatalf("rejected admission produced %d completion callbacks", callbacks.Load())
	}
}

type windowEmptyListStore struct {
	*windowDeadOwnerStore
	empty atomic.Bool
}

func (s *windowEmptyListStore) List(ctx context.Context) []Request {
	if s.empty.Load() {
		return nil
	}
	return s.windowDeadOwnerStore.List(ctx)
}

func TestWindowFinalClosedForeignSessionOrphansAreSilent(t *testing.T) {
	store := &windowDeadOwnerStore{DefaultStore: NewDefaultStore()}
	firstConn, cleanFirst := newPipePair()
	defer cleanFirst()
	nextConn, cleanNext := newPipePair()
	defer cleanNext()
	settings := lifecycleSettings()
	settings.PduExpireTimeOut = time.Nanosecond
	settings.ExpireCheckTimer = time.Hour
	var firstCallbacks, foreignCallbacks atomic.Int32
	settings.OnClosePduRequest = func(pdu.PDU) { firstCallbacks.Add(1) }
	first, err := NewSession(lifecycleConnector{connect: func() (*Connection, error) { return firstConn, nil }}, settings, 0, WithRequestStore(store))
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err = first.bound().out.write(pdu.NewSubmitSM()); err != nil {
			t.Fatal(err)
		}
	}
	store.failDeletes.Store(true)
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	store.failDeletes.Store(false)
	nextSettings := settings
	window := *settings.WindowedRequestTracking
	nextSettings.WindowedRequestTracking = &window
	nextSettings.OnClosePduRequest = func(pdu.PDU) { foreignCallbacks.Add(1) }
	nextSettings.OnExpiredPduRequest = func(pdu.PDU) bool { foreignCallbacks.Add(1); return false }
	next, err := NewSession(lifecycleConnector{connect: func() (*Connection, error) { return nextConn, nil }}, nextSettings, 0, WithRequestStore(store))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = next.Close() }()
	next.bound().expireRequests()
	if n, _ := store.Length(context.Background()); n != 0 {
		t.Fatalf("closed foreign Session left %d unreachable reservations", n)
	}
	if firstCallbacks.Load() != 3 || foreignCallbacks.Load() != 0 {
		t.Fatalf("orphan callbacks: first=%d foreign=%d", firstCallbacks.Load(), foreignCallbacks.Load())
	}
}

func TestWindowFastEmptyListRetainsRebindCleanupHint(t *testing.T) {
	store := &windowEmptyListStore{windowDeadOwnerStore: &windowDeadOwnerStore{DefaultStore: NewDefaultStore()}}
	firstConn, cleanFirst := newPipePair()
	defer cleanFirst()
	nextConn, cleanNext := newPipePair()
	defer cleanNext()
	settings := lifecycleSettings()
	var connected, completed atomic.Int32
	settings.OnClosePduRequest = func(pdu.PDU) { completed.Add(1) }
	session, err := NewSession(lifecycleConnector{connect: func() (*Connection, error) {
		if connected.Add(1) == 1 {
			return firstConn, nil
		}
		return nextConn, nil
	}}, settings, 0, WithRequestStore(store))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	for range 3 {
		if _, err = session.bound().out.write(pdu.NewSubmitSM()); err != nil {
			t.Fatal(err)
		}
	}
	store.empty.Store(true)
	store.failDeletes.Store(true)
	old := session.bound()
	if err = old.Close(); err != nil {
		t.Fatal(err)
	}
	if !old.out.lineage.orphaned.Load() {
		t.Error("fast-empty List lost outstanding cleanup hint")
	}
	store.empty.Store(false)
	store.failDeletes.Store(false)
	session.rebind()
	next := session.bound()
	if next == old {
		t.Fatal("did not rebind")
	}
	if _, err = next.out.write(pdu.NewSubmitSM()); err != nil {
		t.Fatal(err)
	}
	next.out.reclaimWG.Wait()
	if completed.Load() != 3 {
		t.Fatalf("recovered old completions=%d, want 3", completed.Load())
	}
}

func TestWindowMarkFailureRepairsWrittenMetadata(t *testing.T) {
	conn, cleanup := newPipePair()
	defer cleanup()
	store := &windowMarkErrorStore{DefaultStore: NewDefaultStore()}
	settings := lifecycleSettings()
	out := newTransmittable(conn, settings, store)
	defer func() { _ = out.close(ExplicitClosing) }()
	request := pdu.NewSubmitSM()
	if _, err := out.write(request); err != nil {
		t.Fatal(err)
	}
	out.reclaimWG.Wait()
	saved, ok := store.Get(context.Background(), request.GetSequenceNumber())
	if !ok || saved.WrittenAt.IsZero() {
		t.Fatal("successful write remained pending after metadata retry")
	}
}

type windowProducerStore struct {
	DefaultStore
	reserved    atomic.Int32
	allReserved chan struct{}
	responses   chan struct{}
}

func (s *windowProducerStore) Set(ctx context.Context, r Request) error {
	err := s.DefaultStore.Set(ctx, r)
	if err == nil && s.reserved.Add(1) == 16 {
		close(s.allReserved)
	}
	return err
}
func (s *windowProducerStore) MarkWritten(ctx context.Context, r Request, at time.Time) (bool, error) {
	// Wait until the immediate response has claimed this write before the writer
	// starts its next application request; queue contention remains concurrent.
	select {
	case <-s.responses:
	case <-ctx.Done():
		return false, ctx.Err()
	}
	return s.DefaultStore.MarkWritten(ctx, r, at)
}
func TestWindowSixteenProducersUseWrittenCapacity(t *testing.T) {
	raw, peer := net.Pipe()
	store := &windowProducerStore{DefaultStore: NewDefaultStore(), allReserved: make(chan struct{}), responses: make(chan struct{}, 16)}
	settings := lifecycleSettings()
	settings.MaxWindowSize = 10
	events := make(chan error, 16)
	settings.OnExpectedPduResponse = func(Response) { store.responses <- struct{}{}; events <- nil }
	settings.OnSubmitError = func(_ pdu.PDU, err error) { events <- err }
	v := newTransceivable(NewConnection(raw), settings, store)
	defer func() { _ = peer.Close(); _ = v.Close() }()
	peerDone := make(chan struct{})
	go func() {
		defer close(peerDone)
		for {
			request, err := pdu.Parse(peer)
			if err != nil {
				return
			}
			response := request.GetResponse()
			if response == nil {
				continue
			}
			buf := pdu.NewBuffer(nil)
			response.Marshal(buf)
			if _, err = peer.Write(buf.Bytes()); err != nil {
				return
			}
		}
	}()
	submissions := make(chan error, 16)
	for range 16 {
		go func() { submissions <- v.Submit(pdu.NewSubmitSM()) }()
	}
	lifecycleWait(t, store.allReserved)
	v.start()
	for range 16 {
		select {
		case err := <-submissions:
			if err != nil {
				t.Errorf("submission: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("producer blocked")
		}
	}
	for range 16 {
		select {
		case err := <-events:
			if err != nil {
				t.Errorf("immediate-response request rejected: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("request did not complete")
		}
	}
	_ = peer.Close()
	lifecycleWait(t, peerDone)
}

func TestWindowFullRecoveryRateLimitsCompletedScans(t *testing.T) {
	conn, cleanup := newPipePair()
	defer cleanup()
	store := &windowCountStore{DefaultStore: NewDefaultStore()}
	settings := lifecycleSettings()
	settings.MaxWindowSize = 1
	settings.ExpireCheckTimer = time.Hour
	if err := store.Set(context.Background(), Request{PDU: pdu.NewSubmitSM(), WrittenAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	out := newTransmittable(conn, settings, store)
	defer func() { _ = out.close(ExplicitClosing) }()
	for range 20 {
		if _, err := out.write(pdu.NewSubmitSM()); !errors.Is(err, ErrWindowsFull) {
			t.Fatal(err)
		}
		out.reclaimWG.Wait()
	}
	if scans := store.lists.Load(); scans != 1 {
		t.Fatalf("completed scans bypassed rate limit: %d", scans)
	}
}

type windowTimeoutListStore struct {
	DefaultStore
	entered chan struct{}
	once    sync.Once
}

func (s *windowTimeoutListStore) List(ctx context.Context) []Request {
	s.once.Do(func() { close(s.entered) })
	<-ctx.Done()
	return nil
}
func TestWindowRecoveryListTimeoutRetainsHint(t *testing.T) {
	conn, cleanup := newPipePair()
	defer cleanup()
	store := &windowTimeoutListStore{DefaultStore: NewDefaultStore(), entered: make(chan struct{})}
	settings := lifecycleSettings()
	settings.MaxWindowSize = 1
	settings.StoreAccessTimeOut = time.Millisecond
	if err := store.Set(context.Background(), Request{PDU: pdu.NewSubmitSM(), WrittenAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	out := newTransmittable(conn, settings, store)
	defer func() { _ = out.close(ExplicitClosing) }()
	out.reclaimDeadRequests()
	lifecycleWait(t, store.entered)
	out.reclaimWG.Wait()
	if !out.lineage.orphaned.Load() {
		t.Fatal("timed-out recovery lost retry hint")
	}
}

type windowEmptyRepairStore struct {
	windowMarkErrorStore
	empty atomic.Bool
}

func (s *windowEmptyRepairStore) List(ctx context.Context) []Request {
	if s.empty.Load() {
		return nil
	}
	return s.DefaultStore.List(ctx)
}
func TestWindowFastEmptyListRetainsMetadataRepairHint(t *testing.T) {
	conn, cleanup := newPipePair()
	defer cleanup()
	store := &windowEmptyRepairStore{windowMarkErrorStore: windowMarkErrorStore{DefaultStore: NewDefaultStore()}}
	store.empty.Store(true)
	settings := lifecycleSettings()
	settings.ExpireCheckTimer = time.Nanosecond
	out := newTransmittable(conn, settings, store)
	defer func() { _ = out.close(ExplicitClosing) }()
	request := pdu.NewSubmitSM()
	if _, err := out.write(request); err != nil {
		t.Fatal(err)
	}
	out.reclaimWG.Wait()
	if !out.lineage.orphaned.Load() {
		t.Fatal("empty List lost known written metadata repair")
	}
	store.empty.Store(false)
	if _, err := out.write(pdu.NewSubmitSM()); err != nil {
		t.Fatal(err)
	}
	out.reclaimWG.Wait()
	saved, _ := store.Get(context.Background(), request.GetSequenceNumber())
	if saved.WrittenAt.IsZero() {
		t.Fatal("known write remained pending after store recovery")
	}
	if out.lineage.orphaned.Load() {
		t.Fatal("completed metadata repair remained outstanding")
	}
}

func TestWindowSharedSessionsRepairForeignWrittenCapacity(t *testing.T) {
	store := &windowMarkErrorStore{DefaultStore: NewDefaultStore()}
	firstConn, cleanFirst := newPipePair()
	defer cleanFirst()
	nextConn, cleanNext := newPipePair()
	defer cleanNext()
	settings := lifecycleSettings()
	settings.MaxWindowSize = 1
	var foreignCallbacks atomic.Int32
	first, err := NewSession(lifecycleConnector{connect: func() (*Connection, error) { return firstConn, nil }}, settings, 0, WithRequestStore(store))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Close() }()
	request := pdu.NewSubmitSM()
	if _, err = first.bound().out.write(request); err != nil {
		t.Fatal(err)
	}
	first.bound().out.reclaimWG.Wait()
	window := *settings.WindowedRequestTracking
	settings.WindowedRequestTracking = &window
	settings.OnClosePduRequest = func(pdu.PDU) { foreignCallbacks.Add(1) }
	next, err := NewSession(lifecycleConnector{connect: func() (*Connection, error) { return nextConn, nil }}, settings, 0, WithRequestStore(store))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = next.Close() }()
	if _, err = next.bound().out.write(pdu.NewSubmitSM()); !errors.Is(err, ErrWindowsFull) {
		t.Fatalf("foreign repaired write lost shared occupancy: %v", err)
	}
	next.bound().out.reclaimWG.Wait()
	if foreignCallbacks.Load() != 0 {
		t.Fatal("foreign Session claimed live request callback")
	}
	first.bound().in.completeResponse(request.GetResponse(), request.GetSequenceNumber(), nil)
	if _, err = next.bound().out.write(pdu.NewSubmitSM()); err != nil {
		t.Fatal(err)
	}
}

func TestWindowMetadataOutageKeepsOwnWrittenCapacity(t *testing.T) {
	conn, cleanup := newPipePair()
	defer cleanup()
	settings := lifecycleSettings()
	settings.MaxWindowSize = 1
	v := newTransceivable(conn, settings, windowMarkFailureStore{NewDefaultStore()})
	defer func() { _ = v.Close() }()
	if _, err := v.out.write(pdu.NewSubmitSM()); err != nil {
		t.Fatal(err)
	}
	v.out.reclaimWG.Wait()
	if size, err := v.GetWindowSize(); err != nil || size != 1 {
		t.Fatalf("metadata outage lost completed write: size=%d error=%v", size, err)
	}
	if _, err := v.out.write(pdu.NewSubmitSM()); !errors.Is(err, ErrWindowsFull) {
		t.Fatalf("metadata outage bypassed window: %v", err)
	}
}

type windowWriteBudgetStore struct {
	DefaultStore
	beforeWrite        context.Context
	reservationContext context.Context
	markError          error
	markOnce           sync.Once
}

func (s *windowWriteBudgetStore) WrittenLength(ctx context.Context, excludeOwner uint64) (int, error) {
	s.beforeWrite = ctx
	counter, ok := any(s.DefaultStore).(interface {
		WrittenLength(context.Context, uint64) (int, error)
	})
	if !ok {
		return s.DefaultStore.Length(ctx)
	}
	return counter.WrittenLength(ctx, excludeOwner)
}
func (s *windowWriteBudgetStore) MarkWritten(ctx context.Context, r Request, at time.Time) (bool, error) {
	s.markOnce.Do(func() { s.markError = ctx.Err() })
	return s.DefaultStore.MarkWritten(ctx, r, at)
}
func TestWindowBlockedWriteStartsFreshStoreBudget(t *testing.T) {
	raw, peer := net.Pipe()
	defer func() { _ = peer.Close() }()
	gate := &windowWriteGate{Conn: raw, entered: make(chan struct{}), release: make(chan struct{})}
	settings := lifecycleSettings()
	settings.StoreAccessTimeOut = time.Second
	store := &windowWriteBudgetStore{DefaultStore: NewDefaultStore()}
	out := newTransmittable(NewConnection(gate), settings, store)
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(gate.release) }) }
	done := make(chan struct{})
	writeError := make(chan error, 1)
	defer func() {
		release()
		defer func() { _ = out.close(ExplicitClosing) }()
		lifecycleWait(t, done)
	}()
	go func() {
		defer close(done)
		_, err := out.write(pdu.NewSubmitSM())
		writeError <- err
	}()
	select {
	case <-gate.entered:
	case <-done:
		t.Fatalf("write returned before reaching transport: %v", <-writeError)
	case <-time.After(2 * time.Second):
		t.Fatal("write did not reach transport")
	}
	if store.beforeWrite != store.reservationContext {
		t.Error("count and reservation used separate pre-write budgets")
	}
	// Transport starts after cancellation of the pre-write context. Await that
	// cancellation rather than relying on a short scheduling deadline.
	lifecycleWait(t, store.beforeWrite.Done())
	release()
	lifecycleWait(t, done)
	if err := <-writeError; err != nil {
		t.Fatalf("blocked write failed: %v", err)
	}
	if store.markError != nil {
		t.Fatalf("blocked transport consumed metadata deadline: %v", store.markError)
	}
}

func (s *windowWriteBudgetStore) Length(ctx context.Context) (int, error) {
	s.beforeWrite = ctx
	return s.DefaultStore.Length(ctx)
}

func (s *windowWriteBudgetStore) Set(ctx context.Context, r Request) error {
	s.reservationContext = ctx
	return s.DefaultStore.Set(ctx, r)
}

func TestWindowRepeatedWritesKeepOwnerAccounting(t *testing.T) {
	conn, cleanup := newPipePair()
	defer cleanup()
	settings := lifecycleSettings()
	settings.MaxWindowSize = 2
	v := newTransceivable(conn, settings, NewDefaultStore())
	defer func() { _ = v.Close() }()
	if _, err := v.out.write(pdu.NewSubmitSM()); err != nil {
		t.Fatal(err)
	}
	owner, _ := liveRequestOwners.Load(v.out.owner)
	if _, err := v.out.write(pdu.NewSubmitSM()); err != nil {
		t.Fatal(err)
	}
	current, _ := liveRequestOwners.Load(v.out.owner)
	if current != owner {
		t.Error("repeated write replaced generation accounting")
	}
	if count, err := v.GetWindowSize(); err != nil || count != 2 {
		t.Fatalf("owner lost previous written request: count=%d error=%v", count, err)
	}
	if _, err := v.out.write(pdu.NewSubmitSM()); !errors.Is(err, ErrWindowsFull) {
		t.Fatalf("repeated registration lost window capacity: %v", err)
	}
}
