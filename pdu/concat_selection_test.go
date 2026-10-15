package pdu_test

import (
	"strings"
	"testing"

	"github.com/gomaja/go-smpp/data"
	"github.com/gomaja/go-smpp/pdu"
	"github.com/stretchr/testify/require"
)

// TS 23.040 §§9.2.3.24, 9.2.3.24.1, 9.2.3.24.8: ignore invalid
// concatenation IEs, then use the last applicable 8-bit or 16-bit IE.
func TestConcatSelection(t *testing.T) {
	for _, tc := range []struct {
		name   string
		header pdu.UDH
		wire   []byte // Complete UDH, including UDHL; independent of MarshalBinary.
		total  byte
		seq    byte
		ref    uint16
		found  bool
	}{
		{"total_zero", pdu.UDH{{ID: 0, Data: []byte{7, 0, 1}}}, []byte{5, 0, 3, 7, 0, 1}, 0, 0, 0, false},
		{"sequence_zero", pdu.UDH{{ID: 8, Data: []byte{0x12, 0x34, 2, 0}}}, []byte{6, 8, 4, 0x12, 0x34, 2, 0}, 0, 0, 0, false},
		{"sequence_over_total", pdu.UDH{{ID: 0, Data: []byte{7, 2, 3}}}, []byte{5, 0, 3, 7, 2, 3}, 0, 0, 0, false},
		{"short_final_ie", pdu.UDH{pdu.NewIEConcatMessage(2, 1, 7), {ID: 8, Data: []byte{0x12}}}, []byte{8, 0, 3, 7, 2, 1, 8, 1, 0x12}, 2, 1, 7, true},
		{"invalid_final_ie", pdu.UDH{pdu.NewIEConcatMessage16Bit(2, 1, 0x1234), pdu.NewIEConcatMessage(0, 0, 7)}, []byte{11, 8, 4, 0x12, 0x34, 2, 1, 0, 3, 7, 0, 0}, 2, 1, 0x1234, true},
		{"valid_8bit", pdu.UDH{pdu.NewIEConcatMessage(2, 1, 0xfe)}, []byte{5, 0, 3, 0xfe, 2, 1}, 2, 1, 0xfe, true},
		{"valid_16bit", pdu.UDH{pdu.NewIEConcatMessage16Bit(2, 2, 0xfedc)}, []byte{6, 8, 4, 0xfe, 0xdc, 2, 2}, 2, 2, 0xfedc, true},
		{"zero_reference", pdu.UDH{pdu.NewIEConcatMessage(1, 1, 0)}, []byte{5, 0, 3, 0, 1, 1}, 1, 1, 0, true},
		{"maximum_counters", pdu.UDH{pdu.NewIEConcatMessage16Bit(255, 255, 65535)}, []byte{6, 8, 4, 255, 255, 255, 255}, 255, 255, 65535, true},
		{"last_8bit", pdu.UDH{pdu.NewIEConcatMessage(2, 1, 7), pdu.NewIEConcatMessage(3, 2, 9)}, []byte{10, 0, 3, 7, 2, 1, 0, 3, 9, 3, 2}, 3, 2, 9, true},
		{"last_16bit", pdu.UDH{pdu.NewIEConcatMessage16Bit(2, 1, 7), pdu.NewIEConcatMessage16Bit(3, 2, 0x1234)}, []byte{12, 8, 4, 0, 7, 2, 1, 8, 4, 0x12, 0x34, 3, 2}, 3, 2, 0x1234, true},
		{"mixed_last_16bit", pdu.UDH{pdu.NewIEConcatMessage(2, 1, 7), pdu.NewIEConcatMessage16Bit(3, 2, 0x1234)}, []byte{11, 0, 3, 7, 2, 1, 8, 4, 0x12, 0x34, 3, 2}, 3, 2, 0x1234, true},
		{"mixed_last_8bit", pdu.UDH{pdu.NewIEConcatMessage16Bit(2, 1, 0x1234), pdu.NewIEConcatMessage(3, 2, 9)}, []byte{11, 8, 4, 0x12, 0x34, 2, 1, 0, 3, 9, 3, 2}, 3, 2, 9, true},
		{"long_final_ie", pdu.UDH{pdu.NewIEConcatMessage16Bit(2, 1, 0x1234), {ID: 0, Data: []byte{7, 2, 1, 0}}}, []byte{12, 8, 4, 0x12, 0x34, 2, 1, 0, 4, 7, 2, 1, 0}, 2, 1, 0x1234, true},
		{"interleaved_8bit", pdu.UDH{{ID: 0x70, Data: []byte{0xaa}}, pdu.NewIEConcatMessage16Bit(3, 2, 0x5678), {ID: 0x71}, pdu.NewIEConcatMessage(2, 1, 7), {ID: 0x72, Data: []byte{0}}, {ID: 8, Data: []byte{0x12}}, {ID: 0x73}}, []byte{24, 0x70, 1, 0xaa, 8, 4, 0x56, 0x78, 3, 2, 0x71, 0, 0, 3, 7, 2, 1, 0x72, 1, 0, 8, 1, 0x12, 0x73, 0}, 2, 1, 7, true},
		{"interleaved_16bit", pdu.UDH{{ID: 0x70, Data: []byte{0xaa}}, pdu.NewIEConcatMessage(3, 2, 9), {ID: 0x71}, pdu.NewIEConcatMessage16Bit(2, 1, 0x1234), {ID: 0x72, Data: []byte{0}}, {ID: 0, Data: []byte{7}}, {ID: 0x73}}, []byte{24, 0x70, 1, 0xaa, 0, 3, 9, 3, 2, 0x71, 0, 8, 4, 0x12, 0x34, 2, 1, 0x72, 1, 0, 0, 1, 7, 0x73, 0}, 2, 1, 0x1234, true},
		{"malformed_before_8bit", pdu.UDH{{ID: 0x70}, {ID: 8, Data: []byte{0x12}}, {ID: 0x71}, pdu.NewIEConcatMessage(2, 1, 7), {ID: 0x72}}, []byte{14, 0x70, 0, 8, 1, 0x12, 0x71, 0, 0, 3, 7, 2, 1, 0x72, 0}, 2, 1, 7, true},
		{"malformed_before_16bit", pdu.UDH{{ID: 0x70}, {ID: 0, Data: []byte{7}}, {ID: 0x71}, pdu.NewIEConcatMessage16Bit(2, 1, 0x1234), {ID: 0x72}}, []byte{15, 0x70, 0, 0, 1, 7, 0x71, 0, 8, 4, 0x12, 0x34, 2, 1, 0x72, 0}, 2, 1, 0x1234, true},
		{"no_concat", pdu.UDH{{ID: 0x70}}, []byte{2, 0x70, 0}, 0, 0, 0, false},
		{"nil", nil, nil, 0, 0, 0, false},
		{"empty", pdu.UDH{}, nil, 0, 0, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			check := func(t *testing.T, header pdu.UDH) {
				t.Helper()
				total, seq, ref, found := header.GetConcatInfo()
				require.Equal(t, tc.found, found)
				require.Equal(t, tc.total, total)
				require.Equal(t, tc.seq, seq)
				require.Equal(t, tc.ref, ref)
			}
			t.Run("constructed", func(t *testing.T) { check(t, tc.header) })
			t.Run("received", func(t *testing.T) {
				// Received short_message: data_coding, default_msg_id, sm_length,
				// UDH and a literal GSM7 payload. UDHI comes from esm_class.
				wire := append([]byte{0, 0, byte(len(tc.wire) + 1)}, tc.wire...)
				wire = append(wire, 'A')
				var message pdu.ShortMessage
				require.NoError(t, message.Unmarshal(pdu.NewBuffer(wire), len(tc.wire) != 0))
				check(t, message.UDH())
				text, err := message.GetMessage()
				require.NoError(t, err)
				require.Equal(t, "A", text)
			})
		})
	}
}

func TestSelectedPoundFitsOneSegment(t *testing.T) {
	text := strings.Repeat("£", 80)
	parts, err := pdu.NewLongMessageWithEncoding(text, data.FindEncoding(text))
	require.NoError(t, err)
	require.Len(t, parts, 1)
	require.Equal(t, data.GSM7BIT, parts[0].Encoding())
	payload, err := parts[0].GetMessageData()
	require.NoError(t, err)
	require.Equal(t, strings.Repeat("\x01", 80), string(payload))
	decoded, err := parts[0].GetMessage()
	require.NoError(t, err)
	require.Equal(t, text, decoded)

	// The former ASCII-based choice used UCS2: 160 octets exceed one SMS.
	previous, err := pdu.NewLongMessageWithEncoding(text, data.UCS2)
	require.NoError(t, err)
	require.Len(t, previous, 2)
}

func TestFindEncodingReplacement(t *testing.T) {
	saved := data.GSM7BIT
	t.Cleanup(func() { data.GSM7BIT = saved })
	data.GSM7BIT = data.ASCII

	enc := data.FindEncoding("£")
	require.Equal(t, data.UCS2, enc)
	encoded, err := enc.Encode("£")
	require.NoError(t, err)
	require.Equal(t, []byte{0, 0xa3}, encoded)
	text := strings.Repeat("£", 80)
	parts, err := pdu.NewLongMessageWithEncoding(text, enc)
	require.NoError(t, err)
	require.Len(t, parts, 2)
	var joined string
	for _, part := range parts {
		decoded, err := part.GetMessage()
		require.NoError(t, err)
		joined += decoded
	}
	require.Equal(t, text, joined)

	// A replacement must accept the input, but cannot widen the default repertoire.
	require.Equal(t, data.ASCII, data.FindEncoding("ASCII"))
	require.Equal(t, data.UCS2, data.FindEncoding("`"))
}

func TestSelectedExtensionSegmentBudgets(t *testing.T) {
	for _, tc := range []struct {
		name  string
		text  string
		parts int
	}{
		{"default_160", strings.Repeat("Ä", 160), 1},
		{"default_161", strings.Repeat("Ä", 161), 2},
		{"mixed_160_septets", "A" + strings.Repeat("€", 79) + "Z", 1},
		{"mixed_162_septets", "A" + strings.Repeat("€", 80) + "Z", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			enc := data.FindEncoding(tc.text)
			require.Equal(t, data.GSM7BIT, enc)
			parts, err := pdu.NewLongMessageWithEncoding(tc.text, enc)
			require.NoError(t, err)
			require.Len(t, parts, tc.parts)
			var joined string
			for _, part := range parts {
				decoded, err := part.GetMessage()
				require.NoError(t, err)
				joined += decoded
			}
			require.Equal(t, tc.text, joined)
		})
	}
}
