package gosmpp

import (
	"context"
	"errors"
	"net"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gomaja/go-smpp/pdu"
)

var errWindowResponseDelete = errors.New("response store unavailable")

type windowResponseFailureStore struct {
	DefaultStore
	failDelete atomic.Bool
	failGet    atomic.Bool
	missingGet atomic.Bool
	getEntered chan struct{}
}

func (s *windowResponseFailureStore) Delete(ctx context.Context, request Request) (bool, error) {
	if s.failDelete.Load() {
		return false, errWindowResponseDelete
	}
	return s.DefaultStore.Delete(ctx, request)
}

func (s *windowResponseFailureStore) Get(ctx context.Context, sequence int32) (Request, bool) {
	if s.missingGet.Load() {
		return Request{}, false
	}
	if s.failGet.Load() {
		s.getEntered <- struct{}{}
		<-ctx.Done()
		return Request{}, false
	}
	return s.DefaultStore.Get(ctx, sequence)
}

// A reader on the peer keeps transport writes independent of callback gates.
// Cleanup joins it, including when an assertion fails before the last response.
func windowResponsePeer(t *testing.T) (*Connection, net.Conn, <-chan pdu.PDU) {
	t.Helper()
	client, peer := net.Pipe()
	frames, done := make(chan pdu.PDU, 16), make(chan struct{})
	go func() {
		defer close(done)
		for {
			frame, err := pdu.Parse(peer)
			if err != nil {
				return
			}
			frames <- frame
		}
	}()
	t.Cleanup(func() {
		_ = peer.Close()
		_ = client.Close()
		lifecycleWait(t, done)
	})
	return NewConnection(client), peer, frames
}

func windowResponseFrame(t *testing.T, frames <-chan pdu.PDU) pdu.PDU {
	t.Helper()
	select {
	case frame := <-frames:
		return frame
	case <-time.After(2 * time.Second):
		t.Fatal("accepted request did not reach the transport")
		return nil
	}
}

func windowResponseAwait(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("response accounting did not settle")
		}
		runtime.Gosched()
	}
}

func windowResponseReclaim(out *transmittable) {
	// No writes run during this explicit recovery. Remove the rate-limit delay
	// instead of using elapsed time as synchronization with a previous scan.
	out.reclaimWG.Wait()
	out.reclaimMu.Lock()
	out.nextReclaim = time.Time{}
	out.reclaimMu.Unlock()
	out.reclaimDeadRequests()
	out.reclaimWG.Wait()
}

func TestWindowResponseStoreFailureReleasesLocalCapacity(t *testing.T) {
	for _, failure := range []string{"Delete", "GetDeadline", "GetMissing"} {
		t.Run(failure, func(t *testing.T) {
			conn, peer, frames := windowResponsePeer(t)
			store := &windowResponseFailureStore{DefaultStore: NewDefaultStore(), getEntered: make(chan struct{}, 1)}
			settings := lifecycleSettings()
			settings.MaxWindowSize = 1
			settings.StoreAccessTimeOut = 100 * time.Millisecond
			storeErrors, expected := make(chan error, 8), make(chan Response, 8)
			var closed, unexpected atomic.Int32
			settings.OnReceivingError = func(err error) { storeErrors <- err }
			settings.OnExpectedPduResponse = func(response Response) { expected <- response }
			settings.OnClosePduRequest = func(pdu.PDU) { closed.Add(1) }
			settings.OnUnexpectedPduResponse = func(pdu.PDU) { unexpected.Add(1) }
			session, err := NewSession(lifecycleConnector{connect: func() (*Connection, error) { return conn, nil }}, settings, 0, WithRequestStore(store))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = session.Close() })
			out := session.bound().out
			first := pdu.NewSubmitSM()
			if err := session.Transceiver().Submit(first); err != nil {
				t.Fatal(err)
			}
			frame := windowResponseFrame(t, frames)
			windowResponseAwait(t, func() bool { return out.writtenCount() == 1 })
			reservation, ok := store.DefaultStore.Get(context.Background(), first.GetSequenceNumber())
			if !ok {
				t.Fatal("written request has no reservation")
			}
			out.reclaimMu.Lock()
			out.nextReclaim = time.Now().Add(time.Hour)
			out.reclaimMu.Unlock()
			// Keep cleanup unavailable until local release and uncertainty have
			// been observed. A recovery scan is triggered explicitly below.
			store.failDelete.Store(true)
			switch failure {
			case "GetDeadline":
				store.failGet.Store(true)
			case "GetMissing":
				if _, err := store.DefaultStore.Delete(context.Background(), reservation); err != nil {
					t.Fatal(err)
				}
				store.missingGet.Store(true)
			}
			writeWire(t, peer, frame.GetResponse())
			if failure == "GetDeadline" {
				lifecycleWait(t, store.getEntered)
			}
			if failure == "GetMissing" {
				select {
				case response := <-expected:
					if response.OriginalRequest.ID != reservation.ID || response.OriginalRequest.PDU != first || response.PDU == nil || response.Err != nil || response.GetSequenceNumber() != frame.GetSequenceNumber() {
						t.Fatal("missing store row lost the local response correlation")
					}
				case <-time.After(2 * time.Second):
					t.Fatal("missing store row did not complete through the local ledger")
				}
				if len(storeErrors) != 0 || out.writtenCount() != 0 || len(out.ownerState.permits) != 0 {
					t.Fatal("local completion retained capacity or reported a store error")
				}
				store.missingGet.Store(false)
				store.failDelete.Store(false)
				return
			}
			select {
			case err := <-storeErrors:
				want := errWindowResponseDelete
				if failure == "GetDeadline" {
					want = context.DeadlineExceeded
				}
				if !errors.Is(err, want) {
					t.Fatalf("response error = %v, want %v", err, want)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("response store failure was not reported via OnReceivingError")
			}
			if written, permits := out.writtenCount(), len(out.ownerState.permits); written != 0 || permits != 0 {
				t.Fatalf("answered request retained local capacity: written=%d permits=%d", written, permits)
			}
			if _, abandoned := out.lineage.abandoned.Load(reservation.ID); !abandoned || !out.lineage.orphaned.Load() {
				t.Fatalf("failed response cleanup lost abandoned identity or recovery hint: abandoned=%v hint=%v", abandoned, out.lineage.orphaned.Load())
			}
			if _, exists := store.DefaultStore.Get(context.Background(), first.GetSequenceNumber()); !exists {
				t.Fatal("fake cleanup outage did not retain the backend row")
			}
			store.failGet.Store(false)
			store.missingGet.Store(false)
			store.failDelete.Store(false)
			windowResponseReclaim(out)
			if _, exists := store.DefaultStore.Get(context.Background(), first.GetSequenceNumber()); exists {
				t.Fatal("healthy recovery retained the already-answered backend row")
			}
			if len(expected) != 0 || closed.Load() != 0 || unexpected.Load() != 0 {
				t.Fatalf("failed completion was delivered again: expected=%d close=%d unexpected=%d", len(expected), closed.Load(), unexpected.Load())
			}
			next := pdu.NewSubmitSM()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err := session.bound().SubmitContext(ctx, next); err != nil {
				t.Fatalf("response error stranded the admission permit: %v", err)
			}
			nextFrame := windowResponseFrame(t, frames)
			if nextFrame.GetSequenceNumber() != next.GetSequenceNumber() {
				t.Fatal("replacement request was not written")
			}
			writeWire(t, peer, nextFrame.GetResponse())
			select {
			case response := <-expected:
				if response.OriginalRequest.PDU != next || response.Err != nil {
					t.Fatalf("healthy response after recovery: %+v", response)
				}
			case <-ctx.Done():
				t.Fatal("healthy response after recovery did not complete")
			}
			if size, err := session.GetWindowSize(); err != nil || size != 0 {
				t.Fatalf("recovered window = %d, %v", size, err)
			}
		})
	}
}

type windowResponsePostWireConn struct {
	net.Conn
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (c *windowResponsePostWireConn) Write(bytes []byte) (int, error) {
	n, err := c.Conn.Write(bytes)
	if err == nil {
		c.once.Do(func() { close(c.entered); <-c.release })
	}
	return n, err
}

var errWindowResponseMark = errors.New("written metadata unavailable")

type windowResponseGatedMarkStore struct {
	*windowResponseFailureStore
	markEntered chan struct{}
	markRelease chan struct{}
}

func (s *windowResponseGatedMarkStore) MarkWritten(context.Context, Request, time.Time) (bool, error) {
	close(s.markEntered)
	<-s.markRelease
	return false, errWindowResponseMark
}

func TestWindowResponseStoreFailureBeforeWriteReturnsDoesNotRestoreCapacity(t *testing.T) {
	conn, peer, frames := windowResponsePeer(t)
	gate := &windowResponsePostWireConn{Conn: conn.conn, entered: make(chan struct{}), release: make(chan struct{})}
	conn.conn = gate
	store := &windowResponseGatedMarkStore{
		windowResponseFailureStore: &windowResponseFailureStore{DefaultStore: NewDefaultStore()},
		markEntered:                make(chan struct{}), markRelease: make(chan struct{}),
	}
	settings := lifecycleSettings()
	settings.MaxWindowSize = 1
	storeErrors := make(chan error, 4)
	var expected atomic.Int32
	settings.OnReceivingError = func(err error) { storeErrors <- err }
	settings.OnExpectedPduResponse = func(Response) { expected.Add(1) }
	session, err := NewSession(lifecycleConnector{connect: func() (*Connection, error) { return conn, nil }}, settings, 0, WithRequestStore(store))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	var writeOnce, markOnce sync.Once
	releaseWrite := func() { writeOnce.Do(func() { close(gate.release) }) }
	releaseMark := func() { markOnce.Do(func() { close(store.markRelease) }) }
	t.Cleanup(func() { releaseWrite(); releaseMark() })
	out := session.bound().out
	out.reclaimMu.Lock()
	out.nextReclaim = time.Now().Add(time.Hour)
	out.reclaimMu.Unlock()
	store.failDelete.Store(true)
	request := pdu.NewSubmitSM()
	if err := session.Transceiver().Submit(request); err != nil {
		t.Fatal(err)
	}
	frame := windowResponseFrame(t, frames)
	lifecycleWait(t, gate.entered)
	// The peer has the whole request, but Write has not returned to the writer.
	// A response must settle local capacity before that writer can mark it sent.
	writeWire(t, peer, frame.GetResponse())
	select {
	case err := <-storeErrors:
		if !errors.Is(err, errWindowResponseDelete) {
			t.Fatalf("response cleanup error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("early response cleanup failure was not reported")
	}
	if permits := len(out.ownerState.permits); permits != 0 {
		t.Fatalf("early response retained %d admission permits", permits)
	}
	releaseWrite()
	lifecycleWait(t, store.markEntered)
	if written := out.writtenCount(); written != 0 {
		t.Fatalf("post-response write completion restored local capacity: written=%d", written)
	}
	releaseMark()
	select {
	case err := <-storeErrors:
		if !errors.Is(err, errWindowResponseMark) {
			t.Fatalf("post-response metadata error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("post-response metadata failure was not reported")
	}
	if written, repairs := out.writtenCount(), out.lineage.repairs.Load(); written != 0 || repairs != 0 {
		t.Fatalf("completed response was resurrected: written=%d metadata repairs=%d", written, repairs)
	}
	if expected.Load() != 0 {
		t.Fatal("failed response completion also ran the expected-response callback")
	}
	store.failDelete.Store(false)
	windowResponseReclaim(out)
	if count, err := store.Length(context.Background()); err != nil || count != 0 {
		t.Fatalf("silent response cleanup after write completion: count=%d err=%v", count, err)
	}
}

func TestWindowResponseCannotCompleteReservationWaitingForAdmission(t *testing.T) {
	conn, peer, frames := windowResponsePeer(t)
	store := NewDefaultStore()
	settings := lifecycleSettings()
	settings.MaxWindowSize = 1
	expected, unexpected := make(chan Response, 4), make(chan pdu.PDU, 4)
	settings.OnExpectedPduResponse = func(response Response) { expected <- response }
	settings.OnUnexpectedPduResponse = func(response pdu.PDU) { unexpected <- response }
	session, err := NewSession(lifecycleConnector{connect: func() (*Connection, error) { return conn, nil }}, settings, 0, WithRequestStore(store))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	first, second := pdu.NewSubmitSM(), pdu.NewSubmitSM()
	if err := session.Transceiver().Submit(first); err != nil {
		t.Fatal(err)
	}
	firstFrame := windowResponseFrame(t, frames)
	windowResponseAwait(t, func() bool { return session.bound().out.writtenCount() == 1 })
	ids, submitted := make(chan uint64, 1), make(chan error, 1)
	go func() {
		ids <- callbackGoroutineID()
		submitted <- session.Transceiver().Submit(second)
	}()
	id := <-ids
	windowResponseAwait(t, func() bool { _, exists := store.Get(context.Background(), second.GetSequenceNumber()); return exists })
	lifecycleAwaitWait(t, id, session.bound().out.callbacks.instance())
	reservation, _ := store.Get(context.Background(), second.GetSequenceNumber())
	writeWire(t, peer, second.GetResponse())
	select {
	case response := <-unexpected:
		if response.GetSequenceNumber() != second.GetSequenceNumber() {
			t.Fatal("wrong unexpected response")
		}
	case response := <-expected:
		t.Fatalf("never-transmitted reservation was completed: sequence=%d original=%d", response.GetSequenceNumber(), response.OriginalRequest.GetSequenceNumber())
	case <-time.After(2 * time.Second):
		t.Fatal("response for never-transmitted reservation was not reported")
	}
	if current, exists := store.Get(context.Background(), second.GetSequenceNumber()); !exists || !sameReservation(current, reservation) {
		t.Fatal("unexpected response removed the admission-waiting reservation")
	}
	select {
	case err := <-submitted:
		t.Fatalf("Submit returned before a permit was available: %v", err)
	default:
	}
	writeWire(t, peer, firstFrame.GetResponse())
	select {
	case response := <-expected:
		if response.OriginalRequest.PDU != first {
			t.Fatal("first written request lost its response")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first written request did not complete")
	}
	select {
	case err := <-submitted:
		if err != nil {
			t.Fatalf("second admission: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first response did not release admission")
	}
	secondFrame := windowResponseFrame(t, frames)
	if secondFrame.GetSequenceNumber() != second.GetSequenceNumber() {
		t.Fatal("accepted second request was silently omitted from the wire")
	}
	writeWire(t, peer, secondFrame.GetResponse())
	select {
	case response := <-expected:
		if response.OriginalRequest.PDU != second || response.Err != nil {
			t.Fatalf("actual second response: %+v", response)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("transmitted second request did not complete")
	}
	if len(expected) != 0 || len(unexpected) != 0 {
		t.Fatal("response callbacks ran more than once")
	}
}

func TestWindowRecoverySilentlyReclaimsClosedForeignSessionWithoutExpiry(t *testing.T) {
	store := &windowResponseFailureStore{DefaultStore: NewDefaultStore()}
	firstConn, firstCleanup := newPipePair()
	defer firstCleanup()
	nextConn, nextCleanup := newPipePair()
	defer nextCleanup()
	settings := lifecycleSettings()
	settings.MaxWindowSize = 3
	settings.PduExpireTimeOut = 0
	settings.ExpireCheckTimer = 0
	var firstCallbacks, foreignCallbacks atomic.Int32
	settings.OnClosePduRequest = func(pdu.PDU) { firstCallbacks.Add(1) }
	first, err := NewSession(lifecycleConnector{connect: func() (*Connection, error) { return firstConn, nil }}, settings, 0, WithRequestStore(store))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })
	orphans := make(map[pdu.PDU]bool)
	for range 3 {
		request := pdu.NewSubmitSM()
		orphans[request] = true
		if err := first.Transceiver().Submit(request); err != nil {
			t.Fatal(err)
		}
	}
	windowResponseAwait(t, func() bool { count, _ := store.WrittenLength(context.Background(), 0); return count == 3 })
	store.failDelete.Store(true)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	store.failDelete.Store(false)
	settings.OnClosePduRequest = func(request pdu.PDU) {
		if orphans[request] {
			foreignCallbacks.Add(1)
		}
	}
	settings.OnExpiredPduRequest = func(request pdu.PDU) bool {
		if orphans[request] {
			foreignCallbacks.Add(1)
		}
		return false
	}
	next, err := NewSession(lifecycleConnector{connect: func() (*Connection, error) { return nextConn, nil }}, settings, 0, WithRequestStore(store))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = next.Close() })
	out := next.bound().out
	if out.lineage.orphaned.Load() {
		t.Fatal("fresh Session unexpectedly inherited the foreign recovery hint")
	}
	if _, err := out.write(pdu.NewSubmitSM()); !errors.Is(err, ErrWindowsFull) {
		t.Fatalf("foreign rows did not trigger full-window recovery: %v", err)
	}
	out.reclaimWG.Wait()
	for _, request := range store.List(context.Background()) {
		if request.Lineage == first.lineage.id {
			t.Fatalf("expiry-disabled recovery retained foreign reservation sequence=%d", request.GetSequenceNumber())
		}
	}
	if firstCallbacks.Load() != 3 || foreignCallbacks.Load() != 0 {
		t.Fatalf("foreign recovery invoked request callbacks: original=%d foreign=%d", firstCallbacks.Load(), foreignCallbacks.Load())
	}
	if _, err := out.write(pdu.NewSubmitSM()); err != nil {
		t.Fatalf("silent foreign recovery did not release shared capacity: %v", err)
	}
}
