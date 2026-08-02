package pdu

import (
	"strings"
	"testing"

	"github.com/gomaja/go-smpp/data"

	"github.com/stretchr/testify/require"
)

func TestSubmitSM(t *testing.T) {
	v := NewSubmitSM().(*SubmitSM)
	require.True(t, v.CanResponse())
	v.SequenceNumber = 13

	validate(t,
		v.GetResponse(),
		"0000001180000004000000000000000d00",
		data.SUBMIT_SM_RESP,
	)

	v.ServiceType = "abc"
	_ = v.SourceAddr.SetAddress("Alicer")
	v.SourceAddr.SetTon(28)
	v.SourceAddr.SetNpi(29)

	_ = v.DestAddr.SetAddress("Bob")
	v.DestAddr.SetTon(79)
	v.DestAddr.SetNpi(80)

	v.EsmClass = 77 ^ data.SM_UDH_GSM
	v.ProtocolID = 99
	v.PriorityFlag = 61
	v.RegisteredDelivery = 83
	_ = v.Message.SetMessageWithEncoding("nghắ nghiêng nghiễng ngả", data.UCS2)
	v.Message.message = ""

	validate(t,
		v,
		"0000005d00000004000000000000000d616263001c1d416c69636572004f50426f62000d633d00005300080030006e006700681eaf0020006e00670068006900ea006e00670020006e0067006800691ec5006e00670020006e00671ea3",
		data.SUBMIT_SM,
	)
}

func TestSubmitSMSplitAssignsIndependentHeaders(t *testing.T) {
	v := NewSubmitSM().(*SubmitSM)
	require.NoError(t, v.Message.SetMessageWithEncoding(strings.Repeat("a", 170), data.GSM7BIT))
	v.RegisterOptionalParam(Field{Tag: TagUserMessageReference, Data: []byte{0x12, 0x34}})

	parts, err := v.Split()
	require.NoError(t, err)
	require.Greater(t, len(parts), 1)

	seen := make(map[int32]struct{}, len(parts))
	for _, part := range parts {
		require.Equal(t, data.SUBMIT_SM, part.CommandID)
		require.NotEqual(t, v.GetSequenceNumber(), part.GetSequenceNumber())
		require.NotZero(t, part.GetSequenceNumber())

		_, exists := seen[part.GetSequenceNumber()]
		require.False(t, exists, "duplicate sequence number %d", part.GetSequenceNumber())
		seen[part.GetSequenceNumber()] = struct{}{}

		require.Equal(t, v.OptionalParameters[TagUserMessageReference], part.OptionalParameters[TagUserMessageReference])
	}

	parts[0].OptionalParameters[TagUserMessageReference].Data[0] = 0xff
	require.Equal(t, []byte{0x12, 0x34}, parts[1].OptionalParameters[TagUserMessageReference].Data)
	require.Equal(t, []byte{0x12, 0x34}, v.OptionalParameters[TagUserMessageReference].Data)
}
