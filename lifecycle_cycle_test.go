package gosmpp

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gomaja/go-smpp/pdu"
)

func lifecycleAssertGoroutinesReturn(t *testing.T, baseline int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > baseline && time.Now().Before(deadline) {
		runtime.Gosched()
	}
	if got := runtime.NumGoroutine(); got > baseline {
		buffer := make([]byte, 1<<20)
		n := runtime.Stack(buffer, true)
		t.Fatalf("shutdown left goroutines: baseline=%d current=%d\n%s", baseline, got, buffer[:n])
	}
}

func testSessionCloseCycle(t *testing.T, count int) {
	t.Helper()
	baseline := runtime.NumGoroutine()
	sessions := make([]atomic.Pointer[Session], count)
	callbacks := make(chan int, count)
	var cleanups []func()
	for i := range count {
		conn, cleanup := newPipePair()
		cleanups = append(cleanups, cleanup)
		s, err := NewSession(lifecycleConnector{connect: func() (*Connection, error) { return conn, nil }}, Settings{
			ReadTimeout: time.Hour,
			OnClosed: func(State) {
				_ = sessions[(i+1)%count].Load().Close()
				callbacks <- i
			},
		}, 0)
		if err != nil {
			t.Fatal(err)
		}
		sessions[i].Store(s)
	}
	defer func() {
		for _, cleanup := range cleanups {
			cleanup()
		}
	}()
	closed := make(chan struct{})
	go func() {
		for i := range count {
			_ = sessions[i].Load().Close()
		}
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatalf("%d-session Close cycle did not return", count)
	}
	if len(callbacks) != count {
		t.Fatalf("external Close returned before callbacks: got %d, want %d", len(callbacks), count)
	}
	for _, cleanup := range cleanups {
		cleanup()
	}
	lifecycleAssertGoroutinesReturn(t, baseline)
}

func TestSessionLifecycleMutualCloseJoinsCallbacks(t *testing.T) {
	testSessionCloseCycle(t, 2)
}

func TestSessionLifecycleThreeInstanceCloseCycle(t *testing.T) {
	testSessionCloseCycle(t, 3)
}

func TestServerLifecycleGatewayMutualClose(t *testing.T) {
	for _, accountOnly := range []bool{false, true} {
		t.Run(map[bool]string{false: "server", true: "account"}[accountOnly], func(t *testing.T) {
			baseline := runtime.NumGoroutine()
			var upstream atomic.Pointer[Session]
			entered, release, upstreamClosing := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			defer unblock()
			srv := NewServer(ServerSettings{Address: "127.0.0.1:0"})
			if err := srv.StartSystemID(SystemIDConfig{SystemID: "sys", Password: "pw",
				OnPDU: func(_ ServerSessionInfo, request pdu.PDU) (pdu.PDU, bool) {
					close(entered)
					<-release
					_ = upstream.Load().Close()
					return request.GetResponse(), false
				},
			}); err != nil {
				t.Fatal(err)
			}
			if err := srv.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = srv.listener.Close() }()
			peer, _ := bind(t, srv.Addr().String(), "sys", "pw", pdu.Transceiver)
			defer func() { _ = peer.Close() }()
			writeWire(t, peer, pdu.NewSubmitSM())
			lifecycleWait(t, entered)
			conn, cleanup := newPipePair()
			defer cleanup()
			s, err := NewSession(lifecycleConnector{connect: func() (*Connection, error) { return conn, nil }}, Settings{
				ReadTimeout: time.Hour, OnClosed: func(State) {
					close(upstreamClosing)
					if accountOnly {
						srv.StopSystemID("sys")
					} else {
						srv.Stop()
					}
				},
			}, 0)
			if err != nil {
				t.Fatal(err)
			}
			upstream.Store(s)
			closed := make(chan struct{})
			go func() { _ = s.Close(); srv.Stop(); close(closed) }()
			lifecycleWait(t, upstreamClosing)
			// Transport closure proves the upstream callback initiated shutdown;
			// the gated server handler still owns its worker until we release it.
			assertConnectionClosed(t, peer)
			unblock()
			select {
			case <-closed:
			case <-time.After(2 * time.Second):
				t.Fatal("Session Close and server shutdown formed a wait cycle")
			}
			cleanup()
			lifecycleAssertGoroutinesReturn(t, baseline)
		})
	}
}

func TestLifecycleApplicationGoroutineStillJoins(t *testing.T) {
	conn, cleanup := newPipePair()
	defer cleanup()
	entered, release, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	var s *Session
	var err error
	s, err = NewSession(lifecycleConnector{connect: func() (*Connection, error) { return conn, nil }}, Settings{
		ReadTimeout: time.Hour,
		OnClosed: func(State) {
			close(entered)
			go func() { _ = s.Close(); close(returned) }()
			<-release
		},
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = s.Close() }()
	lifecycleWait(t, entered)
	lifecycleAwaitJoin(t, returned, "(*Session).Close(")
	unblock()
	lifecycleWait(t, returned)
	_ = s.Close()
}
