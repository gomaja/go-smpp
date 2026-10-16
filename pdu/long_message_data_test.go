package pdu

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/gomaja/go-smpp/data"
	smpperrors "github.com/gomaja/go-smpp/errors"
	"github.com/stretchr/testify/require"
)

func rawPortHeader() UDH {
	// TS 23.040 §9.2.3.24.4: destination 5514, source 5515.
	return UDH{{ID: 0x05, Data: []byte{0x15, 0x8a, 0x15, 0x8b}}}
}

func TestLongMessageDataReproducer(t *testing.T) {
	for _, tc := range []struct {
		name   string
		size   int
		header UDH
		length []int
	}{
		{"268", 268, nil, []int{134, 134}},
		{"269", 269, nil, []int{134, 134, 1}},
		{"ports", 269, rawPortHeader(), []int{128, 128, 13}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewSubmitSM().(*SubmitSM)
			require.NoError(t, s.Message.SetUDH(tc.header))
			input := bytes.Repeat([]byte{0x80}, tc.size)
			require.NoError(t, s.Message.SetLongMessageDataWithEncoding(input, data.BINARY8BIT2))
			before := s.Message
			parts, err := s.Split()
			require.NoError(t, err)
			require.Len(t, parts, len(tc.length))
			require.Equal(t, before, s.Message, "multipart splitting leaves receiver unchanged")
			var joined []byte
			var reference uint16
			for i, part := range parts {
				require.Len(t, part.Message.messageData, tc.length[i])
				require.Len(t, part.Message.UDH(), 1+len(tc.header))
				require.Equal(t, byte(0), part.Message.UDH()[0].ID)
				if tc.header != nil {
					require.Equal(t, tc.header, part.Message.UDH()[1:])
					header, marshalErr := part.Message.UDH().MarshalBinary()
					require.NoError(t, marshalErr)
					require.Equal(t, []byte{5, 4, 0x15, 0x8a, 0x15, 0x8b}, header[6:])
				}
				total, seq, ref, found := part.Message.UDH().GetConcatInfo()
				require.True(t, found)
				require.Equal(t, byte(len(parts)), total)
				require.Equal(t, byte(i+1), seq)
				if i == 0 {
					reference = ref
				}
				require.Equal(t, reference, ref)
				require.LessOrEqual(t, part.Message.headerLength()+len(part.Message.messageData), 140)
				joined = append(joined, part.Message.messageData...)
			}
			require.Equal(t, input, joined)
			again, err := s.Split()
			require.NoError(t, err)
			_, _, fresh, _ := again[0].Message.UDH().GetConcatInfo()
			require.NotEqual(t, reference, fresh)
		})
	}
}

func TestLongMessageDataBoundaries(t *testing.T) {
	for _, tc := range []struct {
		size  int
		parts int
		last  int
	}{
		{0, 1, 0}, {140, 1, 140}, {141, 2, 7}, {34170, 255, 134}, {34171, 0, 0},
	} {
		t.Run(fmt.Sprint(tc.size), func(t *testing.T) {
			input := bytes.Repeat([]byte{0x80}, tc.size)
			var m ShortMessage
			require.NoError(t, m.SetLongMessageDataWithEncoding(input, data.BINARY8BIT2))
			parts, err := m.Split()
			if tc.parts == 0 {
				require.ErrorContains(t, err, "maximum is 255")
				require.Nil(t, parts)
				return
			}
			require.NoError(t, err)
			require.Len(t, parts, tc.parts)
			require.Len(t, parts[len(parts)-1].messageData, tc.last)
			if tc.parts == 1 {
				require.Same(t, &m, parts[0])
				require.Empty(t, parts[0].UDH())
			}
			var joined []byte
			for i, part := range parts {
				require.LessOrEqual(t, part.headerLength()+len(part.messageData), 140)
				if tc.parts > 1 {
					total, seq, _, ok := part.UDH().GetConcatInfo()
					require.True(t, ok)
					require.Equal(t, byte(tc.parts), total)
					require.Equal(t, byte(i+1), seq)
				}
				joined = append(joined, part.messageData...)
			}
			require.True(t, bytes.Equal(input, joined))
		})
	}
	for _, input := range [][]byte{nil, {}} {
		var m ShortMessage
		require.NoError(t, m.SetLongMessageDataWithEncoding(input, nil))
		require.Equal(t, input, m.messageData)
		require.Nil(t, m.enc)
		parts, err := m.Split()
		require.NoError(t, err)
		require.Len(t, parts, 1)
		require.Same(t, &m, parts[0])
		require.Equal(t, data.DefaultGSM7(), m.enc)
	}
}

func TestLongMessageDataStagedHeaders(t *testing.T) {
	for _, tc := range []struct {
		name   string
		header UDH
		id     byte
		limit  int
	}{
		{"wide", UDH{NewIEConcatMessage16Bit(9, 7, 0xbeef)}, 8, 133},
		{"last-wide", UDH{NewIEConcatMessage(8, 6, 0xaa), NewIEConcatMessage16Bit(9, 7, 0xbeef)}, 8, 133},
		{"last-narrow", UDH{NewIEConcatMessage16Bit(9, 7, 0xbeef), NewIEConcatMessage(8, 6, 0xaa)}, 0, 134},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var m ShortMessage
			require.NoError(t, m.SetUDH(tc.header))
			require.NoError(t, m.SetLongMessageDataWithEncoding(bytes.Repeat([]byte{0x80}, 269), data.BINARY8BIT2))
			parts, err := m.Split()
			require.NoError(t, err)
			require.Len(t, parts, 3)
			require.Len(t, parts[0].messageData, tc.limit)
			require.Len(t, parts[1].messageData, tc.limit)
			require.Len(t, parts[2].messageData, 269-2*tc.limit)
			for i, part := range parts {
				require.Len(t, part.UDH(), 1, "old concatenation IEs must be replaced")
				require.Equal(t, tc.id, part.UDH()[0].ID)
				total, seq, _, ok := part.UDH().GetConcatInfo()
				require.True(t, ok)
				require.Equal(t, byte(3), total)
				require.Equal(t, byte(i+1), seq)
			}
		})
	}
	var m ShortMessage
	// 135-octet staged header plus five concatenation IE octets leaves no data room.
	require.NoError(t, m.SetUDH(UDH{{ID: 0x70, Data: make([]byte, 132)}}))
	require.NoError(t, m.SetLongMessageDataWithEncoding(make([]byte, 6), data.BINARY8BIT2))
	parts, err := m.Split()
	require.ErrorIs(t, err, smpperrors.ErrUDHTooLong)
	require.Nil(t, parts)
}

func TestLongMessageDataOwnership(t *testing.T) {
	input := bytes.Repeat([]byte{0x80}, 269)
	var m ShortMessage
	require.NoError(t, m.SetUDH(rawPortHeader()))
	require.NoError(t, m.SetLongMessageDataWithEncoding(input, data.BINARY8BIT2))
	input[0] = 0x81
	require.Equal(t, byte(0x81), m.messageData[0], "setter retains caller's slice")
	parts, err := m.Split()
	require.NoError(t, err)
	require.Len(t, parts, 3)
	input[0] = 0xff
	require.Equal(t, byte(0x81), parts[0].messageData[0])
	next := bytes.Clone(parts[1].messageData)
	parts[0].messageData = append(parts[0].messageData, 0x42)
	parts[0].messageData[0] = 0x43
	require.Equal(t, next, parts[1].messageData, "append must not overwrite the next part")
	require.Equal(t, byte(0xff), input[0])
	parts[0].udHeader[1].Data[0] = 0xff
	require.Equal(t, rawPortHeader()[0], parts[1].udHeader[1])
	require.Equal(t, rawPortHeader(), m.UDH())
}

func TestLongMessageDataStagedIEOrder(t *testing.T) {
	var m ShortMessage
	m.SmDefaultMsgID = 3
	header := UDH{
		{ID: 0x70, Data: []byte{0x41}},
		NewIEConcatMessage(9, 7, 0xaa),
		rawPortHeader()[0],
		{ID: 0x71, Data: []byte{0x42}},
	}
	require.NoError(t, m.SetUDH(header))
	require.NoError(t, m.SetLongMessageDataWithEncoding(bytes.Repeat([]byte{0x80}, 269), data.BINARY8BIT2))
	parts, err := m.Split()
	require.NoError(t, err)
	require.Len(t, parts, 3)
	for i, part := range parts {
		require.Equal(t, byte(3), part.SmDefaultMsgID)
		require.Len(t, part.messageData, []int{122, 122, 25}[i])
		encoded, err := part.UDH().MarshalBinary()
		require.NoError(t, err)
		require.Equal(t, byte(17), encoded[0])
		require.Equal(t, []byte{0x70, 1, 0x41, 5, 4, 0x15, 0x8a, 0x15, 0x8b, 0x71, 1, 0x42}, encoded[6:])
	}
	require.Equal(t, header, m.UDH())
}

func TestLongMessageDataWire(t *testing.T) {
	s := NewSubmitSM().(*SubmitSM)
	require.NoError(t, s.Message.SetUDH(rawPortHeader()))
	require.NoError(t, s.Message.SetLongMessageDataWithEncoding(bytes.Repeat([]byte{0x80}, 269), data.BINARY8BIT2))
	kept := []Field{NewFieldUint16(TagUserMessageReference, 0x1234), NewField(TagMessagePayload, []byte{0x42})}
	for _, field := range kept {
		s.AppendOptionalParam(field)
	}
	s.AppendOptionalParam(NewFieldUint16(TagSarMsgRefNum, 0x9876))
	s.AppendOptionalParam(NewField(TagSarTotalSegments, []byte{9}))
	s.AppendOptionalParam(NewField(TagSarSegmentSeqnum, []byte{7}))
	parts, err := s.Split()
	require.NoError(t, err)
	require.Len(t, parts, 3)
	seen := map[int32]bool{s.SequenceNumber: true}
	for i, part := range parts {
		b := NewBuffer(nil)
		part.Marshal(b)
		require.NoError(t, b.Err())
		wire := bytes.Clone(b.Bytes())
		// Empty addresses and times put esm_class at 23 and sm_length at 32
		// (SMPP v3.4 §4.4.1 Table 4-10); inspect bytes independently of Parse.
		require.Equal(t, byte(0x40), wire[23]&0x40)
		wantLength := []byte{140, 140, 25}[i]
		require.Equal(t, wantLength, wire[32])
		header, err := part.Message.UDH().MarshalBinary()
		require.NoError(t, err)
		require.Equal(t, header, wire[33:45])
		require.Equal(t, part.Message.messageData, wire[45:33+int(wantLength)])
		parsed, err := Parse(bytes.NewReader(wire))
		require.NoError(t, err)
		got := parsed.(*SubmitSM)
		require.Equal(t, part.Message.UDH(), got.Message.UDH())
		require.Equal(t, part.Message.messageData, got.Message.messageData)
		require.Equal(t, kept, got.OptionalParameters)
		require.Equal(t, part.SequenceNumber, got.SequenceNumber)
		require.False(t, seen[got.SequenceNumber])
		seen[got.SequenceNumber] = true
	}
	// This test deliberately checks preservation, not validity of combining
	// message_payload and short_message (SMPP v3.4 §4.4.1 Table 4-10).
	require.Len(t, s.OptionalParameters, 5)
}

func TestLongMessageDataSetterState(t *testing.T) {
	m, err := NewShortMessage("earlier text")
	require.NoError(t, err)
	require.NoError(t, m.SetUDH(rawPortHeader()))
	m.SmDefaultMsgID = 3
	input := []byte{0x80, 0x81}
	require.NoError(t, m.SetLongMessageDataWithEncoding(input, nil))
	require.Equal(t, rawPortHeader(), m.UDH())
	require.Empty(t, m.message)
	require.Nil(t, m.enc)
	require.Equal(t, byte(3), m.SmDefaultMsgID)
	b := NewBuffer(nil)
	require.NoError(t, m.Marshal(b))
	require.Equal(t, []byte{0, 3, 9, 6, 5, 4, 0x15, 0x8a, 0x15, 0x8b, 0x80, 0x81}, b.Bytes())
	for _, size := range []int{254, 255, 256, 269} {
		s := NewSubmitSM().(*SubmitSM)
		require.NoError(t, s.Message.SetLongMessageDataWithEncoding(make([]byte, size), data.BINARY8BIT2))
		require.True(t, s.ShouldSplit())
		err := Validate(s)
		if size > 254 {
			var validation *ValidationError
			require.ErrorAs(t, err, &validation)
			require.Equal(t, data.ESME_RINVMSGLEN, validation.Status)
		} else {
			require.NoError(t, err)
		}
		b := NewBuffer(nil)
		s.Marshal(b)
		if size > 255 {
			require.ErrorIs(t, b.Err(), smpperrors.ErrShortMessageLengthTooLarge)
		} else {
			require.NoError(t, b.Err())
		}
	}
	// Header marshal errors must leave all previous state unchanged.
	m.udHeader = UDH{{ID: 0x70, Data: make([]byte, 256)}}
	before := m
	require.Error(t, m.SetLongMessageDataWithEncoding([]byte{1}, data.ASCII))
	require.Equal(t, before, m)
}

func TestLongMessageDataReceivedHeaders(t *testing.T) {
	for _, header := range [][]byte{
		{6, 5, 4, 0x15, 0x8a, 0x15, 0x8b},
		{3, 0x70, 2, 0x41}, // bounded header with truncated final IE
		{0},                // received empty header is retained as ignored
	} {
		var m ShortMessage
		wire := append([]byte{4, 0, byte(len(header) + 1)}, header...)
		wire = append(wire, 0x80)
		require.NoError(t, m.Unmarshal(NewBuffer(wire), true))
		beforeUDH, beforeIgnored := m.UDH(), m.IgnoredUDH()
		require.NoError(t, m.SetLongMessageDataWithEncoding([]byte{0x81}, data.BINARY8BIT2))
		require.Equal(t, beforeUDH, m.UDH())
		require.Equal(t, beforeIgnored, m.IgnoredUDH())
		b := NewBuffer(nil)
		require.NoError(t, m.Marshal(b))
		want := append([]byte{4, 0, byte(len(header) + 1)}, header...)
		want = append(want, 0x81)
		require.Equal(t, want, b.Bytes())
	}
}

func requireRawPartsEquivalent(t *testing.T, left, right []*ShortMessage) {
	t.Helper()
	require.Len(t, right, len(left))
	for i, a := range left {
		b := right[i]
		require.Equal(t, a.enc, b.enc)
		require.Equal(t, a.messageData, b.messageData)
		require.Equal(t, a.SmDefaultMsgID, b.SmDefaultMsgID)
		require.Len(t, b.UDH(), len(a.UDH()))
		for j, ie := range a.UDH() {
			require.Equal(t, ie.ID, b.UDH()[j].ID)
			switch ie.ID {
			case 0:
				require.Equal(t, ie.Data[1:], b.UDH()[j].Data[1:])
			case 8:
				require.Equal(t, ie.Data[2:], b.UDH()[j].Data[2:])
			default:
				require.Equal(t, ie.Data, b.UDH()[j].Data)
			}
		}
	}
}

func TestLongMessageDataShortSetterEquivalence(t *testing.T) {
	for name, enc := range map[string]data.Encoding{
		"nil": nil, "binary1": data.BINARY8BIT1, "binary2": data.BINARY8BIT2,
		"gsm7": data.GSM7BIT, "packed": data.GSM7BITPACKED, "ucs2": data.UCS2,
		"ascii": data.ASCII, "latin1": data.LATIN1, "custom": data.NewCustomEncoding(0x10, data.ASCII),
	} {
		for _, header := range []UDH{nil, rawPortHeader()} {
			t.Run(fmt.Sprintf("%s/header=%t", name, header != nil), func(t *testing.T) {
				for n := 0; n <= 254-header.UDHL(); n++ {
					var short, long ShortMessage
					require.NoError(t, short.SetUDH(header))
					require.NoError(t, long.SetUDH(header))
					input := bytes.Repeat([]byte{0x41}, n)
					require.NoError(t, short.SetMessageDataWithEncoding(input, enc))
					require.NoError(t, long.SetLongMessageDataWithEncoding(input, enc))
					require.Equal(t, short, long, "length %d", n)
					a, shortErr := short.Split()
					b, longErr := long.Split()
					if shortErr != nil {
						require.EqualError(t, longErr, shortErr.Error(), "length %d", n)
						require.Nil(t, b)
					} else {
						require.NoError(t, longErr, "length %d", n)
						requireRawPartsEquivalent(t, a, b)
					}
				}
			})
		}
	}
}

func TestLongMessageDataTextEquivalence(t *testing.T) {
	for name, enc := range map[string]data.Encoding{
		"nil": nil, "gsm7": data.GSM7BIT, "packed": data.GSM7BITPACKED,
		"ucs2": data.UCS2, "ascii": data.ASCII, "latin1": data.LATIN1,
		"custom": data.NewCustomEncoding(0x10, data.ASCII),
	} {
		t.Run(name, func(t *testing.T) {
			codec := enc
			if codec == nil {
				codec = data.DefaultGSM7()
			}
			// 400 A's give unambiguous packed input: no terminal CR or padding.
			text := strings.Repeat("A", 400)
			input, err := codec.Encode(text)
			require.NoError(t, err)
			require.Greater(t, len(input), 254)
			var raw ShortMessage
			require.NoError(t, raw.SetLongMessageDataWithEncoding(input, enc))
			got, err := raw.Split()
			require.NoError(t, err)
			want, err := NewLongMessageWithEncoding(text, enc)
			require.NoError(t, err)
			requireRawPartsEquivalent(t, want, got)
		})
	}
}

func FuzzLongMessageDataSplit(f *testing.F) {
	for _, size := range []int{0, 140, 141, 268, 269, 34170, 34171} {
		for choice := byte(0); choice < 6; choice++ {
			f.Add(bytes.Repeat([]byte{0x41}, size), choice, choice%4)
		}
	}
	f.Fuzz(func(t *testing.T, input []byte, encodingChoice, headerChoice byte) {
		// Bound resource use while retaining the 255-segment overflow boundary.
		if len(input) > 65536 {
			return
		}
		enc := []data.Encoding{data.BINARY8BIT1, data.BINARY8BIT2, nil, data.ASCII, data.LATIN1, data.GSM7BIT}[encodingChoice%6]
		header := []UDH{nil, rawPortHeader(), {NewIEConcatMessage16Bit(9, 7, 0xbeef)}, {{ID: 0x70, Data: make([]byte, 132)}}}[headerChoice%4]
		var m ShortMessage
		require.NoError(t, m.SetUDH(header))
		require.NoError(t, m.SetLongMessageDataWithEncoding(input, enc))
		parts, err := m.Split()
		if err != nil {
			// Existing splitter errors: header capacity, one-octet segment
			// count, character capacity, or non-round-trippable raw text.
			reason := err.Error()
			require.True(t, err == smpperrors.ErrUDHTooLong ||
				strings.Contains(reason, "maximum is 255") ||
				strings.Contains(reason, "cannot fit a GSM character") ||
				strings.Contains(reason, "cannot be resegmented without changing encoded content"), "unexpected error: %v", err)
			require.Nil(t, parts)
			return
		}
		require.NotEmpty(t, parts)
		require.LessOrEqual(t, len(parts), 255)
		var joined []byte
		for _, part := range parts {
			limit := 140
			if encodingChoice%6 == 2 || encodingChoice%6 == 5 {
				limit = 160 - (part.headerLength()*8+6)/7
				require.LessOrEqual(t, len(part.messageData), limit)
			} else {
				require.LessOrEqual(t, part.headerLength()+len(part.messageData), limit)
			}
			joined = append(joined, part.messageData...)
		}
		if encodingChoice%6 < 2 {
			require.True(t, bytes.Equal(input, joined))
		}
	})
}
