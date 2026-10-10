package pdu

import (
	"bytes"
	"testing"

	"github.com/gomaja/go-smpp/data"

	"github.com/stretchr/testify/require"
)

func TestReplaceSMMessageAssignmentWireFormat(t *testing.T) {
	r := NewReplaceSM().(*ReplaceSM)
	var err error
	r.Message, err = NewShortMessageWithEncoding("A", data.ASCII)
	require.NoError(t, err)
	b := NewBuffer(nil)
	r.Marshal(b)
	parsed, err := Parse(bytes.NewReader(b.Bytes()))
	require.NoError(t, err, "replace_sm has no data_coding field")
	text, err := parsed.(*ReplaceSM).Message.GetMessage()
	require.NoError(t, err)
	require.Equal(t, "A", text)
	// The same message value in submit_sm must include data_coding.
	s := NewSubmitSM().(*SubmitSM)
	s.Message = r.Message
	b = NewBuffer(nil)
	s.Marshal(b)
	parsed, err = Parse(bytes.NewReader(b.Bytes()))
	require.NoError(t, err)
	require.Equal(t, data.ASCIICoding, parsed.(*SubmitSM).Message.Encoding().DataCoding())
}

func TestReplaceSM(t *testing.T) {
	v := NewReplaceSM().(*ReplaceSM)
	require.True(t, v.CanResponse())
	v.SequenceNumber = 13

	validate(t,
		v.GetResponse(),
		"0000001080000007000000000000000d",
		data.REPLACE_SM_RESP,
	)

	v.MessageID = "ID_Her"
	_ = v.SourceAddr.SetAddress("Alicer")
	v.SourceAddr.SetTon(28)
	v.SourceAddr.SetNpi(29)
	v.RegisteredDelivery = 83
	_ = v.Message.SetMessageWithEncoding("nightwish", data.GSM7BIT)
	v.Message.message = ""
	require.Equal(t, data.GSM7BIT, v.Message.Encoding())

	message, err := v.Message.GetMessage()
	require.Nil(t, err)
	require.Equal(t, "nightwish", message)

	validate(t,
		v,
		"0000002e00000007000000000000000d49445f486572001c1d416c696365720000005300096e6967687477697368",
		data.REPLACE_SM,
	)
}
