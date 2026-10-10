package pdu

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"github.com/gomaja/go-smpp/data"
)

const (
	// SizeByte is size of byte.
	SizeByte = 1

	// SizeShort is size of short.
	SizeShort = 2

	// SizeInt is size of int.
	SizeInt = 4

	// SizeLong is size of long.
	SizeLong = 8
)

var (
	// ErrBufferNotEnoughByteToRead indicates not enough byte(s) to read from buffer.
	ErrBufferNotEnoughByteToRead = fmt.Errorf("not enough byte to read from buffer")

	endianese = binary.BigEndian
)

// ByteBuffer wraps over bytes.Buffer with additional features.
type ByteBuffer struct {
	*bytes.Buffer
	err error
}

// Err returns the first error encountered while encoding into the buffer.
// Callers must check Err before sending marshaled bytes.
func (c *ByteBuffer) Err() error {
	return c.err
}

// Reset clears the contents and any encoding error so the buffer can be reused.
func (c *ByteBuffer) Reset() {
	c.Buffer.Reset()
	c.err = nil
}

func (c *ByteBuffer) setError(err error) {
	if c.err == nil {
		c.err = err
	}
}

// NewBuffer create new buffer from preallocated buffer array.
func NewBuffer(inp []byte) *ByteBuffer {
	if inp == nil {
		inp = make([]byte, 0, 512)
	}
	return &ByteBuffer{Buffer: bytes.NewBuffer(inp)}
}

// ReadN read n-bytes from buffer.
func (c *ByteBuffer) ReadN(n int) (r []byte, err error) {
	if n > 0 {
		if c.Len() >= n { // optimistic branching
			r = make([]byte, n)
			_, _ = c.Read(r)
		} else {
			err = ErrBufferNotEnoughByteToRead
		}
	}
	return
}

// ReadShort reads short from buffer.
func (c *ByteBuffer) ReadShort() (r int16, err error) {
	v, err := c.ReadN(SizeShort)
	if err == nil {
		r = int16(endianese.Uint16(v))
	}
	return
}

// WriteShort writes short to buffer.
func (c *ByteBuffer) WriteShort(v int16) {
	var b [SizeShort]byte
	endianese.PutUint16(b[:], uint16(v))
	_, _ = c.Write(b[:])
}

// ReadInt reads int from buffer.
func (c *ByteBuffer) ReadInt() (r int32, err error) {
	v, err := c.ReadN(SizeInt)
	if err == nil {
		r = int32(endianese.Uint32(v))
	}
	return
}

// WriteInt writes int to buffer.
func (c *ByteBuffer) WriteInt(v int32) {
	var b [SizeInt]byte
	endianese.PutUint32(b[:], uint32(v))
	_, _ = c.Write(b[:])
}

// WriteBuffer appends buffer.
func (c *ByteBuffer) WriteBuffer(d *ByteBuffer) {
	if d != nil {
		_, _ = c.Write(d.Bytes())
	}
}

func (c *ByteBuffer) writeString(st string, isCString bool, enc data.Encoding) (err error) {
	defer func() { c.setError(err) }()
	if isCString && bytes.IndexByte([]byte(st), 0) >= 0 {
		return fmt.Errorf("C-Octet string contains an embedded terminator")
	}
	if len(st) > 0 {
		var payload []byte
		if payload, err = enc.Encode(st); err == nil {
			if isCString && bytes.IndexByte(payload, 0) >= 0 {
				return fmt.Errorf("encoded C-Octet string contains an embedded terminator")
			}
			_, _ = c.Write(payload)
		}
	}

	if err == nil && isCString {
		_ = c.WriteByte(0)
	}

	return
}

// WriteCString writes c-string.
func (c *ByteBuffer) WriteCString(s string) error {
	if bytes.IndexByte([]byte(s), 0) >= 0 {
		err := fmt.Errorf("C-Octet string contains an embedded terminator")
		c.setError(err)
		return err
	}
	// SMPP v3.4 §3.1 specifies ASCII; retain peer-provided octets for
	// interoperability. Only embedded NUL would corrupt field framing.
	_, _ = c.WriteString(s)
	_ = c.WriteByte(0)
	return nil
}

// WriteCStringWithEnc write c-string with encoding.
func (c *ByteBuffer) WriteCStringWithEnc(s string, enc data.Encoding) error {
	return c.writeString(s, true, enc)
}

// ReadCString reads a frame-bounded C-Octet string without a length limit.
// An unterminated string returns its remaining octets together with io.EOF.
func (c *ByteBuffer) ReadCString() (st string, err error) {
	buf, err := c.ReadBytes(0)
	// Preserve every received octet. Mandatory fields still return EOF when
	// unterminated; bind/submit responses deliberately tolerate a trailing EOF.
	if err == nil && len(buf) > 0 {
		buf = buf[:len(buf)-1]
	}
	st = string(buf)
	return
}

// HexDump returns hex dump.
func (c *ByteBuffer) HexDump() string {
	return fmt.Sprintf("%x", c.Buffer.Bytes())
}
