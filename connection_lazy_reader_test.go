package gosmpp

import (
	"bytes"
	"errors"
	"io"
	"runtime"
	"sync"
	"testing"
	"time"
)

func TestConnectionIdleAllocationBound(t *testing.T) {
	const count = 256
	connections := make([]*Connection, count)
	transport := &handshakeConn{Reader: bytes.NewReader(nil)}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for i := range connections {
		connections[i] = NewConnection(transport)
	}
	runtime.ReadMemStats(&after)
	average := (after.TotalAlloc - before.TotalAlloc) / count
	t.Logf("idle Connection allocation: %d bytes/connection", average)
	if average > 4<<10 {
		t.Fatalf("idle Connection allocated %d bytes/connection; limit 4096", average)
	}
	runtime.KeepAlive(connections)
}

type idleHandshakeConn struct {
	*handshakeConn
	entered chan<- struct{}
	release <-chan struct{}
}

func (c *idleHandshakeConn) Read([]byte) (int, error) {
	c.entered <- struct{}{}
	<-c.release
	return 0, io.EOF
}

func TestServerIdleHandshakeAllocationBound(t *testing.T) {
	const count = 256
	ready := make(chan struct{}, count)
	reading := make(chan struct{}, count)
	start, release := make(chan struct{}), make(chan struct{})
	finished := make(chan error, count)
	var releaseOnce sync.Once
	srv := NewServer(ServerSettings{BindTimeout: time.Minute})
	sessions := make([]*serverSession, count)
	for i := range sessions {
		conn := &idleHandshakeConn{handshakeConn: &handshakeConn{Reader: bytes.NewReader(nil)}, entered: reading, release: release}
		go func() {
			ready <- struct{}{}
			<-start
			session := newServerSession(conn, srv)
			sessions[i] = session
			finished <- session.handleBind()
		}()
	}
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		for range count {
			select {
			case err := <-finished:
				if !errors.Is(err, io.EOF) {
					t.Errorf("idle bind result: %v", err)
				}
			case <-time.After(testBindWait):
				t.Error("idle handshake worker did not finish")
				return
			}
		}
	})
	for range count {
		<-ready
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	close(start)
	for range count {
		select {
		case <-reading:
		case <-time.After(testBindWait):
			t.Fatal("idle handshake did not start raw read")
		}
	}
	runtime.ReadMemStats(&after)
	average := (after.TotalAlloc - before.TotalAlloc) / count
	t.Logf("idle handshake allocation before input: %d bytes/connection", average)
	if average > 4<<10 {
		t.Fatalf("idle handshake allocated %d bytes/connection before any input; limit 4096", average)
	}
	runtime.KeepAlive(sessions)
}

func TestConnectionLazyReaderPreservesReadAhead(t *testing.T) {
	wire := bytes.Repeat([]byte{1, 2, 3, 4}, 1024)
	transport := &handshakeConn{Reader: bytes.NewReader(wire)}
	conn := NewConnection(transport)
	var first [1]byte
	if _, err := io.ReadFull(conn, first[:]); err != nil || first[0] != wire[0] {
		t.Fatalf("first buffered read: %x, %v", first, err)
	}
	reader := conn.reader
	if reader == nil || reader.Size() != 128<<10 || reader.Buffered() != len(wire)-1 {
		t.Fatal("first buffered read did not retain read-ahead bytes")
	}
	got := make([]byte, len(wire)-1)
	if _, err := io.ReadFull(conn, got); err != nil || !bytes.Equal(got, wire[1:]) {
		t.Fatalf("subsequent read lost buffered bytes: %v", err)
	}
	if conn.reader != reader {
		t.Fatal("subsequent read replaced its buffered reader")
	}
}

var idleConnectionBenchmarkSink *Connection

func BenchmarkConnectionIdleAllocation(b *testing.B) {
	transport := &handshakeConn{Reader: bytes.NewReader(nil)}
	b.ReportAllocs()
	for b.Loop() {
		idleConnectionBenchmarkSink = NewConnection(transport)
	}
}
