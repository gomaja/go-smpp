package pdu

import (
	"bytes"
	"errors"
	"reflect"
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
	for _, enc := range []data.Encoding{data.GSM7BIT, data.BINARY8BIT1, data.BINARY8BIT2} {
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
