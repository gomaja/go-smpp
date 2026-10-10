package gosmpp

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gomaja/go-smpp/pdu"
)

var (
	ErrWindowSizeEqualZero                   = errors.New("request window size cannot be 0")
	ErrExpireCheckTimerNotSet                = errors.New("ExpireCheckTimer cannot be 0 if PduExpireTimeOut is set")
	ErrStoreAccessTimeOutEqualZero           = errors.New("StoreAccessTimeOut window size cannot be 0")
	ErrWindowSizeNotAvailableOnReceiverBinds = errors.New("window size not available on receiver binds")
)

// Session represents session for TX, RX, TRX.
type Session struct {
	callbacks callbackScope
	c         Connector

	originalOnClosed func(State)
	settings         Settings

	rebindingInterval time.Duration

	trx atomic.Value // transceivable

	state        int32
	rebinding    int32
	requestStore RequestStore
	lineage      *requestLineage
	mu           sync.Mutex
	ctx          context.Context
	cancel       context.CancelFunc
	rebindWG     sync.WaitGroup
	rebindDone   chan struct{}
	closeOnce    sync.Once
	closeDone    chan struct{}
	closeErr     error
}

type SessionOption func(session *Session)

// NewSession creates new session for TX, RX, TRX.
//
// Session will `non-stop`, automatically rebind (create new and authenticate connection with SMSC) when
// unexpected error happened.
//
// `rebindingInterval` indicates duration that Session has to wait before rebinding again.
//
// Setting `rebindingInterval <= 0` will disable `auto-rebind` functionality.
func NewSession(c Connector, settings Settings, rebindingInterval time.Duration, opts ...SessionOption) (session *Session, err error) {
	// Loop through each option

	if settings.ReadTimeout <= 0 || settings.ReadTimeout <= settings.EnquireLink {
		return nil, fmt.Errorf("invalid settings: ReadTimeout must greater than max(0, EnquireLink)")
	}
	var requestStore RequestStore = nil
	if settings.WindowedRequestTracking != nil {
		requestStore = NewDefaultStore()
		if settings.MaxWindowSize == 0 {
			return nil, ErrWindowSizeEqualZero
		}
		if settings.StoreAccessTimeOut == 0 {
			return nil, ErrStoreAccessTimeOutEqualZero
		}
		if settings.PduExpireTimeOut > 0 && settings.ExpireCheckTimer == 0 {
			return nil, ErrExpireCheckTimerNotSet
		}
	}

	conn, err := c.Connect()
	if err == nil {
		session = &Session{
			c:                 c,
			rebindingInterval: rebindingInterval,
			originalOnClosed:  settings.OnClosed,
			requestStore:      requestStore,
		}

		for _, opt := range opts {
			opt(session)
		}

		session.ctx, session.cancel = context.WithCancel(context.Background())
		session.closeDone = make(chan struct{})
		session.settings = settings
		session.settings.OnClosed = func(state State) {
			if session.originalOnClosed != nil {
				session.originalOnClosed(state)
			}
			if state != ExplicitClosing && rebindingInterval > 0 {
				session.rebind()
			}
		}
		// Publish before starting workers so immediate failure sees this generation.
		trans := newTransceivable(conn, session.settings, session.requestStore)
		trans.callbacks.parent = &session.callbacks
		session.lineage = trans.out.lineage
		session.lineage.managed = true
		session.trx.Store(trans)
		trans.start()
	}
	return
}

func WithRequestStore(store RequestStore) SessionOption {
	return func(s *Session) {
		s.requestStore = store
	}
}

func (s *Session) bound() *transceivable {
	r, _ := s.trx.Load().(*transceivable)
	return r
}

// Transmitter returns bound Transmitter.
func (s *Session) Transmitter() Transmitter {
	return s.bound()
}

// Receiver returns bound Receiver.
func (s *Session) Receiver() Receiver {
	return s.bound()
}

// Transceiver returns bound Transceiver.
func (s *Session) Transceiver() Transceiver {
	return s.bound()
}

// GetWindowSize reports written, unanswered application requests. Pending
// reservations and enquire_link do not count toward the application window.
func (s *Session) GetWindowSize() (int, error) {
	if s.c.GetBindType() == pdu.Transmitter || s.c.GetBindType() == pdu.Transceiver {
		size, err := s.bound().GetWindowSize()
		if err != nil {
			return 0, err
		}
		return size, nil
	}
	return 0, ErrWindowSizeNotAvailableOnReceiverBinds
}

// Close stops the session, interrupts bind I/O and joins rebinding and callbacks.
// Inside this session's callback, or when joining would create a cycle between
// instances' callbacks, it initiates shutdown and returns. The calling callback
// must return to let cleanup and remaining callbacks finish. One-way calls from
// another instance still join. Application goroutines are external and their
// waits are not tracked; an external Close always waits for completion.
// Every still-owned transmitted request reaches OnClosePduRequest before that
// external call returns, even if bounded store cleanup must continue later.
// Cycle detection covers the whole Session, including its rebind worker and
// connection generations, even when only Transceiver().Close() is requested.
// A custom Connector lacking ConnectContext must return from
// Connect before that wait can finish.
func (s *Session) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		if s.closeDone == nil {
			s.closeDone = make(chan struct{})
		}
		atomic.StoreInt32(&s.state, Closed)
		if s.cancel != nil {
			s.cancel()
		}
		bound := s.bound()
		s.mu.Unlock()
		// Initiate immediately even if rebinding is inside a slow custom Connect.
		if bound != nil {
			bound.stop(ExplicitClosing)
		}
		go func() {
			defer close(s.closeDone)
			defer registerCallbackWorker(&s.callbacks)()
			if bound != nil {
				// Structural aggregation joins this child rather than invoking
				// its callback-facing Close, which shares our worker membership.
				<-bound.done
				s.closeErr = bound.closeErr
			}
			s.rebindWG.Wait()
			if s.lineage != nil {
				s.lineage.markLineageClosed()
			}
		}()
	})
	if !joinCallbackScope(&s.callbacks, s.closeDone) {
		return nil
	}
	return s.closeErr
}

func (s *Session) close() error {
	if bound := s.bound(); bound != nil {
		return bound.Close()
	}
	return nil
}

type contextConnector interface {
	ConnectContext(context.Context) (*Connection, error)
}

func (s *Session) rebind() {
	s.mu.Lock()
	if atomic.LoadInt32(&s.state) != Alive {
		s.mu.Unlock()
		return
	}
	if atomic.LoadInt32(&s.rebinding) != 0 {
		done := s.rebindDone
		s.mu.Unlock()
		joinCallbackScope(&s.callbacks, done)
		return
	}
	atomic.StoreInt32(&s.rebinding, 1)
	done := make(chan struct{})
	s.rebindDone = done
	s.rebindWG.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.rebindWG.Done()
		defer close(done)
		defer registerCallbackWorker(&s.callbacks)()
		defer func() {
			s.mu.Lock()
			if s.rebindDone == done {
				atomic.StoreInt32(&s.rebinding, 0)
			}
			s.mu.Unlock()
		}()
		if old := s.bound(); old != nil {
			old.stop(ExplicitClosing)
			<-old.done
		}
		for s.ctx.Err() == nil {
			var conn *Connection
			var err error
			if connector, ok := s.c.(contextConnector); ok {
				conn, err = connector.ConnectContext(s.ctx)
			} else {
				conn, err = s.c.Connect()
			}
			s.mu.Lock()
			if atomic.LoadInt32(&s.state) != Alive {
				s.mu.Unlock()
				if conn != nil {
					_ = conn.Close()
				}
				return
			}
			if err == nil {
				trans := newTransceivable(conn, s.settings, s.requestStore)
				trans.callbacks.parent = &s.callbacks
				trans.out.lineage = s.lineage
				s.trx.Store(trans)
				trans.start()
				s.mu.Unlock()
				if s.settings.OnRebind != nil {
					invokeCallback(&s.callbacks, s.settings.OnRebind)
				}
				s.mu.Lock()
				if atomic.LoadInt32(&s.state) == Alive && !trans.isAlive() {
					s.mu.Unlock()
					trans.stop(ExplicitClosing)
					<-trans.done
					continue
				}
				// Reset under the publication lock: a later failure can start its
				// own attempt without this worker clearing that attempt's flag.
				atomic.StoreInt32(&s.rebinding, 0)
				s.mu.Unlock()
				return
			}
			s.mu.Unlock()
			if s.settings.OnRebindingError != nil {
				invokeCallback(&s.callbacks, func() { s.settings.OnRebindingError(err) })
			}
			timer := time.NewTimer(s.rebindingInterval)
			select {
			case <-s.ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()
	joinCallbackScope(&s.callbacks, done)
}
