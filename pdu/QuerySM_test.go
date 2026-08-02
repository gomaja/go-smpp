package pdu

import (
	"testing"

	"github.com/gomaja/go-smpp/data"

	"github.com/stretchr/testify/require"
)

func TestQuerySM(t *testing.T) {
	v := NewQuerySM().(*QuerySM)
	require.True(t, v.CanResponse())
	require.NotNil(t, v.OptionalParameters)
	require.NotZero(t, v.GetSequenceNumber())
	v.SequenceNumber = 13

	validate(t,
		v.GetResponse(),
		"0000001480000003000000000000000d00000000",
		data.QUERY_SM_RESP,
	)

	v.MessageID = "away"
	_ = v.SourceAddr.SetAddress("Alicer")
	v.SourceAddr.SetTon(28)
	v.SourceAddr.SetNpi(29)

	validate(t,
		v,
		"0000001e00000003000000000000000d61776179001c1d416c6963657200",
		data.QUERY_SM,
	)
}

func TestQuerySMParseOptionalParameter(t *testing.T) {
	buf := NewBuffer(fromHex("0000001f000000030000000000000001617761790001014100020400021234"))

	parsed, err := Parse(buf)
	require.NoError(t, err)

	query, ok := parsed.(*QuerySM)
	require.True(t, ok)
	require.Equal(t, []byte{0x12, 0x34}, query.OptionalParameters[TagUserMessageReference].Data)
}
