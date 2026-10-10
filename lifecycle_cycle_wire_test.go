package gosmpp

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func mutualTCPAwaitCallbackJoin(t *testing.T, id string, completed <-chan struct{}) bool {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	buffer := make([]byte, 1<<20)
	for time.Now().Before(deadline) {
		select {
		case <-completed:
			t.Error("first callback returned before joining its partner")
			return false
		default:
		}
		stacks := string(buffer[:runtime.Stack(buffer, true)])
		for _, stack := range strings.Split(stacks, "\n\n") {
			// Match this callback's ID, not the external Close caller, which
			// also has a Session.Close frame while waiting on the first session.
			if strings.HasPrefix(stack, "goroutine "+id+" [chan receive]:\n") &&
				strings.Contains(stack, "(*Session).Close(") {
				return true
			}
		}
		runtime.Gosched()
	}
	t.Errorf("callback goroutine %s did not join its partner\n%s", id, buffer[:runtime.Stack(buffer, true)])
	return false
}

func TestSessionLifecycleMutualCloseTCP(t *testing.T) {
	baseline := runtime.NumGoroutine()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	var sessions [2]*Session
	var peers [2]net.Conn
	completed := [2]chan struct{}{make(chan struct{}), make(chan struct{})}
	firstCallbackID := make(chan string, 1)
	firstCallbackJoined := make(chan struct{})
	var allowPartnerOnce sync.Once
	allowPartnerClose := func() { allowPartnerOnce.Do(func() { close(firstCallbackJoined) }) }
	t.Cleanup(allowPartnerClose)
	partnerCloseReturned := make(chan struct{})
	releaseCallback := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseCallback) }) }
	t.Cleanup(release)
	callbackErrors := make(chan error, 4)
	for i := range sessions {
		client, dialErr := net.DialTimeout("tcp", listener.Addr().String(), 2*time.Second)
		if dialErr != nil {
			t.Fatal(dialErr)
		}
		t.Cleanup(func() { _ = client.Close() })
		peer, acceptErr := listener.Accept()
		if acceptErr != nil {
			t.Fatal(acceptErr)
		}
		peers[i] = peer
		t.Cleanup(func() { _ = peer.Close() })
		sessions[i], err = NewSession(lifecycleConnector{connect: func() (*Connection, error) {
			return NewConnection(client), nil
		}}, Settings{ReadTimeout: time.Hour, OnClosed: func(state State) {
			if state != ExplicitClosing {
				callbackErrors <- fmt.Errorf("session %d closed with state %v", i, state)
			}
			if i == 0 {
				var header [64]byte
				n := runtime.Stack(header[:], false)
				firstCallbackID <- strings.Fields(string(header[:n]))[1]
			} else {
				// The first callback must register its wait for this session
				// before this callback closes the first session. Otherwise either
				// edge can validly escape the cycle, changing what Close joins.
				<-firstCallbackJoined
			}
			if closeErr := sessions[1-i].Close(); closeErr != nil {
				callbackErrors <- closeErr
			}
			if i == 1 {
				// Closing the partner formed the cycle. Keep this callback
				// active to prove the external Close still joins both callbacks.
				close(partnerCloseReturned)
				<-releaseCallback
			}
			close(completed[i])
		}}, 0)
		if err != nil {
			t.Fatal(err)
		}
	}

	// SMPP v3.4 §4.2 and v5 §§2.3.5–2.3.6: both explicit closes
	// must transmit unbind before closing their independent TCP transports.
	readersReady := [2]chan struct{}{make(chan struct{}), make(chan struct{})}
	wireResults := make(chan error, 2)
	for i, peer := range peers {
		if err := peer.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatal(err)
		}
		go func() {
			close(readersReady[i])
			var header [16]byte
			if _, readErr := io.ReadFull(peer, header[:]); readErr != nil {
				wireResults <- fmt.Errorf("peer %d received no unbind: %w", i, readErr)
				return
			}
			if binary.BigEndian.Uint32(header[:4]) != 16 ||
				binary.BigEndian.Uint32(header[4:8]) != 0x00000006 ||
				binary.BigEndian.Uint32(header[8:12]) != 0 ||
				binary.BigEndian.Uint32(header[12:]) == 0 {
				wireResults <- fmt.Errorf("peer %d unbind header = %x", i, header)
				return
			}
			var extra [1]byte
			n, readErr := peer.Read(extra[:])
			if n != 0 || !errors.Is(readErr, io.EOF) {
				wireResults <- fmt.Errorf("peer %d transport after unbind: %x, %v", i, extra[:n], readErr)
				return
			}
			wireResults <- nil
		}()
	}
	for _, ready := range readersReady {
		lifecycleWait(t, ready)
	}

	externalClosed := make(chan error, 1)
	go func() { externalClosed <- sessions[0].Close() }()
	var firstID string
	select {
	case firstID = <-firstCallbackID:
	case <-time.After(2 * time.Second):
		t.Fatal("first TCP session's callback did not start")
	}
	if !mutualTCPAwaitCallbackJoin(t, firstID, completed[0]) {
		return
	}
	allowPartnerClose()
	select {
	case <-partnerCloseReturned:
	case <-time.After(2 * time.Second):
		stack := make([]byte, 1<<20)
		n := runtime.Stack(stack, true)
		t.Fatalf("mutual TCP Close cycle did not reach partner-close return\n%s", stack[:n])
	}
	select {
	case err := <-externalClosed:
		t.Fatalf("external Close returned while partner callback remained active: %v", err)
	default:
	}
	release()
	select {
	case err := <-externalClosed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("external TCP Close did not join the mutual callback cycle")
	}
	for i, done := range completed {
		select {
		case <-done:
		default:
			t.Fatalf("external Close returned before session %d callback completed", i)
		}
	}
	for range peers {
		select {
		case err := <-wireResults:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("TCP unbind reader did not finish")
		}
	}
	select {
	case err := <-callbackErrors:
		t.Fatal(err)
	default:
	}
	for _, session := range sessions {
		if err := session.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	for _, peer := range peers {
		_ = peer.Close()
	}
	lifecycleAssertGoroutinesReturn(t, baseline)
}
