package pdu

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/gomaja/go-smpp/data"

	"github.com/stretchr/testify/require"
)

func TestSubmitMultiDestinationCountLimit(t *testing.T) {
	for _, n := range []int{254, 255, 256} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			s := NewSubmitMulti().(*SubmitMulti)
			d := NewDestinationAddress()
			a, err := NewAddressWithAddr("123")
			require.NoError(t, err)
			d.SetAddress(a)
			for range n {
				s.DestAddrs.l = append(s.DestAddrs.l, d)
			}
			b := NewBuffer(nil)
			s.Marshal(b)
			if n > 255 {
				require.Zero(t, b.Len(), "invalid destination count must emit no PDU")
			} else {
				p, err := Parse(bytes.NewReader(b.Bytes()))
				require.NoError(t, err)
				require.Len(t, p.(*SubmitMulti).DestAddrs.Get(), n)
			}
		})
	}
}

func TestSubmitMulti(t *testing.T) {
	v := NewSubmitMulti().(*SubmitMulti)
	require.True(t, v.CanResponse())
	v.SequenceNumber = 13

	validate(t,
		v.GetResponse(),
		"0000001280000021000000000000000d0000",
		data.SUBMIT_MULTI_RESP,
	)

	v.ServiceType = "abc"
	_ = v.SourceAddr.SetAddress("Alicer")
	v.SourceAddr.SetTon(28)
	v.SourceAddr.SetNpi(29)

	addr := NewAddress()
	require.Nil(t, addr.SetAddress("Bob1"))
	d1 := NewDestinationAddress()
	d1.SetAddress(addr)

	dl, err := NewDistributionList("List1")
	require.Nil(t, err)
	d2 := NewDestinationAddress()
	d2.SetDistributionList(dl)

	dl, err = NewDistributionList("List2")
	require.Nil(t, err)
	d3 := NewDestinationAddress()
	d3.SetDistributionList(dl)

	_ = v.DestAddrs.Add(d1, d2, d3)
	require.Equal(t, []DestinationAddress{d1, d2, d3}, v.DestAddrs.Get())

	v.EsmClass = 13
	v.ProtocolID = 99
	v.PriorityFlag = 61
	v.RegisteredDelivery = 83

	v.Message, err = NewShortMessageWithEncoding("nghắ nghiêng nghiễng ngả", data.UCS2)
	require.Nil(t, err)
	v.Message.message = ""

	validate(t,
		v,
		"0000006e00000021000000000000000d616263001c1d416c696365720003010000426f623100024c6973743100024c69737432000d633d00005300080030006e006700681eaf0020006e00670068006900ea006e00670020006e0067006800691ec5006e00670020006e00671ea3",
		data.SUBMIT_MULTI,
	)
}

func TestSubmitMultiwithUDH(t *testing.T) {
	v := NewSubmitMulti().(*SubmitMulti)
	require.True(t, v.CanResponse())
	v.SequenceNumber = 13

	validate(t,
		v.GetResponse(),
		"0000001280000021000000000000000d0000",
		data.SUBMIT_MULTI_RESP,
	)

	v.ServiceType = "abc"
	_ = v.SourceAddr.SetAddress("Alicer")
	v.SourceAddr.SetTon(28)
	v.SourceAddr.SetNpi(29)

	addr := NewAddress()
	require.Nil(t, addr.SetAddress("Bob1"))
	d1 := NewDestinationAddress()
	d1.SetAddress(addr)

	dl, err := NewDistributionList("List1")
	require.Nil(t, err)
	d2 := NewDestinationAddress()
	d2.SetDistributionList(dl)

	dl, err = NewDistributionList("List2")
	require.Nil(t, err)
	d3 := NewDestinationAddress()
	d3.SetDistributionList(dl)

	_ = v.DestAddrs.Add(d1, d2, d3)
	require.Equal(t, []DestinationAddress{d1, d2, d3}, v.DestAddrs.Get())

	v.EsmClass = 77
	v.ProtocolID = 99
	v.PriorityFlag = 61
	v.RegisteredDelivery = 83

	v.Message, err = NewShortMessageWithEncoding("nghắ nghiêng nghiễng ngả", data.UCS2)
	require.Nil(t, err)
	v.Message.message = ""
	_ = v.Message.SetUDH(UDH{NewIEConcatMessage(2, 1, 254)})

	validate(t,
		v,
		"0000007400000021000000000000000d616263001c1d416c696365720003010000426f623100024c6973743100024c69737432004d633d00005300080036050003fe0201006e006700681eaf0020006e00670068006900ea006e00670020006e0067006800691ec5006e00670020006e00671ea3",
		data.SUBMIT_MULTI,
	)
}

func TestSubmitMultiReceives255Destinations(t *testing.T) {
	for _, n := range []int{254, 255} {
		base := NewBuffer(nil)
		NewSubmitMulti().Marshal(base)
		body := base.Bytes()[16:]
		peer := append([]byte(nil), body[:4]...)
		peer = append(peer, byte(n))
		for range n {
			peer = append(peer, 1, 0, 0, '1', 0)
		}
		peer = append(peer, body[5:]...)
		parsed, err := Parse(bytes.NewReader(decodeTestFrame(data.SUBMIT_MULTI, peer)))
		require.NoError(t, err)
		dests := parsed.(*SubmitMulti).DestAddrs.Get()
		require.Len(t, dests, n)

		round := NewBuffer(nil)
		parsed.Marshal(round)

		require.NoError(t, round.Err())
		require.Equal(t, decodeTestFrame(data.SUBMIT_MULTI, peer), round.Bytes())
		for _, d := range dests {
			require.True(t, d.IsAddress())
			require.Equal(t, "1", d.Address().Address())
		}
	}
}

func TestSubmitMultiReceivesLongDistributionList(t *testing.T) {
	base := NewBuffer(nil)
	NewSubmitMulti().Marshal(base)
	body := base.Bytes()[16:]
	peer := append([]byte(nil), body[:4]...)
	peer = append(peer, 1, 2)
	peer = append(peer, bytes.Repeat([]byte{'L'}, 512)...)
	peer = append(peer, 0)
	peer = append(peer, body[5:]...)
	parsed, err := Parse(bytes.NewReader(decodeTestFrame(data.SUBMIT_MULTI, peer)))
	require.NoError(t, err)
	dests := parsed.(*SubmitMulti).DestAddrs.Get()
	require.Len(t, dests, 1)
	require.True(t, dests[0].IsDistributionList())
	require.Equal(t, string(bytes.Repeat([]byte{'L'}, 512)), dests[0].DistributionList().Name())
	round := NewBuffer(nil)
	parsed.Marshal(round)
	require.NoError(t, round.Err())
	require.Equal(t, decodeTestFrame(data.SUBMIT_MULTI, peer), round.Bytes())
}
