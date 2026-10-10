package pdu

import (
	"bytes"
	"testing"

	"github.com/gomaja/go-smpp/data"

	"github.com/stretchr/testify/require"
)

func TestMalformUSME(t *testing.T) {
	t.Run("malformSME", func(t *testing.T) {
		b := NewBuffer(nil)
		var u UnsuccessSME
		require.NotNil(t, u.Unmarshal(b))
	})

	t.Run("malformSMEs", func(t *testing.T) {
		b := NewBuffer(nil)
		_ = b.WriteByte(1)
		var u UnsuccessSMEs
		require.NotNil(t, u.Unmarshal(b))
	})
}

func TestUnsuccessSMEsWireCountLimit(t *testing.T) {
	for _, n := range []int{254, 255, 256} {
		p := NewSubmitMultiResp().(*SubmitMultiResp)
		p.UnsuccessSMEs.Add(make([]UnsuccessSME, n)...)
		b := NewBuffer(nil)
		p.Marshal(b)
		if n > 255 {
			require.ErrorIs(t, b.Err(), ErrEncode)
			require.Empty(t, b.Bytes())
			var v *ValidationError
			require.ErrorAs(t, Validate(p), &v)
			require.Equal(t, "no_unsuccess", v.Field)
			require.Equal(t, data.ESME_RINVNUMDESTS, v.Status)
			continue
		}
		require.NoError(t, Validate(p))
		require.NoError(t, b.Err())
		q, err := Parse(bytes.NewReader(b.Bytes()))
		require.NoError(t, err)
		require.Len(t, q.(*SubmitMultiResp).UnsuccessSMEs.Get(), n)
	}
}
