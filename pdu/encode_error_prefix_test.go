package pdu

import (
	"fmt"
	"io"
	"io/fs"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEncodeErrorPrefixBuffer(t *testing.T) {
	p := NewBindRequest(Transceiver)
	p.Password = "synthetic\x00password"
	buf := NewBuffer(nil)
	p.Marshal(buf)
	require.ErrorIs(t, buf.Err(), ErrEncode)
	require.EqualError(t, buf.Err(), "C-Octet string contains an embedded terminator")
}

func TestEncodeErrorPrefixAllPDUs(t *testing.T) {
	for id, newPDU := range pduMap {
		t.Run(fmt.Sprintf("%08x", uint32(id)), func(t *testing.T) {
			p := newPDU()
			p.AppendOptionalParam(NewField(TagMessagePayload, make([]byte, 65536)))
			buf := NewBuffer(nil)
			p.Marshal(buf)
			require.ErrorIs(t, buf.Err(), ErrEncode)
			require.EqualError(t, buf.Err(), "TLV length 65536 exceeds unsigned 16-bit maximum")
		})
	}
}

func TestEncodeErrorPrefixCause(t *testing.T) {
	cause := &fs.PathError{Op: "encode", Path: "synthetic", Err: io.ErrUnexpectedEOF}
	buf := NewBuffer(nil)
	p := NewSubmitSM().(*SubmitSM)
	p.base.marshal(buf, func(body *ByteBuffer) { body.setError(cause) })
	for _, err := range []error{buf.Err(), &EncodeError{PDU: p, Err: buf.Err()}} {
		require.ErrorIs(t, err, ErrEncode)
		require.ErrorIs(t, err, io.ErrUnexpectedEOF)
		require.NotErrorIs(t, err, io.EOF)
		var got *fs.PathError
		require.ErrorAs(t, err, &got)
		require.Same(t, cause, got)
	}
	require.EqualError(t, buf.Err(), "encode synthetic: unexpected EOF")
}
