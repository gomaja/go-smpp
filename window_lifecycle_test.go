package gosmpp

import (
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/gomaja/go-smpp/pdu"
	"github.com/stretchr/testify/require"
)

func TestSessionCloseDoesNotReturnAlreadyClosed(t *testing.T) {
	conn := &windowCloseConn{
		readStarted: make(chan struct{}),
		peerEOF:     make(chan struct{}),
		closed:      make(chan struct{}),
	}
	s, err := NewSession(windowCloseConnector{conn}, Settings{ReadTimeout: time.Minute}, 0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	select {
	case <-conn.readStarted:
	case <-time.After(time.Second):
		t.Fatal("reader did not start")
	}

	// The peer closes immediately after the shutdown write. The transport must
	// have a single owner even when the reader observes EOF during Close.
	require.NoError(t, s.Close())
}

type windowCloseConnector struct{ conn net.Conn }

func (c windowCloseConnector) Connect() (*Connection, error) { return NewConnection(c.conn), nil }
func (windowCloseConnector) GetBindType() pdu.BindingType    { return pdu.Transceiver }

type windowCloseConn struct {
	readStarted chan struct{}
	peerEOF     chan struct{}
	closed      chan struct{}
	readOnce    sync.Once
	eofOnce     sync.Once
	closeOnce   sync.Once
}

func (c *windowCloseConn) Read([]byte) (int, error) {
	c.readOnce.Do(func() { close(c.readStarted) })
	select {
	case <-c.peerEOF:
		return 0, io.EOF
	case <-c.closed:
		return 0, net.ErrClosed
	}
}

func (c *windowCloseConn) Write(b []byte) (int, error) {
	c.eofOnce.Do(func() { close(c.peerEOF) })
	return len(b), nil
}

func (c *windowCloseConn) Close() error {
	closed := false
	c.closeOnce.Do(func() {
		closed = true
		close(c.closed)
	})
	if !closed {
		return net.ErrClosed
	}
	return nil
}

func (*windowCloseConn) LocalAddr() net.Addr              { return windowCloseAddr("local") }
func (*windowCloseConn) RemoteAddr() net.Addr             { return windowCloseAddr("remote") }
func (*windowCloseConn) SetDeadline(time.Time) error      { return nil }
func (*windowCloseConn) SetReadDeadline(time.Time) error  { return nil }
func (*windowCloseConn) SetWriteDeadline(time.Time) error { return nil }

type windowCloseAddr string

func (windowCloseAddr) Network() string  { return "tcp" }
func (a windowCloseAddr) String() string { return string(a) }
