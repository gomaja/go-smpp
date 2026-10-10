package pdu

import (
	"bytes"
	"strings"
	"testing"

	"github.com/gomaja/go-smpp/data"

	"github.com/stretchr/testify/require"
)

func TestOutbind(t *testing.T) {
	v := NewOutbind().(*Outbind)
	require.False(t, v.CanResponse())
	require.Nil(t, v.GetResponse())
	require.True(t, v.IsOk())
	v.SequenceNumber = 13

	v.SystemID = "inventory"
	v.Password = "password"

	validate(t,
		v,
		"000000230000000b000000000000000d696e76656e746f72790070617373776f726400",
		data.OUTBIND,
	)
}

func TestOutbindCredentialInteroperability(t *testing.T) {
	p := NewOutbind().(*Outbind)
	p.SystemID = strings.Repeat("I", 128)
	p.Password = strings.Repeat("P", 128)
	b := NewBuffer(nil)
	p.Marshal(b)
	require.NoError(t, b.Err())
	require.Equal(t, []byte(p.SystemID+"\x00"+p.Password+"\x00"), b.Bytes()[16:])
	parsed, err := Parse(bytes.NewReader(b.Bytes()))
	require.NoError(t, err)
	require.Equal(t, p, parsed)
	for _, field := range []*string{&p.SystemID, &p.Password} {
		old := *field
		*field = "A\x00B"
		invalid := NewBuffer(nil)
		p.Marshal(invalid)
		require.ErrorIs(t, invalid.Err(), ErrEncode)
		require.Empty(t, invalid.Bytes())
		*field = old
	}
}
