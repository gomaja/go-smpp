package pdu

import (
	"testing"

	"github.com/stretchr/testify/require"
)

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
