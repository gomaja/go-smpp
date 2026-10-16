package pdu

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/gomaja/go-smpp/data"
	smpperrors "github.com/gomaja/go-smpp/errors"
	"github.com/gomaja/go-sms/encoding/gsm7"
	"github.com/gomaja/go-sms/encoding/tpdu"
	smsucs2 "github.com/gomaja/go-sms/encoding/ucs2"
)

func TestStandaloneIECopiedAndTransactional(t *testing.T) {
	for length := 0; length <= 255; length++ {
		for size := 0; size <= 259; size++ {
			src := bytes.Repeat([]byte{0x41}, size)
			if size > 0 {
				src[0] = 0x70
			}
			if size > 1 {
				src[1] = byte(length)
			}
			ie := InfoElement{ID: 0x71, Data: []byte{0x99}}
			n, err := ie.UnmarshalBinary(src)
			if size < length+2 {
				if n != 0 || err == nil || !reflect.DeepEqual(ie, InfoElement{ID: 0x71, Data: []byte{0x99}}) {
					t.Fatalf("length=%d size=%d: n=%d ie=%+v err=%v", length, size, n, ie, err)
				}
				if !errors.Is(err, tpdu.ErrUnderflow) {
					t.Fatalf("underflow identity: %v", err)
				}
				continue
			}
			if err != nil || n != length+2 || ie.ID != 0x70 || !bytes.Equal(ie.Data, src[2:n]) {
				t.Fatalf("length=%d size=%d: n=%d ie=%+v err=%v", length, size, n, ie, err)
			}
			if length == 0 && ie.Data != nil {
				t.Fatal("empty IE data must be nil")
			}
			if length > 0 {
				src[2] = 0xff
				if ie.Data[0] != 0x41 {
					t.Fatal("IE data aliases source")
				}
			}
		}
	}
}

func TestSegmentationCompleteHeaderCapacity(t *testing.T) {
	for h := 0; h <= 140; h++ {
		for _, enc := range []data.Encoding{data.GSM7BIT, data.GSM7BITPACKED, data.UCS2, data.UCS2Strict, data.ASCII, data.BINARY8BIT1, data.BINARY8BIT2} {
			c := ShortMessage{enc: enc}
			if h > 0 {
				// Ignored bounded headers also consume their original octets.
				c.ignoredUDH = make([]byte, h)
			}
			want := 140 - h
			if enc == data.GSM7BIT {
				want = 160 - (8*h+6)/7
			}
			if got := c.payloadLimit(); got != want {
				t.Fatalf("coding=%T header=%d: limit=%d want=%d", enc, h, got, want)
			}
		}
	}
	c := ShortMessage{udHeader: UDH{{ID: 0x70, Data: make([]byte, 256)}}}
	if c.payloadLimit() != -1 {
		t.Fatal("invalid header must keep the -1 capacity sentinel")
	}
}

func TestOddRawUCS2FitsOctetCapacity(t *testing.T) {
	for _, enc := range []data.Encoding{data.UCS2, data.UCS2Strict} {
		c := ShortMessage{enc: enc, messageData: bytes.Repeat([]byte{0x41}, 133), udHeader: UDH{NewIEConcatMessage16Bit(1, 1, 0x1234)}}
		parts, err := c.split()
		if err != nil || len(parts) != 1 || parts[0] != &c {
			t.Fatalf("already fitting odd UCS2: parts=%v err=%v", parts, err)
		}
	}
}

func TestMalformedUCS2SegmentationRetainsErrors(t *testing.T) {
	for _, enc := range []data.Encoding{data.UCS2, data.UCS2Strict} {
		for _, tc := range []struct {
			payload []byte
			want    error
		}{
			{bytes.Repeat([]byte{0x41}, 141), smsucs2.ErrInvalidLength},
			{append(bytes.Repeat([]byte{0, 0x41}, 71), 0xd8, 0), smsucs2.ErrDanglingSurrogate{0xd8, 0}},
		} {
			c := ShortMessage{enc: enc, messageData: tc.payload}
			parts, err := c.split()
			matches := errors.Is(err, tc.want)
			if want, ok := tc.want.(smsucs2.ErrDanglingSurrogate); ok {
				var got smsucs2.ErrDanglingSurrogate
				matches = errors.As(err, &got) && bytes.Equal(got, want)
			}
			if parts != nil || !matches {
				t.Fatalf("%T malformed UCS2: parts=%v err=%v want=%v", enc, parts, err, tc.want)
			}
		}
	}
}

func TestRawSegmentationKeepsCallerOwnership(t *testing.T) {
	for _, tc := range []struct {
		name string
		enc  data.Encoding
	}{
		{"NLI", data.GSM7BIT},
		{"binary1", data.BINARY8BIT1},
		{"binary2", data.BINARY8BIT2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			enc := tc.enc
			src := bytes.Repeat([]byte{0x41}, 200)
			c := ShortMessage{enc: enc, messageData: src}
			if enc == data.GSM7BIT {
				c.udHeader = UDH{{ID: 0x25, Data: []byte{1}}}
			}
			parts, err := c.split()
			if err != nil || len(parts) < 2 {
				t.Fatalf("split %T: %v", enc, err)
			}
			src[0] = 0xff
			if parts[0].messageData[0] != 0x41 {
				t.Fatalf("%T segment aliases caller", enc)
			}
			parts[0].messageData[0] = 0x42
			if src[0] != 0xff || parts[1].messageData[0] != 0x41 {
				t.Fatalf("%T mutation escaped segment", enc)
			}
			wantSource := bytes.Clone(src)
			wantNext := bytes.Clone(parts[1].messageData)
			parts[0].messageData = append(parts[0].messageData, 0x58)
			parts[0].messageData[0] = 0x43
			parts[0].messageData[len(parts[0].messageData)-1] = 0x59
			if !bytes.Equal(parts[1].messageData, wantNext) || !bytes.Equal(src, wantSource) {
				t.Fatalf("%T append/write changed the next segment or caller buffer", enc)
			}
		})
	}
}

func TestPackedSplitFinalSuffixLookahead(t *testing.T) {
	// Boundaries recorded from 977ff9c. SMPP has no septet count, so both
	// the prefix and the final suffix must preserve the CR receive rule
	// (TS 23.038 §6.1.2.3.1) at the complete header's fill alignment
	// (TS 23.040 §9.2.3.24).
	for _, tc := range []struct {
		name                   string
		header                 UDH
		textLength             int
		textLengths, payloads  []int
		headerOctets, fillBits int
	}{
		{"constructor", nil, 305, []int{151, 152, 2}, []int{133, 134, 2}, 6, 1},
		{"wide", UDH{NewIEConcatMessage16Bit(2, 1, 0x1234)}, 303, []int{150, 151, 2}, []int{132, 133, 2}, 7, 0},
		{"opaque0", UDH{{ID: 0x70}, NewIEConcatMessage(2, 1, 1)}, 299, []int{148, 149, 2}, []int{131, 132, 3}, 8, 6},
		{"opaque1", UDH{{ID: 0x70, Data: []byte{0x41}}, NewIEConcatMessage(2, 1, 1)}, 297, []int{147, 148, 2}, []int{130, 131, 3}, 9, 5},
		{"opaque2", UDH{{ID: 0x70, Data: []byte{0x41, 0x42}}, NewIEConcatMessage(2, 1, 1)}, 295, []int{146, 147, 2}, []int{129, 130, 3}, 10, 4},
		{"opaque3", UDH{{ID: 0x70, Data: []byte{0x41, 0x42, 0x43}}, NewIEConcatMessage(2, 1, 1)}, 293, []int{145, 146, 2}, []int{128, 129, 3}, 11, 3},
		{"opaque4", UDH{{ID: 0x70, Data: []byte{0x41, 0x42, 0x43, 0x44}}, NewIEConcatMessage(2, 1, 1)}, 291, []int{144, 144, 3}, []int{127, 127, 3}, 12, 2},
		{"opaque-wide", UDH{{ID: 0x70}, NewIEConcatMessage16Bit(2, 1, 0x1234)}, 297, []int{147, 148, 2}, []int{130, 131, 3}, 9, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text := strings.Repeat("\r", tc.textLength)
			var parts []*ShortMessage
			var err error
			if tc.header == nil {
				parts, err = NewLongMessageWithEncoding(text, data.GSM7BITPACKED)
			} else {
				p := NewSubmitSM().(*SubmitSM)
				if err := p.Message.SetUDH(tc.header); err != nil {
					t.Fatal(err)
				}
				if err := p.Message.SetLongMessageWithEnc(text, data.GSM7BITPACKED); err != nil {
					t.Fatal(err)
				}
				var pdus []*SubmitSM
				pdus, err = p.Split()
				for _, part := range pdus {
					parts = append(parts, &part.Message)
				}
			}
			if err != nil || len(parts) != len(tc.textLengths) {
				t.Fatalf("split: got %d parts, err=%v; want %d parts", len(parts), err, len(tc.textLengths))
			}
			var joined strings.Builder
			for i, part := range parts {
				payload, err := part.GetMessageData()
				if err != nil || len(payload) != tc.payloads[i] {
					t.Fatalf("part %d payload: got %d octets, err=%v; want %d", i, len(payload), err, tc.payloads[i])
				}
				h := part.UDH().UDHL()
				if h != tc.headerOctets || packedFillBits(h) != tc.fillBits {
					t.Fatalf("part %d header: got %d octets/%d fill bits; want %d/%d", i, h, packedFillBits(h), tc.headerOctets, tc.fillBits)
				}
				wire := NewBuffer(nil)
				if err := part.Marshal(wire); err != nil {
					t.Fatal(err)
				}
				var received ShortMessage
				if err := received.Unmarshal(wire, true); err != nil {
					t.Fatal(err)
				}
				decoded, err := received.GetMessageWithEncoding(data.GSM7BITPACKED)
				if err != nil || decoded != strings.Repeat("\r", tc.textLengths[i]) {
					t.Fatalf("part %d decode: got %q, err=%v; want %d CRs", i, decoded, err, tc.textLengths[i])
				}
				joined.WriteString(decoded)
			}
			if joined.String() != text {
				t.Fatal("decoded parts do not recover the complete text")
			}
		})
	}
}

func TestRawNLISegmentationValidation(t *testing.T) {
	newMessage := func(payload []byte) ShortMessage {
		return ShortMessage{enc: data.GSM7BIT, messageData: payload, udHeader: UDH{{ID: 0x25, Data: []byte{1}}}}
	}
	c := newMessage(append(bytes.Repeat([]byte{0x41}, 160), 0x80))
	if parts, err := c.split(); err == nil || parts != nil {
		t.Fatalf("invalid septet: parts=%v err=%v", parts, err)
	} else {
		var invalid gsm7.ErrInvalidSeptet
		if !errors.As(err, &invalid) || invalid.Offset != 160 {
			t.Fatalf("invalid septet error lost: %v", err)
		}
	}
	c = newMessage(append(bytes.Repeat([]byte{0x41}, 160), 0x1b))
	if _, err := c.split(); err == nil || err.Error() != "split limit cannot fit a GSM character" {
		t.Fatalf("dangling ESC compatibility: %v", err)
	}
	c = newMessage([]byte{0x80, 0x1b})
	if parts, err := c.split(); err != nil || len(parts) != 1 || parts[0] != &c {
		t.Fatalf("already fitting malformed septets changed: %v", err)
	}
	c = newMessage(bytes.Repeat([]byte{0x41}, 160))
	c.udHeader = append(c.udHeader, InfoElement{ID: 0x70, Data: make([]byte, 132)})
	if _, err := c.split(); err != smpperrors.ErrUDHTooLong {
		t.Fatalf("overlong complete header: %v", err)
	}
}

func FuzzStandaloneIE(f *testing.F) {
	for _, raw := range [][]byte{nil, {0x70}, {0x70, 0}, {0x70, 2, 0x41}, {0x70, 1, 0x41, 0x99}} {
		f.Add(raw)
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		ie := InfoElement{ID: 0x71, Data: []byte{0x99}}
		n, err := ie.UnmarshalBinary(raw)
		if len(raw) < 2 || len(raw)-2 < int(raw[1]) {
			if n != 0 || err == nil || !reflect.DeepEqual(ie, InfoElement{ID: 0x71, Data: []byte{0x99}}) {
				t.Fatalf("truncated IE: n=%d err=%v receiver=%+v", n, err, ie)
			}
			return
		}
		if err != nil || n != 2+int(raw[1]) || ie.ID != raw[0] || !bytes.Equal(ie.Data, raw[2:n]) {
			t.Fatalf("complete IE: n=%d err=%v receiver=%+v", n, err, ie)
		}
		if n == 2 && ie.Data != nil {
			t.Fatal("empty IE data is not nil")
		}
		if n > 2 {
			original := raw[2]
			raw[2] ^= 0xff
			if ie.Data[0] != original {
				t.Fatal("decoded IE aliases input")
			}
		}
	})
}
