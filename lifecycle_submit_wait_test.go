package gosmpp

import (
	"errors"
	"net"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gomaja/go-smpp/pdu"
)

func lifecycleAwaitSubmitWait(t *testing.T, id uint64) {
	t.Helper()
	prefix := "goroutine " + strconv.FormatUint(id, 10) + " "
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		buffer := make([]byte, 1<<20)
		for _, stack := range strings.Split(string(buffer[:runtime.Stack(buffer, true)]), "\n\n") {
			if strings.HasPrefix(stack, prefix) && strings.Contains(stack, "[select]") && strings.Contains(stack, "(*transmittable).SubmitContext(") {
				return
			}
		}
		runtime.Gosched()
	}
	t.Fatal("forwarding callback did not reach the full outbound queue")
}

func testLifecycleSubmitCloseCycle(t *testing.T, server, closeFirst bool) {
	t.Helper()
	baseline := runtime.NumGoroutine()
	upstreamConn, cleanup := newPipePair()
	writerEntered, closeGate, submitGate := make(chan struct{}), make(chan struct{}), make(chan struct{})
	writerIDs, forwardingIDs := make(chan uint64, 1), make(chan uint64, 1)
	closeReturned, submitReturned := make(chan error, 1), make(chan error, 1)
	var releaseClose, releaseSubmit sync.Once
	unblock := func() {
		releaseClose.Do(func() { close(closeGate) })
		releaseSubmit.Do(func() { close(submitGate) })
	}
	var closeDownstream func() error
	upstream, err := NewSession(lifecycleConnector{connect: func() (*Connection, error) { return upstreamConn, nil }}, Settings{
		ReadTimeout: time.Hour,
		OnSubmitError: func(_ pdu.PDU, err error) {
			if !errors.Is(err, pdu.ErrEncode) {
				return
			}
			writerIDs <- callbackGoroutineID()
			close(writerEntered)
			<-closeGate
			closeReturned <- closeDownstream()
		},
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	forward := func() {
		forwardingIDs <- callbackGoroutineID()
		<-submitGate
		submitReturned <- upstream.Transceiver().Submit(pdu.NewSubmitSM())
	}
	var downstreamScope *callbackScope
	var peer net.Conn
	if server {
		srv := NewServer(ServerSettings{Address: "127.0.0.1:0"})
		if err := srv.StartSystemID(SystemIDConfig{SystemID: "sys", Password: "pw",
			OnPDU: func(_ ServerSessionInfo, request pdu.PDU) (pdu.PDU, bool) {
				forward()
				return request.GetResponse(), false
			},
		}); err != nil {
			t.Fatal(err)
		}
		if err := srv.Start(); err != nil {
			t.Fatal(err)
		}
		peer, _ = bind(t, srv.Addr().String(), "sys", "pw", pdu.Transceiver)
		downstreamScope = &srv.callbacks
		closeDownstream = func() error { srv.Stop(); return nil }
	} else {
		client, other := net.Pipe()
		peer = other
		session, err := NewSession(lifecycleConnector{connect: func() (*Connection, error) { return NewConnection(client), nil }}, Settings{
			ReadTimeout: time.Hour, OnPDU: func(pdu.PDU, bool) { forward() },
		}, 0)
		if err != nil {
			t.Fatal(err)
		}
		downstreamScope = &session.callbacks
		closeDownstream = session.Close
	}
	t.Cleanup(func() {
		unblock()
		upstream.bound().stop(ExplicitClosing)
		_ = peer.Close()
		_ = closeDownstream()
		_ = upstream.Close()
		cleanup()
	})
	invalid := pdu.NewSubmitSM().(*pdu.SubmitSM)
	invalid.ServiceType = "invalid\x00service"
	if err := upstream.Transceiver().Submit(invalid); err != nil {
		t.Fatal(err)
	}
	lifecycleWait(t, writerEntered)
	writerID := <-writerIDs
	if err := upstream.Transceiver().Submit(pdu.NewSubmitSM()); err != nil {
		t.Fatal(err)
	}
	if server {
		writeWire(t, peer, pdu.NewSubmitSM())
	} else {
		writeWire(t, peer, pdu.NewEnquireLinkResp())
	}
	forwardingID := <-forwardingIDs
	if closeFirst {
		releaseClose.Do(func() { close(closeGate) })
		lifecycleAwaitWait(t, writerID, downstreamScope)
		releaseSubmit.Do(func() { close(submitGate) })
	} else {
		releaseSubmit.Do(func() { close(submitGate) })
		lifecycleAwaitSubmitWait(t, forwardingID)
		releaseClose.Do(func() { close(closeGate) })
	}
	select {
	case err := <-closeReturned:
		if err != nil {
			t.Errorf("downstream close: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("shutdown and the forwarding Submit formed a wait cycle")
		// Initiating upstream shutdown interrupts queue admission even when its
		// writer is stuck in a callback, so a failing regression can clean up.
		upstream.bound().stop(ExplicitClosing)
		select {
		case err := <-closeReturned:
			if err != nil {
				t.Errorf("downstream close after upstream shutdown: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("upstream shutdown did not release the forwarding callback")
		}
	}
	select {
	case err := <-submitReturned:
		if closeFirst && !errors.Is(err, ErrSubmitQueueFull) {
			t.Errorf("cyclic queue admission = %v, want ErrSubmitQueueFull", err)
		} else if !closeFirst && err != nil && !t.Failed() {
			t.Errorf("forwarding Submit after shutdown escaped = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("forwarding Submit did not finish")
	}
	unblock()
	_ = peer.Close()
	_ = closeDownstream()
	_ = upstream.Close()
	cleanup()
	lifecycleAssertGoroutinesReturn(t, baseline)
}

func TestSessionLifecycleSubmitBackpressureCloseCycle(t *testing.T) {
	for _, closeFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "submit_then_close", true: "close_then_submit"}[closeFirst], func(t *testing.T) {
			testLifecycleSubmitCloseCycle(t, false, closeFirst)
		})
	}
}

func TestServerLifecycleForwardingSubmitBackpressureStopCycle(t *testing.T) {
	for _, closeFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "submit_then_stop", true: "stop_then_submit"}[closeFirst], func(t *testing.T) {
			testLifecycleSubmitCloseCycle(t, true, closeFirst)
		})
	}
}
