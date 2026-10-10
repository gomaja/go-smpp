package pdu

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAddressRejectsEmbeddedTerminator(t *testing.T) {
	_, err := NewAddressWithAddr("A\x00B")
	require.Error(t, err)
}

func TestAddressPreservesNonASCIIOctets(t *testing.T) {
	for _, addr := range []string{"é", "A\x80\xff"} {
		a, err := NewAddressWithAddr(addr)
		require.NoError(t, err)
		b := NewBuffer(nil)
		a.Marshal(b)
		require.NoError(t, b.Err())
		var parsed Address
		require.NoError(t, parsed.Unmarshal(NewBuffer(b.Bytes())))
		require.Equal(t, a, parsed)
	}
}

func TestAddressTerminatorLimit(t *testing.T) {
	a, err := NewAddressWithAddr(strings.Repeat("1", 20))
	require.NoError(t, err)
	b := NewBuffer(nil)
	a.Marshal(b)
	require.Len(t, b.Bytes()[2:], 21)
	require.NoError(t, a.SetAddress(strings.Repeat("1", 64)))
	require.Error(t, a.SetAddress(strings.Repeat("1", 65)), "65 content octets plus NUL exceed the widest address field")
	require.Equal(t, strings.Repeat("1", 64), a.Address())
}

func TestAddress(t *testing.T) {
	t.Run("new", func(t *testing.T) {
		a, err := NewAddressWithAddr("abc")
		require.Nil(t, err)
		require.Equal(t, "abc", a.Address())
	})

	t.Run("newWithAddr", func(t *testing.T) {
		_, err := NewAddressWithAddr(strings.Repeat("1", 65))
		require.NotNil(t, err)
	})

	t.Run("newTonNpi", func(t *testing.T) {
		a := NewAddressWithTonNpi(3, 7)
		require.Nil(t, a.SetAddress("123456789"))
		require.EqualValues(t, 3, a.Ton())
		require.EqualValues(t, 7, a.Npi())
		require.Equal(t, "123456789", a.Address())
		a.SetTon(11)
		a.SetNpi(19)
		require.EqualValues(t, 11, a.Ton())
		require.EqualValues(t, 19, a.Npi())
	})

	t.Run("newTonNpiAddr", func(t *testing.T) {
		a, err := NewAddressWithTonNpiAddr(3, 7, "123456789")
		require.Nil(t, err)
		require.EqualValues(t, 3, a.Ton())
		require.EqualValues(t, 7, a.Npi())
		require.Equal(t, "123456789", a.Address())
		a.SetTon(11)
		a.SetNpi(19)
		require.EqualValues(t, 11, a.Ton())
		require.EqualValues(t, 19, a.Npi())
	})

	t.Run("unmarshal", func(t *testing.T) {
		buf := NewBuffer(fromHex("315b7068616e746f6d537472696b6500"))
		var a Address
		require.Nil(t, a.Unmarshal(buf))
		require.Zero(t, buf.Len())
		require.Equal(t, "phantomStrike", a.Address())
		require.EqualValues(t, 49, a.Ton())
		require.EqualValues(t, 91, a.Npi())
	})

	t.Run("marshal", func(t *testing.T) {
		a, err := NewAddressWithAddr("phantomOpera")
		require.Nil(t, err)
		a.SetTon(95)
		a.SetNpi(13)

		buf := NewBuffer(nil)
		a.Marshal(buf)

		require.Equal(t, fromHex("5f0d7068616e746f6d4f7065726100"), buf.Bytes())
	})
}
