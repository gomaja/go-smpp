package pdu

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFieldRejectsOversizedValue(t *testing.T) {
	f := NewField(TagMessagePayload, make([]byte, 65536))
	b := NewBuffer(nil)
	f.Marshal(b)
	require.Zero(t, b.Len(), "TLV value cannot fit its unsigned 16-bit length")
}

func TestFieldUnsignedLength(t *testing.T) {
	for _, n := range []int{32767, 32768, 65535} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			payload := bytes.Repeat([]byte{0xa5}, n)
			wire := make([]byte, 4+n)
			binary.BigEndian.PutUint16(wire, uint16(TagMessagePayload))
			binary.BigEndian.PutUint16(wire[2:], uint16(n))
			copy(wire[4:], payload)
			b := NewBuffer(wire)
			var f Field
			require.NoError(t, f.Unmarshal(b))
			require.Len(t, f.Data, n, "unsigned TLV length")
			require.Equal(t, payload, f.Data)
			require.Zero(t, b.Len())
		})
	}
}

func TestFieldTruncatedLengthDoesNotConsume(t *testing.T) {
	for _, wire := range [][]byte{{4}, {4, 36, 0}, {4, 36, 0, 2, 1}, {4, 36, 128, 0}, {4, 36, 255, 255, 1}} {
		b := NewBuffer(wire)
		f := NewField(TagUserMessageReference, []byte{7})
		require.Error(t, f.Unmarshal(b), "truncated TLV %x", wire)
		require.Equal(t, wire, b.Bytes(), "failed TLV must consume no bytes")
		require.Equal(t, NewField(TagUserMessageReference, []byte{7}), f)
	}
}

func TestFieldZeroLengthOnWire(t *testing.T) {
	f := NewField(TagAlertOnMessageDelivery, nil)
	b := NewBuffer(nil)
	f.Marshal(b)
	require.Equal(t, []byte{0x13, 0x0c, 0, 0}, b.Bytes())
	var parsed Field
	require.NoError(t, parsed.Unmarshal(b))
	require.Equal(t, TagAlertOnMessageDelivery, parsed.Tag)
	require.Empty(t, parsed.Data)
}

func TestFieldConstructors(t *testing.T) {
	src := []byte{0x01, 0x02}
	field := NewField(TagUserMessageReference, src)
	src[0] = 0xff

	require.Equal(t, TagUserMessageReference, field.Tag)
	require.Equal(t, []byte{0x01, 0x02}, field.Data)
	require.Equal(t, Field{Tag: TagDestBearerType, Data: []byte{0x5f}}, NewFieldUint8(TagDestBearerType, 0x5f))
	require.Equal(t, Field{Tag: TagSarMsgRefNum, Data: []byte{0x12, 0x34}}, NewFieldUint16(TagSarMsgRefNum, 0x1234))
	require.Equal(t, Field{Tag: TagQosTimeToLive, Data: []byte{0x12, 0x34, 0x56, 0x78}}, NewFieldUint32(TagQosTimeToLive, 0x12345678))
}

func TestFieldIntegerAccessors(t *testing.T) {
	v8, err := NewFieldUint8(TagDestBearerType, 0x5f).Uint8()
	require.NoError(t, err)
	require.Equal(t, uint8(0x5f), v8)

	v16, err := NewFieldUint16(TagSarMsgRefNum, 0x1234).Uint16()
	require.NoError(t, err)
	require.Equal(t, uint16(0x1234), v16)

	v32, err := NewFieldUint32(TagQosTimeToLive, 0x12345678).Uint32()
	require.NoError(t, err)
	require.Equal(t, uint32(0x12345678), v32)

	_, err = Field{Tag: TagDestBearerType, Data: []byte{0x01, 0x02}}.Uint8()
	require.Error(t, err)

	_, err = Field{Tag: TagSarMsgRefNum, Data: []byte{0x01}}.Uint16()
	require.Error(t, err)

	_, err = Field{Tag: TagQosTimeToLive, Data: []byte{0x01, 0x02, 0x03}}.Uint32()
	require.Error(t, err)
}
