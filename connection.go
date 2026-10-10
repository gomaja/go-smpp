package gosmpp

import (
	"bufio"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gomaja/go-smpp/pdu"
)

// Connection wraps over net.Conn with buffered data reader.
type Connection struct {
	systemID    string
	conn        net.Conn
	reader      *bufio.Reader
	readerOnce  sync.Once
	writeMu     sync.Mutex
	closeOnce   sync.Once
	closeErr    error
	closing     atomic.Bool
	writeFailed atomic.Bool
}

// NewConnection returns a Connection. Its read buffer is allocated on the first
// Read, so idle connections and raw bind exchanges retain no read buffer.
func NewConnection(conn net.Conn) (c *Connection) {
	c = &Connection{
		conn: conn,
	}
	return
}

// Read reads data from the connection.
// Read can be made to time out and return an Error with Timeout() == true
// after a fixed time limit; see SetDeadline and SetReadDeadline.
func (c *Connection) Read(b []byte) (n int, err error) {
	c.readerOnce.Do(func() { c.reader = bufio.NewReaderSize(c.conn, 128<<10) })
	n, err = c.reader.Read(b)
	return
}

// Write writes data to the connection.
// Write can be made to time out and return an Error with Timeout() == true
// after a fixed time limit; see SetDeadline and SetWriteDeadline.
func (c *Connection) Write(b []byte) (n int, err error) {
	n, err = c.conn.Write(b)
	return
}

// WritePDU writes a PDU to the connection. After shutdown, a partial write or a
// permanent transport error, it returns ErrConnectionClosing, including for a
// writer waiting for transport access. Encoding errors and zero-byte timeouts
// leave the transport usable.
func (c *Connection) WritePDU(p pdu.PDU) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.closing.Load() || c.writeFailed.Load() {
		return 0, ErrConnectionClosing
	}
	return c.writePDU(p)
}

// writePDUWithTimeout applies the deadline only after the preceding write has
// finished. Otherwise a waiting response could extend an active delivery's
// deadline, or spend its own timeout waiting for the writer lock.
func (c *Connection) writePDUWithTimeout(p pdu.PDU, timeout time.Duration) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.closing.Load() || c.writeFailed.Load() {
		return 0, ErrConnectionClosing
	}
	var deadline time.Time
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	if err := c.conn.SetWriteDeadline(deadline); err != nil {
		return 0, err
	}
	return c.writePDU(p)
}

func (c *Connection) writePDU(p pdu.PDU) (n int, err error) {
	buf := pdu.NewBuffer(make([]byte, 0, 64))
	p.Marshal(buf)
	if err = buf.Err(); err != nil {
		return 0, &pdu.EncodeError{PDU: p, Err: err}
	}
	n, err = c.conn.Write(buf.Bytes())
	if err == nil && n != buf.Len() {
		err = io.ErrShortWrite
	}
	var networkError net.Error
	if err != nil && (n > 0 || !errors.As(err, &networkError) || !networkError.Timeout()) {
		c.writeFailed.Store(true)
	}
	return n, err
}

const unbindWriteTimeout = 50 * time.Millisecond

// SMPP v3.4 §4.2 and v5 §§2.3.5–2.3.6: explicitly deregister before
// dropping the transport. Do not queue behind an active (possibly partial)
// write, or wait indefinitely for a peer that has stopped reading.
func (c *Connection) tryUnbind() {
	// Reject writers already waiting for the lock as well as future writers;
	// otherwise one can append application traffic after our unbind.
	c.closing.Store(true)
	if !c.writeMu.TryLock() {
		return
	}
	defer c.writeMu.Unlock()
	// An interrupted write can release the lock before shutdown gets here.
	// Its frame may be incomplete, so no further PDU can safely be appended.
	if c.writeFailed.Load() {
		return
	}
	if err := c.conn.SetWriteDeadline(time.Now().Add(unbindWriteTimeout)); err != nil {
		return
	}
	_, _ = c.writePDU(pdu.NewUnbind())
}

// Close closes the connection.
// Any blocked Read or Write operations will be unblocked and return errors.
// For TLS, the underlying Close may wait for its close_notify write timeout.
func (c *Connection) Close() error {
	c.closing.Store(true)
	c.closeOnce.Do(func() { c.closeErr = c.conn.Close() })
	return c.closeErr
}

// LocalAddr returns the local network address.
func (c *Connection) LocalAddr() net.Addr {
	return c.conn.LocalAddr()
}

// RemoteAddr returns the remote network address.
func (c *Connection) RemoteAddr() net.Addr {
	return c.conn.RemoteAddr()
}

// SetDeadline sets the read and write deadlines associated
// with the connection. It is equivalent to calling both
// SetReadDeadline and SetWriteDeadline.
//
// A deadline is an absolute time after which I/O operations
// fail with a timeout (see type Error) instead of
// blocking. The deadline applies to all future and pending
// I/O, not just the immediately following call to Read or
// Write. After a deadline has been exceeded, the connection
// can be refreshed by setting a deadline in the future.
//
// An idle timeout can be implemented by repeatedly extending
// the deadline after successful Read or Write calls.
//
// A zero value for t means I/O operations will not time out.
//
// Note that if a TCP connection has keep-alive turned on,
// which is the default unless overridden by Dialer.KeepAlive
// or ListenConfig.KeepAlive, then a keep-alive failure may
// also return a timeout error. On Unix systems a keep-alive
// failure on I/O can be detected using
// errors.Is(err, syscall.ETIMEDOUT).
func (c *Connection) SetDeadline(t time.Time) error {
	return c.conn.SetDeadline(t)
}

// SetReadDeadline sets the deadline for future Read calls
// and any currently-blocked Read call.
// A zero value for t means Read will not time out.
func (c *Connection) SetReadDeadline(t time.Time) error {
	return c.conn.SetReadDeadline(t)
}

// SetReadTimeout is equivalent to ReadDeadline(now + timeout)
func (c *Connection) SetReadTimeout(t time.Duration) error {
	return c.conn.SetReadDeadline(time.Now().Add(t))
}

// SetWriteDeadline sets the deadline for future Write calls
// and any currently-blocked Write call.
// Even if write times out, it may return n > 0, indicating that
// some of the data was successfully written.
// A zero value for t means Write will not time out.
func (c *Connection) SetWriteDeadline(t time.Time) error {
	return c.conn.SetWriteDeadline(t)
}

// SetWriteTimeout is equivalent to WriteDeadline(now + timeout)
func (c *Connection) SetWriteTimeout(t time.Duration) error {
	return c.conn.SetWriteDeadline(time.Now().Add(t))
}
