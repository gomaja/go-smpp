package data

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/gomaja/go-sms/encoding/gsm7"
)

func TestGSM7PackedSecondCRPadding(t *testing.T) {
	// TS 23.038 V19.0.0 §6.1.2.3.1 requires another CR and a zero bit
	// when the wanted terminal CR ends on an octet boundary.
	for _, count := range []int{8, 16, 24, 160} {
		text := strings.Repeat("A", count-1) + "\r"
		septets := append(bytes.Repeat([]byte{0x41}, count-1), 0x0d)
		want := append(referenceGSM7Pack(septets, 0), 0x0d)
		raw, err := GSM7BITPACKED.Encode(text)
		if err != nil || !bytes.Equal(raw, want) {
			t.Errorf("count=%d raw=%x want=%x err=%v", count, raw, want, err)
		}
		decoded, err := GSM7BITPACKED.Decode(want)
		if err != nil || decoded != text+"\r" {
			t.Errorf("count=%d got=%q want=%q err=%v", count, decoded, text+"\r", err)
		}
		got, err := DecodeGSM7Packed(want, 0, -1)
		if err != nil || got != text+"\r" {
			t.Errorf("count=%d inferred=%q want=%q err=%v", count, got, text+"\r", err)
		}
		got, err = DecodeGSM7Packed(want, 0, count)
		if err != nil || got != text {
			t.Errorf("count=%d counted=%q want=%q err=%v", count, got, text, err)
		}
	}
}

func TestGSM7PackedSecondCRPaddingWithFill(t *testing.T) {
	for fill := uint8(0); fill <= 6; fill++ {
		for count := 1; count <= 24; count++ {
			if (int(fill)+count*7)%8 != 0 {
				continue
			}
			septets := append(bytes.Repeat([]byte{0x41}, count-1), 0x0d)
			want := append(referenceGSM7Pack(septets, fill), 0x0d)
			raw, packErr := gsm7.Pack7BitCR(septets, int(fill))
			if packErr != nil {
				t.Fatal(packErr)
			}
			if !bytes.Equal(raw, want) {
				t.Errorf("fill=%d count=%d raw=%x want=%x", fill, count, raw, want)
			}
			got, err := DecodeGSM7Packed(want, fill, -1)
			if err != nil || got != strings.Repeat("A", count-1)+"\r\r" {
				t.Errorf("fill=%d count=%d decoded=%q err=%v", fill, count, got, err)
			}
		}
	}
}

func TestGSM7PackedTerminalCRNeighbors(t *testing.T) {
	for _, count := range []int{1, 7, 9, 15, 17} {
		text := strings.Repeat("A", count-1) + "\r"
		raw, err := GSM7BITPACKED.Encode(text)
		if err != nil {
			t.Fatal(err)
		}
		got, err := GSM7BITPACKED.Decode(raw)
		if err != nil || got != text {
			t.Errorf("count=%d transform=%q want=%q err=%v", count, got, text, err)
		}
		got, err = DecodeGSM7Packed(raw, 0, -1)
		if err != nil || got != text {
			t.Errorf("count=%d inferred=%q want=%q err=%v", count, got, text+"\r", err)
		}
	}
}

func TestGSM7PackedSecondCRPaddingBudgets(t *testing.T) {
	text := "AAAAAAA\r"
	for _, enc := range []Encoding{GSM7BITPACKED} {
		if !enc.(Splitter).ShouldSplit(text, 7) || enc.(Splitter).ShouldSplit(text, 8) {
			t.Error("eight septets ending CR require eight octets including duplicate CR")
		}
	}
	text = strings.Repeat("A", 152) + "\r"
	parts, err := GSM7BITPACKED.(Splitter).EncodeSplit(text, 134)
	if err != nil || len(parts) != 2 {
		t.Fatalf("parts=%d err=%v", len(parts), err)
	}
	var joined strings.Builder
	for _, part := range parts {
		if len(part) > 134 {
			t.Errorf("segment has %d octets", len(part))
		}
		got, err := DecodeGSM7Packed(part, 1, -1)
		if err != nil {
			t.Fatal(err)
		}
		joined.WriteString(got)
	}
	// The final one-septet part ends at an octet boundary after fill one;
	// the dependency retains its duplicated CR on inferred decoding.
	if joined.String() != text+"\r" {
		t.Errorf("decoded segments=%q want=%q", joined.String(), text+"\r")
	}
}

func TestGSM7PackedInferredPaddingAfterEscape(t *testing.T) {
	// Inference applies the CR profile before alphabet conversion. A final
	// full-boundary CR is removed even after ESC; a zero septet is retained.
	// Explicit counts retain both values for TS 23.038 section 6.2.1.1 fallback.
	for fill := uint8(0); fill <= 6; fill++ {
		for _, tail := range []byte{0, 0x0d} {
			for _, escapeCount := range []int{1, 3} {
				count := 8 + int(fill)
				if count <= escapeCount {
					continue
				}
				septets := bytes.Repeat([]byte{0x41}, count-escapeCount-1)
				septets = append(septets, bytes.Repeat([]byte{escapeSequence}, escapeCount)...)
				septets = append(septets, tail)
				content := septets
				if tail == 0x0d {
					content = content[:len(content)-1]
				}
				want, err := GSM7BIT.Decode(content)
				if err != nil {
					t.Fatal(err)
				}
				raw := referenceGSM7Pack(septets, fill)
				got, err := DecodeGSM7Packed(raw, fill, -1)
				if err != nil || got != want {
					t.Errorf("fill=%d septets=%x got=%q want=%q err=%v", fill, septets, got, want, err)
				}
			}
		}
	}
}

func TestGSM7PackedIgnoresUDHFillBits(t *testing.T) {
	// TS 23.040 V19.0.0 §9.2.3.24: these bits align the payload; their
	// values are not characters and do not affect septet decoding.
	septets := []byte{0x41, 0x1b, 0x65, 0x42}
	for fill := uint8(1); fill <= 6; fill++ {
		for low := byte(0); low < byte(1<<fill); low++ {
			raw := referenceGSM7Pack(septets, fill)
			raw[0] |= low
			for _, count := range []int{-1, len(septets)} {
				got, err := DecodeGSM7Packed(raw, fill, count)
				if err != nil || got != "A€B" {
					t.Errorf("fill=%d low=%02x count=%d got=%q err=%v", fill, low, count, got, err)
				}
			}
		}
	}
}

func TestFromDataCodingUnsupportedSMPPAlphabets(t *testing.T) {
	// SMPP v3.4 Issue 1.2 §5.2.19 defines these named alphabets; none
	// may be silently treated as the GSM default alphabet or split as bytes.
	for _, code := range []byte{0x05, 0x09, 0x0a, 0x0d, 0x0e} {
		t.Run(fmt.Sprintf("%02x", code), func(t *testing.T) {
			enc := FromDataCoding(code)
			if enc.DataCoding() != code {
				t.Errorf("coding=%02x want=%02x", enc.DataCoding(), code)
			}
			if _, ok := enc.(Splitter); ok {
				t.Error("unsupported alphabet advertises a text splitter")
			}
			if _, ok := enc.(interface{ BaseEncoding() EncDec }); ok {
				t.Error("unsupported alphabet advertises an unrelated base codec")
			}
			if raw, err := enc.Encode("ABC"); !errors.Is(err, ErrNotImplEncode) {
				t.Errorf("Encode=%x err=%v", raw, err)
			}
			if text, err := enc.Decode([]byte("ABC")); !errors.Is(err, ErrNotImplDecode) {
				t.Errorf("Decode=%q err=%v", text, err)
			}
		})
	}
}

func TestGSM7PackedNonzeroFinalPadBit(t *testing.T) {
	// CR inference follows the dependency's septets for both entry points;
	// an unused high bit does not change the retained doubled CR.
	for _, last := range []byte{0x0d, 0x8d} {
		wire := []byte{0xc1, 0x60, 0x30, 0x18, 0x0c, 0x06, 0x1b, last}
		got, err := GSM7BITPACKED.Decode(wire)
		if err != nil || got != "AAAAAAA\r\r" {
			t.Errorf("standalone %x=%q,%v", wire, got, err)
		}
		got, err = DecodeGSM7Packed(wire, 0, -1)
		if err != nil || got != "AAAAAAA\r\r" {
			t.Errorf("fill/count helper %x=%q,%v", wire, got, err)
		}
	}
}
