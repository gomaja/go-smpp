package gosmpp

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gomaja/go-smpp/pdu"
)

var ErrWindowNotConfigured = errors.New("window settings not configured")

type transceivable struct {
	callbacks    callbackScope
	settings     Settings
	ctx          context.Context
	cancel       context.CancelFunc
	wg           sync.WaitGroup
	conn         *Connection
	in           *receivable
	out          *transmittable
	started      chan struct{}
	startOnce    sync.Once
	aliveState   int32
	requestStore RequestStore
	stopOnce     sync.Once
	done         chan struct{}
	closeErr     error
}
type TransceivableOption func(session *Session)

func newTransceivable(conn *Connection, settings Settings, store RequestStore) *transceivable {
	t := &transceivable{settings: settings, conn: conn, started: make(chan struct{}), done: make(chan struct{}), requestStore: store}
	t.ctx, t.cancel = context.WithCancel(context.Background())
	outbound := settings
	outbound.OnClosed = nil
	t.out = newTransmittable(conn, outbound, store)
	t.out.callbacks.parent = &t.callbacks
	inbound := settings
	inbound.OnClosed = nil
	inbound.response = func(p pdu.PDU) {
		if err := t.Submit(p); err != nil {
			t.out.submitError(p, err)
		}
	}
	t.in = newReceivable(conn, inbound, store)
	t.in.callbacks.parent = &t.callbacks
	t.in.owner = t.out.owner
	t.in.discardedRequest = func(request Request) bool { return t.out.lineage.discarded(request) }
	t.in.requestClose = t.stop
	t.out.requestClose = t.stop
	return t
}

func (t *transceivable) start() {
	t.startOnce.Do(func() {
		defer close(t.started)
		if atomic.LoadInt32(&t.aliveState) != Alive {
			return
		}
		if t.settings.WindowedRequestTracking != nil && t.settings.PduExpireTimeOut > 0 && t.settings.ExpireCheckTimer > 0 {
			t.wg.Add(1)
			go func() {
				defer t.wg.Done()
				defer registerCallbackWorker(&t.callbacks)()
				t.windowCleanup()
			}()
		}
		t.out.start()
		t.in.start()
	})
}
func (t *transceivable) awaitStarted()    { t.start(); <-t.started }
func (t *transceivable) SystemID() string { return t.conn.systemID }

// Close interrupts I/O and stops accepting submissions. Outside a callback it
// waits for all workers and callbacks, including an already-started shutdown.
// Inside this instance's callback, or when joining would complete a callback wait
// cycle between instances, it only initiates shutdown. The calling callback must
// return before cleanup and remaining callbacks can finish. One-way calls from
// another instance join. Application goroutines are external; their waits are not
// tracked. A later external Close joins completion.
// For a Session or Server connection, cycle detection includes every worker of
// that parent instance and may conservatively escape a connection-only join.
// An external Close joins every pending close callback; its store cleanup is
// time-bounded and unfinished rows retain their identities for silent recovery.
func (t *transceivable) Close() error           { return t.closing(ExplicitClosing) }
func (t *transceivable) Submit(p pdu.PDU) error { return t.out.Submit(p) }
func (t *transceivable) SubmitContext(ctx context.Context, p pdu.PDU) error {
	return t.out.SubmitContext(ctx, p)
}
func (t *transceivable) GetWindowSize() (int, error) {
	if t.settings.WindowedRequestTracking == nil {
		return 0, ErrWindowNotConfigured
	}
	ctx, cancel := context.WithTimeout(context.Background(), t.settings.StoreAccessTimeOut)
	defer cancel()
	count, err := t.requestStore.WrittenLength(ctx, t.out.owner)
	if err != nil {
		return 0, err
	}
	return count + t.out.writtenCount(), nil
}

func (t *transceivable) windowCleanup() {
	if t.settings.PduExpireTimeOut <= 0 {
		return
	}
	ticker := time.NewTicker(t.settings.ExpireCheckTimer)
	defer ticker.Stop()
	for {
		if t.ctx.Err() != nil {
			return
		}
		select {
		case <-t.ctx.Done():
			return
		case <-ticker.C:
			t.expireRequests()
		}
	}
}

func (t *transceivable) expireRequests() {
	if t.ctx.Err() != nil {
		return
	}
	ctx, cancel := context.WithTimeout(t.ctx, t.settings.StoreAccessTimeOut)
	requests := t.requestStore.List(ctx)
	listErr := ctx.Err()
	cancel()
	if t.ctx.Err() != nil {
		return
	}
	if listErr != nil {
		t.out.lineage.orphaned.Store(true)
		t.in.receivingError(listErr)
	}
	for _, request := range requests {
		if t.ctx.Err() != nil {
			return
		}
		abandoned := t.out.lineage.discarded(request)
		owned := t.out.owns(request)
		deadOwner := t.out.ownsDead(request)
		foreignClosed := t.out.foreignClosed(request)
		if !owned && !deadOwner && !foreignClosed {
			continue
		}
		// Pending writes are not response-timeout candidates. A dead generation has
		// finished writing and cleanup, so an orphan can be completed even if its
		// written timestamp could not be persisted.

		if request.WrittenAt.IsZero() {
			request.WrittenAt = requestWrittenTime(request)
		}
		if request.WrittenAt.IsZero() && !deadOwner && !foreignClosed && !abandoned {
			continue
		}

		if !foreignClosed && !abandoned && !request.WrittenAt.IsZero() && time.Since(request.WrittenAt) <= t.settings.PduExpireTimeOut {
			continue
		}
		ctx, cancel := context.WithTimeout(t.ctx, t.settings.StoreAccessTimeOut)
		removed, err := t.out.deleteRequestContext(ctx, request)
		cancel()
		if err != nil {
			if t.ctx.Err() != nil {
				return
			}
			t.in.receivingError(err)
			continue
		}
		if !removed || foreignClosed {
			continue
		}
		// The local claim has now won. Abandonment before that claim suppresses
		// completion; abandonment after it cannot find the local reservation.
		abandoned = t.out.lineage.discarded(request)
		if !abandoned && t.settings.OnExpiredPduRequest != nil {
			var closing bool
			invokeCallback(&t.callbacks, func() { closing = t.settings.OnExpiredPduRequest(request.PDU) })
			if closing {
				t.stop(ConnectionIssue)
				return
			}
		}
	}
}

func (t *transceivable) stop(state State) {
	t.stopOnce.Do(func() {
		atomic.StoreInt32(&t.aliveState, Closed)
		t.cancel()
		// Mark both directions stopped before interrupting the socket, so an
		// intentional shutdown cannot be mistaken for a fresh receive failure.
		t.out.stop(StoppingProcessOnly)
		t.in.stop(StoppingProcessOnly)
		if state == ExplicitClosing {
			t.conn.tryUnbind()
		}
		// Closing the socket must precede every join, including a blocked writer.
		t.closeErr = t.conn.Close()
		go func() {
			defer close(t.done)
			defer func() {
				if !t.out.lineage.managed {
					t.out.lineage.markLineageClosed()
				}
			}()
			defer registerCallbackWorker(&t.callbacks)()
			t.awaitStarted()
			<-t.out.done
			<-t.in.done
			t.wg.Wait()
			if t.settings.OnClosed != nil {
				invokeCallback(&t.callbacks, func() { t.settings.OnClosed(state) })
			}
		}()
	})
}
func (t *transceivable) closing(state State) error {
	t.stop(state)
	if !joinCallbackScope(&t.callbacks, t.done) {
		return nil
	}
	return t.closeErr
}
