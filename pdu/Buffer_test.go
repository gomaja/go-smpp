package pdu

import (
	"strings"
	"testing"

	"github.com/gomaja/go-smpp/data"

	"github.com/stretchr/testify/require"
)

func TestBufferResetClearsEncodingError(t *testing.T) {
	b := NewBuffer(nil)
	require.Error(t, b.WriteCString("A\x00B"))
	require.Error(t, b.Err())
	b.Reset()
	require.NoError(t, b.WriteCString("A"))
	require.NoError(t, b.Err(), "Reset must make buffer reusable")
	require.Equal(t, []byte{'A', 0}, b.Bytes())
}

func TestCStringRejectsEmbeddedTerminator(t *testing.T) {
	b := NewBuffer(nil)
	require.Error(t, b.WriteCString("A\x00B"))
	require.Zero(t, b.Len())
}

func TestCStringRejectsEncodedTerminator(t *testing.T) {
	b := NewBuffer(nil)
	require.Error(t, b.WriteCStringWithEnc("A", data.UCS2))
	require.Error(t, b.Err())
	require.Zero(t, b.Len(), "encoded NUL must not terminate the field early")
}

func TestCStringPreservesNonASCIIOctets(t *testing.T) {
	for _, wire := range [][]byte{{0x80, 0xff, 0}, {0xc3, 0xa9, 0}} {
		text, err := NewBuffer(wire).ReadCString()
		require.NoError(t, err)
		require.Equal(t, string(wire[:len(wire)-1]), text)
		b := NewBuffer(nil)
		require.NoError(t, b.WriteCString(text))
		require.NoError(t, b.Err())
		require.Equal(t, wire, b.Bytes())
	}
}

func TestBuffer(t *testing.T) {
	b := NewBuffer(nil)
	require.Nil(t, b.WriteCStringWithEnc("agjwklgjkwPץ", data.HEBREW))
	require.Equal(t, "61676A776B6C676A6B7750F500", strings.ToUpper(b.HexDump()))
}
