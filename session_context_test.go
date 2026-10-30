package gosmpp

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/gomaja/go-smpp/pdu"
)

// The socket deadline fires before the context's cancellation is published.
type sessionDelayedDeadlineContext struct {
	context.Context
	deadline time.Time
}

func (c sessionDelayedDeadlineContext) Deadline() (time.Time, bool) { return c.deadline, true }

func TestConnectContextDeadlineBeforeContextTimer(t *testing.T) {
	client, peer := net.Pipe()
	defer func() { _ = client.Close(); _ = peer.Close() }()
	ctx := sessionDelayedDeadlineContext{Context: context.Background(), deadline: time.Now().Add(20 * time.Millisecond)}
	_, err := connectContext(ctx, DialerFunc(func(context.Context, string) (net.Conn, error) {
		return client, nil
	}), "synthetic", pdu.NewBindRequest(pdu.Transceiver), time.Second)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("bind error = %v, want context deadline exceeded", err)
	}
}

func TestConnectContextBindTimeoutBeforeContextDeadline(t *testing.T) {
	client, peer := net.Pipe()
	defer func() { _ = client.Close(); _ = peer.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	_, err := connectContext(ctx, DialerFunc(func(context.Context, string) (net.Conn, error) {
		return client, nil
	}), "synthetic", pdu.NewBindRequest(pdu.Transceiver), 20*time.Millisecond)
	if !errors.Is(err, os.ErrDeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("bind error = %v, want transport deadline exceeded", err)
	}
}

func TestNewSessionContextCancelBind(t *testing.T) {
	auth, bound, peerClosed, _, stop := sessionSilentBindPeer(t)
	defer stop()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	results, _ := sessionContextStart(t, ctx, TXConnector(NonTLSDialer, auth), Settings{ReadTimeout: time.Hour}, 0)
	if err := sessionContextReceive(t, bound); err != nil {
		t.Fatal(err)
	}
	cancel()
	result := sessionContextReceive(t, results)
	if result.session != nil || !errors.Is(result.err, context.Canceled) {
		t.Fatalf("NewSessionContext = (%v, %v), want nil session and cancellation", result.session, result.err)
	}
	if err := sessionContextReceive(t, peerClosed); !errors.Is(err, io.EOF) {
		t.Fatalf("peer read = %v, want EOF", err)
	}
}

func TestNewSessionContextCancelDial(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan context.Context, 1)
	finished, abort := make(chan struct{}), make(chan struct{})
	defer close(abort)
	dialer := DialerFunc(func(dialCtx context.Context, _ string) (net.Conn, error) {
		defer close(finished)
		entered <- dialCtx
		select {
		case <-dialCtx.Done():
			return nil, dialCtx.Err()
		case <-abort:
			return nil, errors.New("dial aborted by test cleanup")
		}
	})
	results, _ := sessionContextStart(t, ctx, TRXConnector(dialer, Auth{}), Settings{ReadTimeout: time.Hour}, 0)
	if got := sessionContextReceive(t, entered); got != ctx {
		t.Fatal("dialer did not receive the caller's context")
	}
	cancel()
	result := sessionContextReceive(t, results)
	if result.session != nil || !errors.Is(result.err, context.Canceled) {
		t.Fatalf("NewSessionContext = (%v, %v), want nil session and cancellation", result.session, result.err)
	}
	lifecycleWait(t, finished)
}

func TestNewSessionContextAlreadyDone(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		name := "canceled"
		if deadline {
			name = "deadline"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			if deadline {
				ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			}
			defer cancel()
			if !deadline {
				cancel()
			}
			for _, kind := range []string{"TX", "RX", "TRX", "custom"} {
				t.Run(kind, func(t *testing.T) {
					called := false
					dialer := DialerFunc(func(context.Context, string) (net.Conn, error) {
						called = true
						return nil, errors.New("unexpected dial")
					})
					var c Connector
					switch kind {
					case "TX":
						c = TXConnector(dialer, Auth{})
					case "RX":
						c = RXConnector(dialer, Auth{})
					case "TRX":
						c = TRXConnector(dialer, Auth{})
					default:
						c = lifecycleConnector{connect: func() (*Connection, error) {
							called = true
							return nil, errors.New("unexpected Connect")
						}}
					}
					s, err := NewSessionContext(ctx, c, Settings{ReadTimeout: time.Hour}, 0)
					if s != nil || err != ctx.Err() || called {
						t.Fatalf("NewSessionContext = (%v, %v), called = %v; want nil, %v, false", s, err, called, ctx.Err())
					}
				})
			}
		})
	}
}

func TestNewSessionContextDeadlineBind(t *testing.T) {
	auth, bound, peerClosed, stopAccepting, stop := sessionSilentBindPeer(t)
	defer stop()
	// Expiry may occur before dialing or writing the bind.
	deadlineCtx, expire := context.WithDeadline(context.Background(), time.Now().Add(50*time.Millisecond))
	defer expire()
	results, _ := sessionContextStart(t, deadlineCtx, RXConnector(NonTLSDialer, auth), Settings{ReadTimeout: time.Hour}, 0)
	result := sessionContextReceive(t, results)
	if result.session != nil || !errors.Is(result.err, context.DeadlineExceeded) {
		t.Fatalf("NewSessionContext = (%v, %v), want nil session and deadline exceeded", result.session, result.err)
	}
	// Unblock Accept if no connection was made, leaving accepted peers open to observe EOF.
	stopAccepting()
	if err := sessionContextReceive(t, bound); err == nil {
		if err := sessionContextReceive(t, peerClosed); !errors.Is(err, io.EOF) {
			t.Fatalf("peer read = %v, want EOF", err)
		}
	}
}

func TestNewSessionContextSuccessThenCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), sessionContextKey{}, "initial only"))
	defer cancel()
	replies := make(chan pdu.PDU, 2)
	rebound := make(chan struct{}, 1)
	closed, releaseClose := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseClose) }) }
	defer release()
	s, err := NewSessionContext(ctx, TRXConnector(NonTLSDialer, nextAuth()), Settings{
		ReadTimeout: time.Hour, WriteTimeout: time.Second,
		OnPDU:    func(p pdu.PDU, _ bool) { replies <- p },
		OnRebind: func() { rebound <- struct{}{} },
		OnClosed: func(state State) {
			if state == ExplicitClosing {
				close(closed)
				<-releaseClose
			}
		},
	}, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { release(); _ = s.Close() }()
	cancel()
	if !s.IsAlive() {
		t.Fatal("canceling the initial context stopped the session")
	}
	if s.ctx.Err() != nil || s.ctx.Value(sessionContextKey{}) != nil {
		t.Fatal("session retained the initial context's cancellation or values")
	}
	roundTrip := func() {
		t.Helper()
		request := pdu.NewEnquireLink()
		if err := s.Transceiver().Submit(request); err != nil {
			t.Fatal(err)
		}
		reply := sessionContextReceive(t, replies)
		if _, ok := reply.(*pdu.EnquireLinkResp); !ok || reply.GetSequenceNumber() != request.GetSequenceNumber() {
			t.Fatalf("round trip reply = %v, want matching enquire_link_resp", reply)
		}
	}
	roundTrip()
	old := s.bound()
	if err := old.conn.Close(); err != nil {
		t.Fatal(err)
	}
	sessionContextReceive(t, rebound)
	if s.bound() == old || !s.IsAlive() {
		t.Fatal("session did not rebind after initial context cancellation")
	}
	roundTrip()
	done := make(chan struct{})
	go func() { _ = s.Close(); close(done) }()
	lifecycleWait(t, closed)
	lifecycleAwaitJoin(t, done, "(*Session).Close(")
	release()
	lifecycleWait(t, done)
	if s.ctx.Err() != context.Canceled {
		t.Fatal("Close did not cancel the session's own context")
	}
	lifecycleWait(t, s.bound().done)
	s.rebindWG.Wait()
}

type sessionContextKey struct{}

func TestNewSessionContextCustomConnectCancellation(t *testing.T) {
	for _, tc := range []struct {
		name           string
		withConnection bool
		withError      bool
	}{
		{name: "connection", withConnection: true},
		{name: "connection and error", withConnection: true, withError: true},
		{name: "error", withError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, peer := net.Pipe()
			if err := peer.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = client.Close(); _ = peer.Close() }()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			c := lifecycleConnector{connect: func() (*Connection, error) {
				close(entered)
				<-release
				var conn *Connection
				if tc.withConnection {
					conn = NewConnection(client)
				}
				if tc.withError {
					return conn, errors.New("custom Connect failed")
				}
				return conn, nil
			}}
			results, returned := sessionContextStart(t, ctx, c, Settings{ReadTimeout: time.Hour}, 0)
			lifecycleWait(t, entered)
			cancel()
			lifecycleAwaitJoin(t, returned, "NewSessionContext(")
			unblock()
			result := sessionContextReceive(t, results)
			if result.session != nil || !errors.Is(result.err, context.Canceled) {
				t.Fatalf("NewSessionContext = (%v, %v), want nil session and cancellation", result.session, result.err)
			}
			if tc.withConnection {
				var b [1]byte
				if _, err := peer.Read(b[:]); !errors.Is(err, io.EOF) {
					t.Fatalf("peer read = %v, want EOF", err)
				}
			}
		})
	}
}

func TestNewSessionContextValidationFirst(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tc := range []struct {
		name     string
		settings Settings
		want     string
	}{
		{name: "read timeout", want: "invalid settings: ReadTimeout must greater than max(0, EnquireLink)"},
		{name: "window", settings: Settings{ReadTimeout: time.Hour, WindowedRequestTracking: &WindowedRequestTracking{}}, want: ErrWindowSizeEqualZero.Error()},
		{name: "store timeout", settings: Settings{ReadTimeout: time.Hour, WindowedRequestTracking: &WindowedRequestTracking{MaxWindowSize: 1}}, want: ErrStoreAccessTimeOutEqualZero.Error()},
		{name: "expiry", settings: Settings{ReadTimeout: time.Hour, WindowedRequestTracking: &WindowedRequestTracking{MaxWindowSize: 1, StoreAccessTimeOut: time.Second, PduExpireTimeOut: time.Second}}, want: ErrExpireCheckTimerNotSet.Error()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := NewSessionContext(canceled, nil, tc.settings, 0)
			if s != nil || err == nil || err.Error() != tc.want {
				t.Fatalf("NewSessionContext = (%v, %v), want nil session and %q", s, err, tc.want)
			}
		})
	}
}

func TestNewSessionContextNilContext(t *testing.T) {
	// The panic comes before settings validation, so invalid settings cannot hide it.
	for _, settings := range []Settings{{}, {ReadTimeout: time.Hour}} {
		func() {
			defer func() {
				if got := recover(); got != "gosmpp: nil context" {
					t.Errorf("settings %+v: panic = %v, want clear nil context panic", settings, got)
				}
			}()
			var ctx context.Context
			_, _ = NewSessionContext(ctx, nil, settings, 0)
		}()
	}
}

type sessionContextResult struct {
	session *Session
	err     error
}

func sessionContextStart(t *testing.T, ctx context.Context, c Connector, settings Settings, interval time.Duration) (<-chan sessionContextResult, <-chan struct{}) {
	t.Helper()
	results, returned := make(chan sessionContextResult, 1), make(chan struct{})
	var result sessionContextResult
	go func() {
		defer close(returned)
		result.session, result.err = NewSessionContext(ctx, c, settings, interval)
		results <- result
	}()
	t.Cleanup(func() {
		lifecycleWait(t, returned)
		if result.session != nil {
			_ = result.session.Close()
		}
	})
	return results, returned
}

func sessionContextReceive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(time.Second):
		t.Fatal("initial connection operation did not finish within one second")
		var zero T
		return zero
	}
}

func sessionSilentBindPeer(t *testing.T) (Auth, <-chan error, <-chan error, func(), func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	accepted := make(chan net.Conn, 1)
	bound, peerClosed := make(chan error, 1), make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		peer, err := ln.Accept()
		accepted <- peer
		if err != nil {
			bound <- err
			peerClosed <- err
			return
		}
		defer func() { _ = peer.Close() }()
		_, err = pdu.Parse(peer)
		bound <- err
		if err == nil {
			var b [1]byte
			_, err = peer.Read(b[:])
		}
		peerClosed <- err
	}()
	stopAccepting := func() { _ = ln.Close() }
	stop := func() {
		stopAccepting()
		if peer := <-accepted; peer != nil {
			_ = peer.Close()
		}
		lifecycleWait(t, done)
	}
	return Auth{SMSC: ln.Addr().String(), SystemID: "synthetic", Password: "synthetic"}, bound, peerClosed, stopAccepting, stop
}

type sessionDispatchConnector struct {
	connectErr error
	contextErr error
}

func (c sessionDispatchConnector) Connect() (*Connection, error) { return nil, c.connectErr }
func (c sessionDispatchConnector) ConnectContext(context.Context) (*Connection, error) {
	return nil, c.contextErr
}
func (sessionDispatchConnector) GetBindType() pdu.BindingType { return pdu.Transceiver }

func TestNewSessionInitialConnectDispatch(t *testing.T) {
	connectErr := errors.New("Connect selected")
	contextErr := errors.New("ConnectContext selected")
	c := sessionDispatchConnector{connectErr: connectErr, contextErr: contextErr}
	settings := Settings{ReadTimeout: time.Hour}
	for _, tc := range []struct {
		name   string
		create func() (*Session, error)
		want   error
	}{
		{name: "NewSession", create: func() (*Session, error) { return NewSession(c, settings, 0) }, want: connectErr},
		{name: "NewSessionContext", create: func() (*Session, error) { return NewSessionContext(context.Background(), c, settings, 0) }, want: contextErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := tc.create()
			if s != nil || err != tc.want {
				t.Fatalf("constructor = (%v, %v), want nil session and %v", s, err, tc.want)
			}
		})
	}
}

func TestNewSessionNilConnectorInvalidSettings(t *testing.T) {
	s, err := NewSession(nil, Settings{}, 0)
	if s != nil || err == nil || err.Error() != "invalid settings: ReadTimeout must greater than max(0, EnquireLink)" {
		t.Fatalf("NewSession = (%v, %v), want nil session and settings error", s, err)
	}
}
