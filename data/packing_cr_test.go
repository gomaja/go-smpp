package data

import (
	"bytes"
	"strings"
	"testing"
)

// Literal payloads distinguish CR inference from the previous zero/duplicate
// heuristics. TS 23.038 V20.0.0 section 6.1.2.3.1 supplies the CR convention;
// explicit septet counts preserve data independently of that convention.
func TestPackedCRDependencyPolicy(t *testing.T) {
	configured, err := NewGSM7(GSM7Options{Packed: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name              string
		wire              []byte
		inferred, counted string
		count             int
	}{
		{"zero spare retained as at", []byte{0xc1, 0x60, 0x30, 0x18, 0x0c, 0x06, 0x01}, "AAAAAAA@", "AAAAAAA", 7},
		{"literal at retained", []byte{0xc1, 0x60, 0x30, 0x18, 0x0c, 0x06, 0x01}, "AAAAAAA@", "AAAAAAA@", 8},
		{"full boundary CR removed", []byte{0xc1, 0x60, 0x30, 0x18, 0x0c, 0x06, 0x1b}, "AAAAAAA", "AAAAAAA\r", 8},
		{"doubled CR retained", []byte{0xc1, 0x60, 0x30, 0x18, 0x0c, 0x06, 0x1b, 0x0d}, "AAAAAAA\r\r", "AAAAAAA\r", 8},
		{"doubled CR high bit ignored", []byte{0xc1, 0x60, 0x30, 0x18, 0x0c, 0x06, 0x1b, 0x8d}, "AAAAAAA\r\r", "AAAAAAA\r", 8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, codec := range []Encoding{GSM7BITPACKED, configured} {
				got, err := codec.Decode(tc.wire)
				if err != nil || got != tc.inferred {
					t.Errorf("%T decode=%q,%v; want %q", codec, got, err, tc.inferred)
				}
			}
			got, err := DecodeGSM7Packed(tc.wire, 0, -1)
			if err != nil || got != tc.inferred {
				t.Errorf("inferred helper=%q,%v; want %q", got, err, tc.inferred)
			}
			got, err = DecodeGSM7Packed(tc.wire, 0, tc.count)
			if err != nil || got != tc.counted {
				t.Errorf("counted helper=%q,%v; want %q", got, err, tc.counted)
			}
		})
	}
}

func TestPackedCRExplicitCountsAfterEscape(t *testing.T) {
	for fill := uint8(0); fill <= 6; fill++ {
		count := 8 + int(fill)
		septets := append(bytes.Repeat([]byte{0x41}, count-2), escapeSequence, 0x0d)
		wire := referenceGSM7Pack(septets, fill)
		prefix := strings.Repeat("A", count-2)
		inferred, err := DecodeGSM7Packed(wire, fill, -1)
		if err != nil || inferred != prefix+" " {
			t.Errorf("fill=%d CR inference=%q,%v; want prefix plus lone-escape space", fill, inferred, err)
		}
		exact, err := DecodeGSM7Packed(wire, fill, count)
		if err != nil || exact != prefix+"\r" {
			t.Errorf("fill=%d counted=%q,%v; want escaped CR", fill, exact, err)
		}
	}
}

func TestPackedCRCountValidation(t *testing.T) {
	for _, tc := range []struct {
		wire  []byte
		fill  uint8
		count int
	}{
		{[]byte{0x41}, 0, 2}, {[]byte{0x41}, 6, 1}, {nil, 0, 1},
		{[]byte{0x41}, 0, int(^uint(0) >> 1)},
		{[]byte{0x41}, 7, -1}, {nil, 255, -1}, {[]byte{0x41}, 0, -2},
	} {
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Errorf("invalid fill=%d count=%d panicked: %v", tc.fill, tc.count, recovered)
				}
			}()
			got, err := DecodeGSM7Packed(tc.wire, tc.fill, tc.count)
			if err == nil || got != "" {
				t.Errorf("invalid fill=%d count=%d accepted: %q,%v", tc.fill, tc.count, got, err)
			}
		}()
	}
}

func TestPackedCRZeroCount(t *testing.T) {
	for _, tc := range []struct {
		wire []byte
		fill uint8
	}{
		{nil, 0}, {[]byte{0x41}, 0}, {[]byte{0xff}, 6},
	} {
		got, err := DecodeGSM7Packed(tc.wire, tc.fill, 0)
		if err != nil || got != "" {
			t.Errorf("zero count fill=%d wire=%x got=%q,%v", tc.fill, tc.wire, got, err)
		}
	}
}
