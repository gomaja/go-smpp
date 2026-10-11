package gosmpp

import (
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gomaja/go-smpp/pdu"
)

func TestLifecycleStandaloneClosedCallbackCanClose(t *testing.T) {
	for _, direction := range []string{"receiver", "transmitter", "transceiver"} {
		t.Run(direction, func(t *testing.T) {
			baseline := runtime.NumGoroutine()
			conn, cleanup := newPipePair()
			var closeTarget func() error
			callbackDone := make(chan error, 1)
			settings := Settings{ReadTimeout: time.Hour, OnClosed: func(State) {
				callbackDone <- closeTarget()
			}}
			switch direction {
			case "receiver":
				v := newReceivable(conn, settings, nil)
				closeTarget = func() error { return v.close(ExplicitClosing) }
				v.start()
			case "transmitter":
				v := newTransmittable(conn, settings, nil)
				closeTarget = func() error { return v.close(ExplicitClosing) }
				v.start()
			case "transceiver":
				v := newTransceivable(conn, settings, nil)
				closeTarget = v.Close
				v.start()
			}
			externalDone := make(chan error, 1)
			go func() { externalDone <- closeTarget() }()
			select {
			case err := <-callbackDone:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				cleanup()
				t.Fatal("OnClosed could not close its own connection")
			}
			select {
			case err := <-externalDone:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				cleanup()
				t.Fatal("external Close did not join the closed callback")
			}
			cleanup()
			lifecycleAssertGoroutinesReturn(t, baseline)
		})
	}
}

func TestSessionLifecycleCloseFromExpiryCallback(t *testing.T) {
	baseline := runtime.NumGoroutine()
	conn, cleanup := newPipePair()
	defer cleanup()
	store := NewDefaultStore()
	settings := lifecycleSettings()
	settings.PduExpireTimeOut = time.Millisecond
	settings.ExpireCheckTimer = time.Millisecond
	callbackDone := make(chan error, 1)
	var session *Session
	settings.OnExpiredPduRequest = func(pdu.PDU) bool {
		callbackDone <- session.Close()
		return false
	}
	var err error
	session, err = NewSession(lifecycleConnector{connect: func() (*Connection, error) {
		return conn, nil
	}}, settings, 0, WithRequestStore(store))
	if err != nil {
		t.Fatal(err)
	}
	request := session.bound().out.newRequest(pdu.NewSubmitSM())
	request.WrittenAt = time.Now().Add(-time.Hour)
	seedWindowRequest(t, session.bound().out, request)
	select {
	case err := <-callbackDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expiry callback could not close its own session")
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	cleanup()
	lifecycleAssertGoroutinesReturn(t, baseline)
}

func TestSessionLifecycleCloseFromRebindCallback(t *testing.T) {
	baseline := runtime.NumGoroutine()
	first, cleanFirst := newPipePair()
	defer cleanFirst()
	next, cleanNext := newPipePair()
	defer cleanNext()
	var attempts atomic.Int32
	callbackDone := make(chan error, 1)
	var session *Session
	settings := Settings{ReadTimeout: time.Hour, OnRebind: func() {
		callbackDone <- session.Close()
	}}
	var err error
	session, err = NewSession(lifecycleConnector{connect: func() (*Connection, error) {
		if attempts.Add(1) == 1 {
			return first, nil
		}
		return next, nil
	}}, settings, 0)
	if err != nil {
		t.Fatal(err)
	}
	rebound := make(chan struct{})
	go func() { defer close(rebound); session.rebind() }()
	select {
	case err := <-callbackDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("rebind callback could not close its own session")
	}
	lifecycleWait(t, rebound)
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	cleanFirst()
	cleanNext()
	lifecycleAssertGoroutinesReturn(t, baseline)
}

func TestServerLifecycleStopFromAcceptErrorCallback(t *testing.T) {
	baseline := runtime.NumGoroutine()
	done := make(chan struct{})
	var server *Server
	server = NewServer(ServerSettings{OnError: func(error) {
		server.Stop()
		close(done)
	}})
	listener := newPipeListener()
	listener.failures = 1
	server.listener = listener
	server.started = true
	server.active.Store(true)
	server.wg.Add(1)
	go server.acceptLoop()
	lifecycleWait(t, done)
	server.Stop()
	lifecycleAssertGoroutinesReturn(t, baseline)
}

func TestServerLifecycleStopFromBindErrorCallback(t *testing.T) {
	baseline := runtime.NumGoroutine()
	done := make(chan struct{})
	var server *Server
	server, listener := pipeServer(t, ServerSettings{OnError: func(error) {
		server.Stop()
		close(done)
	}}, 0)
	peer := pipeDial(t, listener)
	writeWire(t, peer, pdu.NewUnbind())
	lifecycleWait(t, done)
	server.Stop()
	_ = peer.Close()
	lifecycleAssertGoroutinesReturn(t, baseline)
}
