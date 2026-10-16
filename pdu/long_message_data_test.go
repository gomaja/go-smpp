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
	// TS 23.038 §§6.1.2.1, 6.2.1: A is septet 0x41. With a six-octet
	// concatenation UDH, packing starts with one fill bit: the first A is
	// 0x82, followed by nineteen groups of eight A's in seven octets.
	packedFirst := append([]byte{0x82}, bytes.Repeat([]byte{0xc1, 0x60, 0x30, 0x18, 0x0c, 0x06, 0x83}, 19)...)
	for _, tc := range []struct {
		name  string
		enc   data.Encoding
		first []byte
	}{
		{"nil", nil, bytes.Repeat([]byte{0x41}, 153)},
		{"gsm7", data.GSM7BIT, bytes.Repeat([]byte{0x41}, 153)},
		{"packed", data.GSM7BITPACKED, packedFirst},
		{"ucs2", data.UCS2, bytes.Repeat([]byte{0, 0x41}, 67)},
		{"ascii", data.ASCII, bytes.Repeat([]byte{0x41}, 134)},
		{"latin1", data.LATIN1, bytes.Repeat([]byte{0x41}, 134)},
		{"custom", data.NewCustomEncoding(0x10, data.ASCII), bytes.Repeat([]byte{0x41}, 134)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			codec := tc.enc
			if codec == nil {
				codec = data.DefaultGSM7()
			}
			// 400 A's give unambiguous packed input: no terminal CR or padding.
			text := strings.Repeat("A", 400)
			input, err := codec.Encode(text)
			require.NoError(t, err)
			require.Greater(t, len(input), 254)
			var raw ShortMessage
			require.NoError(t, raw.SetLongMessageDataWithEncoding(input, tc.enc))
			before := raw
			got, err := raw.Split()
			require.NoError(t, err)
			require.Greater(t, len(got), 1)
			require.Equal(t, before, raw, "multipart Split must not resolve the source encoding")
			want, err := NewLongMessageWithEncoding(text, tc.enc)
			require.NoError(t, err)
			requireRawPartsEquivalent(t, want, got)
			for _, parts := range [][]*ShortMessage{got, want} {
				require.Equal(t, tc.first, parts[0].messageData)
				var joined strings.Builder
				for _, part := range parts {
					// Decode a received field with its own UDH and no retained
					// text, independently of both paths' shared splitter.
					b := NewBuffer(nil)
					require.NoError(t, part.Marshal(b))
					var received ShortMessage
					require.NoError(t, received.Unmarshal(NewBuffer(b.Bytes()), true))
					decoded, err := received.GetMessageWithEncoding(codec)
					require.NoError(t, err)
					joined.WriteString(decoded)
				}
				require.Equal(t, text, joined.String())
			}
		})
	}
}

func TestLongMessageDataMultipartNilEncoding(t *testing.T) {
	var m ShortMessage
	require.NoError(t, m.SetUDH(rawPortHeader()))
	input := bytes.Repeat([]byte{0x41}, 400)
	snapshot := bytes.Clone(input)
	require.NoError(t, m.SetLongMessageDataWithEncoding(input, nil))
	parts, err := m.Split()
	require.NoError(t, err)
	require.Len(t, parts, 3)
	require.Nil(t, m.enc)
	require.Equal(t, snapshot, m.messageData)
	require.Equal(t, rawPortHeader(), m.UDH())
	for _, part := range parts {
		require.NotSame(t, &m, part)
		require.Equal(t, data.DefaultGSM7(), part.enc)
	}
}

func FuzzLongMessageDataSplit(f *testing.F) {
	for _, size := range []int{0, 140, 141, 268, 269, 34170, 34171} {
		for choice := byte(0); choice < 6; choice++ {
			for header := byte(0); header < 7; header++ {
				f.Add(bytes.Repeat([]byte{0x41}, size), choice, header)
			}
		}
	}
	f.Add(bytes.Repeat([]byte{0xff}, 161), byte(3), byte(0)) // ASCII round-trip failure
	f.Add(append(bytes.Repeat([]byte{0x41}, 161), 0x1b), byte(5), byte(0))
	f.Add(bytes.Repeat([]byte{0x1b, 0x14}, 4), byte(5), byte(4)) // two-septet character, one-septet capacity
	for _, n := range []int{255 * 76, 255*76 + 1} {
		f.Add(bytes.Repeat([]byte{0x1b, 0x14}, n), byte(5), byte(0))
	}
	f.Fuzz(func(t *testing.T, input []byte, encodingChoice, headerChoice byte) {
		// Bound resource use while retaining the 255-segment overflow boundary.
		if len(input) > 65536 {
			return
		}
		enc := []data.Encoding{data.BINARY8BIT1, data.BINARY8BIT2, nil, data.ASCII, data.LATIN1, data.GSM7BIT}[encodingChoice%6]
		header := []UDH{
			nil, rawPortHeader(), {NewIEConcatMessage16Bit(9, 7, 0xbeef)},
			{{ID: 0x70, Data: make([]byte, 132)}},
			{{ID: 0x70, Data: make([]byte, 131)}},
			{NewIEConcatMessage16Bit(9, 7, 0xbeef), NewIEConcatMessage(8, 6, 0xaa)},
			{NewIEConcatMessage(8, 6, 0xaa), NewIEConcatMessage16Bit(9, 7, 0xbeef)},
		}[headerChoice%7]
		// Independent complete-header arithmetic, including UDHL. Rebuilding
		// keeps non-concatenation IEs and the last concatenation IE's width
		// (TS 23.040 §§9.2.3.24, 9.2.3.24.1, 9.2.3.24.8).
		originalOctets, otherOctets, concatOctets := 0, 0, 5
		if len(header) > 0 {
			originalOctets = 1
		}
		for _, ie := range header {
			originalOctets += 2 + len(ie.Data)
			switch ie.ID {
			case 0:
				concatOctets = 5
			case 8:
				concatOctets = 6
			default:
				otherOctets += 2 + len(ie.Data)
			}
		}
		rebuiltOctets := 1 + concatOctets + otherOctets
		gsm := encodingChoice%6 == 2 || encodingChoice%6 == 5
		capacity := func(octets int) int {
			if gsm {
				return 160 - (octets*8+6)/7
			}
			return 140 - octets
		}
		var m ShortMessage
		require.NoError(t, m.SetUDH(header))
		require.NoError(t, m.SetLongMessageDataWithEncoding(input, enc))
		parts, err := m.Split()
		if len(input) <= capacity(originalOctets) {
			require.NoError(t, err)
			require.Len(t, parts, 1)
			require.Same(t, &m, parts[0])
			require.Equal(t, input, parts[0].messageData)
			require.Equal(t, header, parts[0].UDH())
			return
		}
		limit := capacity(rebuiltOctets)
		if limit <= 0 {
			require.ErrorIs(t, err, smpperrors.ErrUDHTooLong)
			require.Nil(t, parts)
			return
		}
		if encodingChoice%6 >= 2 {
			codec := enc
			if codec == nil {
				codec = data.DefaultGSM7()
			}
			text, decodeErr := codec.Decode(input)
			if decodeErr != nil {
				require.EqualError(t, err, decodeErr.Error())
				require.Nil(t, parts)
				return
			}
			roundTrip, encodeErr := codec.Encode(text)
			if encodeErr != nil || !bytes.Equal(input, roundTrip) {
				require.ErrorContains(t, err, "cannot be resegmented without changing encoded content")
				require.Nil(t, parts)
				return
			}
		}
		// Count whole encoded characters without calling the splitter. GSM
		// extensions occupy two septets and cannot cross a part boundary
		// (TS 23.038 §6.2.1.1); all other fuzz choices occupy one octet.
		var sizes []int
		used := 0
		for i := 0; i < len(input); {
			width := 1
			if gsm && input[i] == 0x1b {
				width = 2
			}
			if width > limit {
				require.ErrorContains(t, err, "cannot fit a GSM character")
				require.Nil(t, parts)
				return
			}
			if used+width > limit {
				sizes = append(sizes, used)
				used = 0
			}
			used += width
			i += width
		}
		sizes = append(sizes, used)
		if len(sizes) > 255 {
			require.EqualError(t, err, fmt.Sprintf("message requires %d segments, maximum is 255", len(sizes)))
			require.Nil(t, parts)
			return
		}
		require.NoError(t, err)
		require.Len(t, parts, len(sizes))
		var joined []byte
		for i, part := range parts {
			require.NotSame(t, &m, part)
			require.Len(t, part.messageData, sizes[i])
			encodedHeader, err := part.UDH().MarshalBinary()
			require.NoError(t, err)
			require.Len(t, encodedHeader, rebuiltOctets)
			joined = append(joined, part.messageData...)
		}
		require.True(t, bytes.Equal(input, joined))
	})
}
