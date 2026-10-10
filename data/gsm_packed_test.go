package data

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"golang.org/x/text/transform"
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
		for _, streaming := range []bool{false, true} {
			var got []byte
			if streaming {
				got, err = io.ReadAll(GSM7(true).NewDecoder().Reader(&singleByteReader{src: want}))
			} else {
				got, err = GSM7(true).NewDecoder().Bytes(want)
			}
			if err != nil || string(got) != text {
				t.Errorf("count=%d streaming=%t got=%q want=%q err=%v", count, streaming, got, text, err)
			}
		}
		got, err := DecodeGSM7Packed(want, 0, -1)
		if err != nil || got != text {
			t.Errorf("count=%d inferred=%q want=%q err=%v", count, got, text, err)
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
			raw := packGSM7WithFill(septets, fill)
			if !bytes.Equal(raw, want) {
				t.Errorf("fill=%d count=%d raw=%x want=%x", fill, count, raw, want)
			}
			got, err := DecodeGSM7Packed(want, fill, -1)
			if err != nil || got != strings.Repeat("A", count-1)+"\r" {
				t.Errorf("fill=%d count=%d decoded=%q err=%v", fill, count, got, err)
			}
		}
	}
}

func TestGSM7PackedSecondCRPaddingShortDestination(t *testing.T) {
	encoder := GSM7(true).NewEncoder()
	first := make([]byte, 7)
	nDst, nSrc, err := encoder.Transform(first, []byte("AAAAAAA\r"), true)
	if nDst != 7 || nSrc != 8 || !errors.Is(err, transform.ErrShortDst) {
		t.Fatalf("first transform=(%d,%d,%v)", nDst, nSrc, err)
	}
	last := make([]byte, 1)
	nDst, nSrc, err = encoder.Transform(last, nil, true)
	if nDst != 1 || nSrc != 0 || err != nil || last[0] != 0x0d {
		t.Fatalf("padding transform=(%d,%d,%v) dst=%x", nDst, nSrc, err, last)
	}
	nDst, nSrc, err = encoder.Transform(last, nil, true)
	if nDst != 0 || nSrc != 0 || err != nil {
		t.Fatalf("repeated EOF=(%d,%d,%v)", nDst, nSrc, err)
	}
	encoder.Reset()
	raw, err := encoder.Bytes([]byte("AAAAAA\r"))
	if err != nil {
		t.Fatal(err)
	}
	nDst, nSrc, err = encoder.Transform(last, nil, true)
	if nDst != 0 || nSrc != 0 || err != nil {
		t.Fatalf("repeated EOF after partial-octet CR=(%d,%d,%v) raw=%x", nDst, nSrc, err, raw)
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
			t.Errorf("count=%d inferred=%q want=%q err=%v", count, got, text, err)
		}
	}
}

func TestGSM7PackedSecondCRPaddingBudgets(t *testing.T) {
	text := "AAAAAAA\r"
	for _, enc := range []Encoding{GSM7BITPACKED, &gsm7bit{packed: true}} {
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
	if joined.String() != text {
		t.Errorf("decoded segments=%q want=%q", joined.String(), text)
	}
}

func TestGSM7PackedInferredPaddingAfterEscape(t *testing.T) {
	// TS 23.038 §6.2.1.1: unassigned extensions fall back to the main
	// alphabet. A terminal escaped @ or CR is content, not padding.
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
				want, err := GSM7BIT.Decode(septets)
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
