package pdu

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"reflect"
	"testing"

	"github.com/gomaja/go-smpp/data"
	"github.com/stretchr/testify/require"
)

// mainUDHDecode preserves the decoder at main 16b0c3e for differential tests.
// Successful empty Data slices are compared by value: ownership and nilness
// deliberately follow go-sms now, rather than the old borrowed slices.
func mainUDHDecode(dst *UDH, src []byte) (int, error) {
	if len(src) == 0 || src[0] == 0 || len(src)-1 < int(src[0]) {
		return 0, fmt.Errorf("invalid header length")
	}
	end, read := 1+int(src[0]), 1
	ies := UDH{}
	for read < end {
		remaining := src[read:end]
		if len(remaining) < 2 || len(remaining)-2 < int(remaining[1]) {
			return 0, fmt.Errorf("invalid information element length")
		}
		n := 2 + int(remaining[1])
		ies = append(ies, InfoElement{ID: remaining[0], Data: remaining[2:n]})
		read += n
	}
	*dst = ies
	return read, nil
}

func equalUDHValues(a, b UDH) bool {
	if (a == nil) != (b == nil) || len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ID != b[i].ID || !bytes.Equal(a[i].Data, b[i].Data) {
			return false
		}
	}
	return true
}

func compareMainUDHDecode(t *testing.T, raw []byte) {
	t.Helper()
	want := UDH{{ID: 0x7f, Data: []byte{9}}}
	got := UDH{{ID: 0x7f, Data: []byte{9}}}
	wn, we := mainUDHDecode(&want, raw)
	gn, ge := got.UnmarshalBinary(raw)
	if wn != gn || (we == nil) != (ge == nil) || !equalUDHValues(want, got) {
		t.Fatalf("raw=%x main=(%d,%v,%v) current=(%d,%v,%v)", raw, wn, we, want, gn, ge, got)
	}
	if ge != nil && !reflect.DeepEqual(got, UDH{{ID: 0x7f, Data: []byte{9}}}) {
		t.Fatalf("failed decode changed receiver: %v", got)
	}
}

func TestUDHUnmarshalDifferential(t *testing.T) {
	for udhl := 0; udhl <= 255; udhl++ {
		for iedl := 0; iedl <= 255; iedl++ {
			raw := make([]byte, udhl+3)
			raw[0], raw[1], raw[2] = byte(udhl), 0x70, byte(iedl)
			compareMainUDHDecode(t, raw)
		}
	}
	for _, raw := range [][]byte{
		nil, {}, {0}, {0, 'A'}, {1, 0x70}, {2, 0x70, 0},
		{2, 0x70, 1, 'A'}, {3, 0x70, 1, 'A', 'B'},
		{5, 0, 3, 7, 2}, {5, 0, 3, 7, 2, 1, 'A'},
		{6, 0, 3, 7, 2, 1, 0x70, 'A'},
		{7, 0, 3, 7, 2, 1, 0x70, 0, 'A'},
		{6, 0x70, 1, 1, 0x70, 1, 2, 'A'},
		{8, 0xff, 0, 0x70, 1, 1, 0x70, 1, 2, 'A'},
	} {
		compareMainUDHDecode(t, raw)
	}
	t.Log("compared all 65,536 UDHL/first-IEDL combinations and additional header shapes")
}

func TestUDHMarshalDifferential(t *testing.T) {
	rng := rand.New(rand.NewPCG(23, 40))
	headers := []UDH{nil, {}, {{ID: 0x70}}, {NewIEConcatMessage(2, 1, 7)}, {NewIEConcatMessage16Bit(2, 1, 0x1234)}}
	for _, size := range []int{252, 253, 254, 255, 256, 65535} {
		headers = append(headers, UDH{{ID: 0x70, Data: make([]byte, size)}})
	}
	for i := 0; i < 2048; i++ {
		u := make(UDH, rng.IntN(16))
		for j := range u {
			u[j] = InfoElement{ID: byte(rng.Uint32()), Data: make([]byte, rng.IntN(40))}
			for k := range u[j].Data {
				u[j].Data[k] = byte(rng.Uint32())
			}
		}
		headers = append(headers, u)
	}
	for _, u := range headers {
		// main's preflight accepts at most 255 body octets, then writes
		// ID, IEDL, Data in order with a single leading UDHL octet.
		body := []byte{}
		for _, ie := range u {
			body = append(body, ie.ID, byte(len(ie.Data)))
			body = append(body, ie.Data...)
		}
		got, err := u.MarshalBinary()
		switch {
		case len(body) > 255:
			require.EqualError(t, err, "header limit (255 in marshal size) exceeds")
			require.Nil(t, got)
			require.Equal(t, -1, u.UDHL())
		case len(u) == 0:
			require.NoError(t, err)
			require.Nil(t, got)
			require.Zero(t, u.UDHL())
		default:
			require.NoError(t, err)
			require.Equal(t, append([]byte{byte(len(body))}, body...), got)
			require.Equal(t, len(got), u.UDHL())
			compareMainUDHDecode(t, got)
		}
	}
}

func TestUDHDecodedDataOwnership(t *testing.T) {
	src := []byte{8, 0x70, 1, 1, 0x70, 1, 2, 0xff, 0, 'A'}
	var u UDH
	n, err := u.UnmarshalBinary(src)
	require.NoError(t, err)
	require.Equal(t, 9, n)
	src[3], src[6] = 9, 9
	require.Equal(t, []byte{1}, u[0].Data)
	require.Equal(t, []byte{2}, u[1].Data)
	require.Empty(t, u[2].Data)
	ie, ok := u.FindInfoElement(0x70)
	require.True(t, ok)
	require.Same(t, &u[0], ie)
	ie.Data[0] = 7
	require.Equal(t, byte(7), u[0].Data[0])
	require.Equal(t, byte(9), src[3])
}

func udhFrame(id uint32, field []byte, udhi bool) []byte {
	// Independent SMPP v3.4 mandatory fields (§§4.4.1, 4.5.1, 4.6.1).
	body := []byte{0, 0, 0, 0} // service_type, source TON/NPI/address
	if id == uint32(data.SUBMIT_MULTI) {
		body = append(body, 1, 1) // one destination, SME address
	}
	body = append(body, 0, 0, 0) // destination TON/NPI/address
	esm := byte(0)
	if udhi {
		esm = data.SM_UDH_GSM
	}
	body = append(body, esm, 0, 0, 0, 0, 0, 0, 0, 7, byte(len(field)))
	body = append(body, field...)
	return benchmarkFrame(id, body)
}

func frameMessage(t *testing.T, p PDU) *ShortMessage {
	t.Helper()
	switch p := p.(type) {
	case *DeliverSM:
		return &p.Message
	case *SubmitSM:
		return &p.Message
	case *SubmitMulti:
		return &p.Message
	default:
		t.Fatalf("unexpected PDU %T", p)
		return nil
	}
}

func TestUDHMessageWireCompatibility(t *testing.T) {
	for _, id := range []uint32{uint32(data.DELIVER_SM), uint32(data.SUBMIT_SM), uint32(data.SUBMIT_MULTI)} {
		for _, field := range [][]byte{
			nil, {0}, {0, 'A'}, {1, 0x70}, {1, 0x70, 'A'},
			{2, 0x70, 1, 'A'}, {6, 0, 3, 7, 2, 1, 0x70, 'A'},
			{5, 0, 3, 7, 2, 1, 'A'}, {6, 8, 4, 0x12, 0x34, 2, 1, 'A'},
			{8, 0xff, 0, 0x70, 1, 1, 0x70, 1, 2, 'A'},
			{5, 0, 3, 7, 2}, // UDHL exceeds the field
			bytes.Repeat([]byte{'A'}, 254), bytes.Repeat([]byte{'A'}, 255),
		} {
			for _, udhi := range []bool{false, true} {
				t.Run(fmt.Sprintf("%x/%x/%t", id, field, udhi), func(t *testing.T) {
					wire := udhFrame(id, field, udhi)
					p, err := Parse(bytes.NewReader(wire))
					if udhi && len(field) > 0 && 1+int(field[0]) > len(field) {
						require.Error(t, err)
						return
					}
					require.NoError(t, err)
					sm := frameMessage(t, p)
					var want UDH
					var ignored []byte
					payload := field
					if udhi && len(field) > 0 {
						n, err := mainUDHDecode(&want, field)
						if err != nil {
							n = 1 + int(field[0])
							ignored = field[:n]
						}
						payload = field[n:]
					}
					require.True(t, equalUDHValues(want, sm.UDH()))
					require.Equal(t, ignored, sm.IgnoredUDH())
					gotPayload, err := sm.GetMessageData()
					require.NoError(t, err)
					require.True(t, bytes.Equal(payload, gotPayload))
					require.Equal(t, len(ignored)+sm.UDH().UDHL(), sm.headerLength())
					round := NewBuffer(nil)
					p.Marshal(round)
					require.NoError(t, round.Err())
					require.Equal(t, wire, round.Bytes())
				})
			}
		}
	}
}

func TestUDHMessageFramingIsTransactional(t *testing.T) {
	for _, wire := range [][]byte{
		nil, {0}, {0, 7}, {0, 7, 3, 0}, {0, 7, 5, 5, 0, 3, 7, 2},
	} {
		sm, err := NewShortMessage("old")
		require.NoError(t, err)
		require.NoError(t, sm.SetUDH(UDH{NewIEConcatMessage(2, 1, 7)}))
		before := sm
		require.Error(t, sm.Unmarshal(NewBuffer(wire), true))
		require.Equal(t, before, sm)
	}
}

func TestUDHShortMessageDifferential(t *testing.T) {
	for udhl := 0; udhl <= 255; udhl++ {
		for iedl := 0; iedl <= 255; iedl++ {
			field := make([]byte, 255)
			field[0], field[1], field[2] = byte(udhl), 0x70, byte(iedl)
			wire := append([]byte{0, 7, 255}, field...)
			sm := ShortMessage{message: "old"}
			err := sm.Unmarshal(NewBuffer(wire), true)
			if udhl == 255 {
				if err == nil || !reflect.DeepEqual(sm, ShortMessage{message: "old"}) {
					t.Fatalf("overflow udhl=%d iedl=%d err=%v receiver=%+v", udhl, iedl, err, sm)
				}
				continue
			}
			require.NoError(t, err)
			var want UDH
			_, parseErr := mainUDHDecode(&want, field)
			var ignored []byte
			if parseErr != nil {
				ignored = field[:udhl+1]
			}
			if !equalUDHValues(want, sm.UDH()) || !bytes.Equal(ignored, sm.IgnoredUDH()) ||
				!bytes.Equal(field[udhl+1:], sm.messageData) || sm.headerLength() != udhl+1 {
				t.Fatalf("udhl=%d iedl=%d parsed=%v ignored=%x payload=%x", udhl, iedl, sm.UDH(), sm.IgnoredUDH(), sm.messageData)
			}
			round := NewBuffer(nil)
			require.NoError(t, sm.Marshal(round))
			if !bytes.Equal(wire, round.Bytes()) {
				t.Fatalf("wire mismatch udhl=%d iedl=%d", udhl, iedl)
			}
		}
	}
	t.Log("compared all 65,536 UDHL/first-IEDL combinations in a 255-octet SMPP field")
}

func TestStandaloneInfoElementLengthDomain(t *testing.T) {
	for size := 0; size <= 255; size++ {
		src := append([]byte{0x70, byte(size)}, bytes.Repeat([]byte{0x41}, size)...)
		src = append(src, 0x99)
		var ie InfoElement
		n, err := ie.UnmarshalBinary(src)
		require.NoError(t, err)
		require.Equal(t, size+2, n)
		require.Equal(t, byte(0x70), ie.ID)
		require.Len(t, ie.Data, size)
		if size > 0 {
			src[2] = 0x42
			require.Equal(t, byte(0x42), ie.Data[0])
		}
	}
}

func TestStandaloneInfoElementErrorsLeaveReceiverUnchanged(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  []byte
	}{
		{"nil", nil},
		{"empty", []byte{}},
		{"missing_length", []byte{0x70}},
		{"missing_data", []byte{0x70, 1}},
		{"truncated_data", []byte{0x70, 2, 0x41}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ie := InfoElement{ID: 0x42, Data: []byte{9, 8}}
			n, err := ie.UnmarshalBinary(tc.raw)
			require.Error(t, err)
			require.Zero(t, n)
			require.Equal(t, InfoElement{ID: 0x42, Data: []byte{9, 8}}, ie)
		})
	}
}

func TestStandaloneInfoElementZeroLengthDataIsNonNil(t *testing.T) {
	ie := InfoElement{ID: 0xff, Data: []byte{9}}
	n, err := ie.UnmarshalBinary([]byte{0x70, 0})
	require.NoError(t, err)
	require.Equal(t, 2, n)
	require.Equal(t, byte(0x70), ie.ID)
	require.NotNil(t, ie.Data)
	require.Empty(t, ie.Data)
}

func TestFindInfoElementMissing(t *testing.T) {
	for _, tc := range []struct {
		name   string
		header UDH
	}{
		{"nil", nil},
		{"empty", UDH{}},
		{"nonmatching", UDH{{ID: 0x71, Data: []byte{9}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ie, found := tc.header.FindInfoElement(0x70)
			require.False(t, found)
			require.Nil(t, ie)
		})
	}
}
