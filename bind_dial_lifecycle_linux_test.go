//go:build linux

package gosmpp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/gomaja/go-smpp/pdu"
)

// A zero-backlog listener holds one established connection. Filling its accept
// queue provides a loopback peer that drops subsequent SYNs without a firewall.
func bindBlockedListener(t *testing.T) (string, func() (net.Conn, error), func()) {
	t.Helper()
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	closeListener := func() { once.Do(func() { _ = syscall.Close(fd) }) }
	t.Cleanup(closeListener)
	if err := syscall.Bind(fd, &syscall.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}}); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Listen(fd, 0); err != nil {
		t.Fatal(err)
	}
	address, err := syscall.Getsockname(fd)
	if err != nil {
		t.Fatal(err)
	}
	accept := func() (net.Conn, error) {
		acceptedFD, _, err := syscall.Accept(fd)
		if err != nil {
			return nil, err
		}
		file := os.NewFile(uintptr(acceptedFD), "bind-peer")
		connection, err := net.FileConn(file)
		_ = file.Close()
		return connection, err
	}
	return fmt.Sprintf("127.0.0.1:%d", address.(*syscall.SockaddrInet4).Port), accept, closeListener
}

func TestSessionCloseCancelsBlockedDefaultDialer(t *testing.T) {
	baseline := runtime.NumGoroutine()
	address, accept, closeListener := bindBlockedListener(t)
	peerResult := make(chan struct {
		connection net.Conn
		err        error
	}, 1)
	go func() {
		peer, err := accept()
		if err == nil {
			request, parseErr := pdu.Parse(peer)
			err = parseErr
			if err == nil {
				_, err = NewConnection(peer).WritePDU(request.GetResponse())
			}
		}
		peerResult <- struct {
			connection net.Conn
			err        error
		}{peer, err}
	}()
	entered, finished := make(chan struct{}), make(chan struct{})
	var attempts atomic.Int32
	dialer := DialerFunc(func(ctx context.Context, address string) (net.Conn, error) {
		if attempts.Add(1) == 2 {
			close(entered)
			defer close(finished)
		}
		return NonTLSDialer.DialContext(ctx, address)
	})
	session, err := NewSession(TRXConnector(dialer, Auth{SMSC: address}), Settings{ReadTimeout: time.Hour}, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	peer := <-peerResult
	if peer.err != nil {
		t.Fatal(peer.err)
	}
	defer func() { _ = peer.connection.Close() }()
	filler, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = filler.Close() }()
	probe, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
	if probe != nil {
		_ = probe.Close()
	}
	var networkError net.Error
	if !errors.As(err, &networkError) || !networkError.Timeout() {
		t.Fatalf("accept queue was not full: dial error = %v", err)
	}
	_ = peer.connection.Close()
	lifecycleWait(t, entered)
	closed := make(chan error, 1)
	go func() { closed <- session.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Errorf("Close: %v", err)
		}
	case <-time.After(time.Second):
		t.Error("Close waited for the operating system's TCP dial timeout")
		closeListener()
		select {
		case err := <-closed:
			if err != nil {
				t.Errorf("Close after releasing blocked dial: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("closing the listener did not release the blocked dial")
		}
	}
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Error("the default dial worker kept running after cancellation")
		closeListener()
		lifecycleWait(t, finished)
	}
	closeListener()
	deadline := time.Now().Add(time.Second)
	for runtime.NumGoroutine() > baseline && time.Now().Before(deadline) {
		runtime.Gosched()
	}
	if got := runtime.NumGoroutine(); got > baseline {
		t.Errorf("closed rebind left workers: baseline %d, got %d", baseline, got)
	}
}
