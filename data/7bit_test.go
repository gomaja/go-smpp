package data

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/text/transform"
)

// These GSM 7-bit tests cover 3GPP TS 23.038 sections 6.2.1 and 6.2.1.1.
// Portions of the original test cases are derived from fiorix/go-smpp; see
// LICENSES/fiorix-go-smpp-MIT.txt.

var validationStringTests = []struct {
	Text     string
	Expected []rune
}{
	{Text: "12345678", Expected: []rune{}},
	{Text: "12345[6]", Expected: []rune{}},
	{Text: "@£$¥èéùìòÇ\nØø\rÅåΔ_ΦΓΛΩΠΨΣΘΞ\f^{}\\[~]|€ÆæßÉ !\"#¤%&'()*+,-./0123456789:;<=>?¡ABCDEFGHIJKLMNOPQRSTUVWXYZÄÖÑÜ§¿abcdefghijklmnopqrstuvwxyzäöñüà", Expected: []rune{}},
	{Text: "你", Expected: []rune{'你'}},
}

var validationBufferTests = []struct {
	Buffer   []byte
	Expected []byte
}{
	{Buffer: []byte{0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37, 0x38}, Expected: []byte{}},
	{Buffer: []byte{0x31, 0x32, 0x33, 0x34, 0x35, 0x1B, 0x3C, 0x36, 0x1B, 0x3E}, Expected: []byte{}},
	{Buffer: []byte{0x31, 0x32, 0x33, 0x34, 0x35, 0x1B}, Expected: []byte{0x1B}},
	{Buffer: []byte{0x31, 0x32, 0x33, 0x34, 0x35, 0x1B, 0x00}, Expected: []byte{}},
	{Buffer: []byte{0x80, 0x81, 0x82, 0x83}, Expected: []byte{0x80, 0x81, 0x82, 0x83}},
}

var packedTests = []struct {
	Text string
	Buff []byte
}{
	{Text: "", Buff: []byte{}},
	{Text: "1", Buff: []byte{0x31}},
	{Text: "12", Buff: []byte{0x31, 0x19}},
	{Text: "123", Buff: []byte{0x31, 0xD9, 0x0C}},
	{Text: "1234", Buff: []byte{0x31, 0xD9, 0x8C, 0x06}},
	{Text: "12345", Buff: []byte{0x31, 0xD9, 0x8C, 0x56, 0x03}},
	{Text: "123456", Buff: []byte{0x31, 0xD9, 0x8C, 0x56, 0xB3, 0x01}},
	{Text: "1234567", Buff: []byte{0x31, 0xD9, 0x8C, 0x56, 0xB3, 0xDD, 0x1A}},
	{Text: "12345678", Buff: []byte{0x31, 0xD9, 0x8C, 0x56, 0xB3, 0xDD, 0x70}},
	{Text: "123456789", Buff: []byte{0x31, 0xD9, 0x8C, 0x56, 0xB3, 0xDD, 0x70, 0x39}},
	{Text: "Lorem ipsum dolor sit amet, consectetur adipiscing elit. Curabitur nec nunc venenatis, ultricies ipsum id, volutpat ante. Sed pretium ac metus a interdum metus.", Buff: []byte("\xCC\xB7\xBC\xDC\x06\xA5\xE1\xF3\x7A\x1B\x44\x7E\xB3\xDF\x72\xD0\x3C\x4D\x07\x85\xDB\x65\x3A\x0B\x34\x7E\xBB\xE7\xE5\x31\xBD\x4C\xAF\xCB\x41\x61\x72\x1A\x9E\x9E\x8F\xD3\xEE\x33\xA8\xCC\x4E\xD3\x5D\xA0\x61\x5D\x1E\x16\xA7\xE9\x75\x39\xC8\x5D\x1E\x83\xDC\x75\xF7\x18\x64\x2F\xBB\xCB\xEE\x30\x3D\x3D\x67\x81\xEA\x6C\xBA\x3C\x3D\x4E\x97\xE7\xA0\x34\x7C\x5E\x6F\x83\xD2\x64\x16\xC8\xFE\x66\xD7\xE9\xF0\x30\x1D\x14\x76\xD3\xCB\x2E\xD0\xB4\x4C\x06\xC1\xE5\x65\x7A\xBA\xDE\x06\x85\xC7\xA0\x76\x99\x5E\x9F\x83\xC2\xA0\xB4\x9B\x5E\x96\x93\xEB\x6D\x50\xBB\x4C\xAF\xCF\x5D")},
	{Text: "\n", Buff: []byte{0x0A}},
	{Text: "\r", Buff: []byte{0x0D}},
	{Text: "\f", Buff: []byte{0x1B, 0x05}},
	{Text: "^{}\\[~]|€", Buff: []byte{0x1B, 0xCA, 0x06, 0xB5, 0x49, 0x6D, 0x5E, 0x1B, 0xDE, 0xA6, 0xB7, 0xF1, 0x6D, 0x80, 0x9B, 0x32}},
	{Text: "@£$¥èéùìòÇØøÅåΔ_ΦΓΛΩΠΨΣΘΞÆæßÉ !\"#¤%&'()*+,-./0123456789:;<=>?¡ABCDEFGHIJKLMNOPQRSTUVWXYZÄÖÑÜ§¿abcdefghijklmnopqrstuvwxyzäöñüà", Buff: []byte("\x80\x80\x60\x40\x28\x18\x0E\x88\xC4\x82\xE1\x78\x40\x22\x92\x09\xA5\x62\xB9\x60\x32\x1A\x4E\xC7\xF3\x01\x85\x44\x23\x52\xC9\x74\x42\xA5\x54\x2B\x56\xCB\xF5\x82\xC5\x64\x33\x5A\xCD\x76\xC3\xE5\x74\x3B\x5E\xCF\xF7\x03\x06\x85\x43\x62\xD1\x78\x44\x26\x95\x4B\x66\xD3\xF9\x84\x46\xA5\x53\x6A\xD5\x7A\xC5\x66\xB5\x5B\x6E\xD7\xFB\x05\x87\xC5\x63\x72\xD9\x7C\x46\xA7\xD5\x6B\x76\xDB\xFD\x86\xC7\xE5\x73\x7A\xDD\x7E\xC7\xE7\xF5\x7B\x7E\xDF\xFF\x07")},
}

var unpackedTests = []struct {
	Text string
	Buff []byte
}{
	{Text: "", Buff: []byte{}},
	{Text: "1", Buff: []byte{0x31}},
	{Text: "12", Buff: []byte{0x31, 0x32}},
	{Text: "123", Buff: []byte{0x31, 0x32, 0x33}},
	{Text: "1234", Buff: []byte{0x31, 0x32, 0x33, 0x34}},
	{Text: "12345", Buff: []byte{0x31, 0x32, 0x33, 0x34, 0x35}},
	{Text: "123456", Buff: []byte{0x31, 0x32, 0x33, 0x34, 0x35, 0x36}},
	{Text: "1234567", Buff: []byte{0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37}},
	{Text: "12345678", Buff: []byte{0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37, 0x38}},
	{Text: "123456789", Buff: []byte{0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37, 0x38, 0x39}},
	{Text: "12345[6", Buff: []byte{0x31, 0x32, 0x33, 0x34, 0x35, 0x1B, 0x3C, 0x36}},
	{Text: "Lorem ipsum dolor sit amet, consectetur adipiscing elit. Curabitur nec nunc venenatis, ultricies ipsum id, volutpat ante. Sed pretium ac metus a interdum metus.", Buff: []byte("\x4C\x6F\x72\x65\x6D\x20\x69\x70\x73\x75\x6D\x20\x64\x6F\x6C\x6F\x72\x20\x73\x69\x74\x20\x61\x6D\x65\x74\x2C\x20\x63\x6F\x6E\x73\x65\x63\x74\x65\x74\x75\x72\x20\x61\x64\x69\x70\x69\x73\x63\x69\x6E\x67\x20\x65\x6C\x69\x74\x2E\x20\x43\x75\x72\x61\x62\x69\x74\x75\x72\x20\x6E\x65\x63\x20\x6E\x75\x6E\x63\x20\x76\x65\x6E\x65\x6E\x61\x74\x69\x73\x2C\x20\x75\x6C\x74\x72\x69\x63\x69\x65\x73\x20\x69\x70\x73\x75\x6D\x20\x69\x64\x2C\x20\x76\x6F\x6C\x75\x74\x70\x61\x74\x20\x61\x6E\x74\x65\x2E\x20\x53\x65\x64\x20\x70\x72\x65\x74\x69\x75\x6D\x20\x61\x63\x20\x6D\x65\x74\x75\x73\x20\x61\x20\x69\x6E\x74\x65\x72\x64\x75\x6D\x20\x6D\x65\x74\x75\x73\x2E")},
	{Text: "\n", Buff: []byte{0x0A}},
	{Text: "\r", Buff: []byte{0x0D}},
	{Text: "\f", Buff: []byte{0x1B, 0x0A}},
	{Text: "^{}\\[~]|€", Buff: []byte{0x1B, 0x14, 0x1B, 0x28, 0x1B, 0x29, 0x1B, 0x2F, 0x1B, 0x3C, 0x1B, 0x3D, 0x1B, 0x3E, 0x1B, 0x40, 0x1B, 0x65}},
	{Text: "@£$¥èéùìòÇØøÅåΔ_ΦΓΛΩΠΨΣΘΞÆæßÉ !\"#¤%&'()*+,-./0123456789:;<=>?¡ABCDEFGHIJKLMNOPQRSTUVWXYZÄÖÑÜ§¿abcdefghijklmnopqrstuvwxyzäöñüà", Buff: []byte("\x00\x01\x02\x03\x04\x05\x06\x07\x08\x09\x0B\x0C\x0E\x0F\x10\x11\x12\x13\x14\x15\x16\x17\x18\x19\x1A\x1C\x1D\x1E\x1F\x20\x21\x22\x23\x24\x25\x26\x27\x28\x29\x2A\x2B\x2C\x2D\x2E\x2F\x30\x31\x32\x33\x34\x35\x36\x37\x38\x39\x3A\x3B\x3C\x3D\x3E\x3F\x40\x41\x42\x43\x44\x45\x46\x47\x48\x49\x4A\x4B\x4C\x4D\x4E\x4F\x50\x51\x52\x53\x54\x55\x56\x57\x58\x59\x5A\x5B\x5C\x5D\x5E\x5F\x60\x61\x62\x63\x64\x65\x66\x67\x68\x69\x6A\x6B\x6C\x6D\x6E\x6F\x70\x71\x72\x73\x74\x75\x76\x77\x78\x79\x7A\x7B\x7C\x7D\x7E\x7F")},
}

var invalidCharacterTests = []struct {
	Packed bool
	Text   string
}{
	{Packed: true, Text: "你"},
	{Packed: false, Text: "你"},
}

var invalidByteTests = []struct {
	Packed bool
	Buff   []byte
}{
	{Packed: false, Buff: []byte{0x80}},
	{Packed: false, Buff: []byte{0x1B}},
	{Packed: false, Buff: []byte{0x1B, 0x80}},
}

func TestGSM7EncodingString(t *testing.T) {
	tests := []struct {
		Packed   bool
		Expected string
	}{
		{Packed: true, Expected: "GSM 7-bit (Packed)"},
		{Packed: false, Expected: "GSM 7-bit (Unpacked)"},
	}

	for index, row := range tests {
		actual := fmt.Sprint(GSM7(row.Packed))
		if actual != row.Expected {
			t.Fatalf("%d: expected '%s' but got '%s'", index, row.Expected, actual)
		}
	}
}

func TestValidateGSM7String(t *testing.T) {
	for index, row := range validationStringTests {
		actual := ValidateGSM7String(row.Text)
		if !reflect.DeepEqual(actual, row.Expected) {
			t.Fatalf("%2d: actual did not equal expected.\nactual: %s\nexpect: %s", index, string(actual), string(row.Expected))
		}
	}
}

func TestValidateGSM7Buffer(t *testing.T) {
	for index, row := range validationBufferTests {
		actual := ValidateGSM7Buffer(row.Buffer)
		if !reflect.DeepEqual(actual, row.Expected) {
			t.Fatalf("%2d: actual did not equal expected.\nactual: %s\nexpect: %s", index, hex.EncodeToString(actual), hex.EncodeToString(row.Expected))
		}
	}
}

func TestPackedEncoder(t *testing.T) {
	encoder := GSM7(true).NewEncoder()
	for index, row := range packedTests {
		es, _, err := transform.Bytes(encoder, []byte(row.Text))
		if err != nil {
			t.Fatalf("%2d: unexpected error: '%s'", index, err.Error())
		}
		if !reflect.DeepEqual(es, row.Buff) {
			t.Fatalf("%2d: actual did not equal expected.\nactual: %s\nexpect: %s", index, hex.EncodeToString(es), hex.EncodeToString(row.Buff))
		}
	}
}

func TestUnpackedEncoder(t *testing.T) {
	encoder := GSM7(false).NewEncoder()
	for index, row := range unpackedTests {
		es, _, err := transform.Bytes(encoder, []byte(row.Text))
		if err != nil {
			t.Fatalf("%2d: unexpected error: '%s'", index, err.Error())
		}
		if !reflect.DeepEqual(es, row.Buff) {
			t.Fatalf("%2d: actual did not equal expected.\nactual: %s\nexpect: %s", index, hex.EncodeToString(es), hex.EncodeToString(row.Buff))
		}
	}
}

func TestPackedDecoder(t *testing.T) {
	decoder := GSM7(true).NewDecoder()
	for index, row := range packedTests {
		es, _, err := transform.Bytes(decoder, row.Buff)
		if err != nil {
			t.Fatalf("%2d: unexpected error: '%s'", index, err.Error())
		}
		if string(es) != row.Text {
			t.Fatalf("%2d: actual did not equal expected.\nactual: %s\nexpect: %s", index, string(es), row.Text)
		}
	}
}

func TestUnpackedDecoder(t *testing.T) {
	encoder := GSM7(false).NewDecoder()
	for index, row := range unpackedTests {
		es, _, err := transform.Bytes(encoder, row.Buff)
		if err != nil {
			t.Fatalf("%2d: unexpected error: '%s'", index, err.Error())
		}
		if string(es) != row.Text {
			t.Fatalf("%2d: actual did not equal expected.\nactual: %s\nexpect: %s", index, string(es), row.Text)
		}
	}
}

func TestInvalidCharacter(t *testing.T) {
	for index, row := range invalidCharacterTests {
		encoder := GSM7(row.Packed).NewEncoder()
		_, _, err := transform.Bytes(encoder, []byte(row.Text))
		if err == nil {
			t.Fatalf("%2d: expected error but got no error", index)
		}
		if err != ErrInvalidCharacter {
			t.Fatalf("%2d: expected '%s' but got '%s'", index, ErrInvalidCharacter, err.Error())
		}
	}
}

func TestInvalidByte(t *testing.T) {
	for index, row := range invalidByteTests {
		decoder := GSM7(row.Packed).NewDecoder()
		_, _, err := transform.Bytes(decoder, row.Buff)
		if err == nil {
			t.Fatalf("%2d: expected error but got no error", index)
		}
		if err != ErrInvalidByte {
			t.Fatalf("%2d: expected '%s' but got '%s'", index, ErrInvalidByte, err.Error())
		}
	}
}

func TestGSM7TransformerConsumedBytes(t *testing.T) {
	for _, packed := range []bool{false, true} {
		dst := make([]byte, 100)
		_, n, err := GSM7(packed).NewEncoder().Transform(dst, []byte("é€"), true)
		if err != nil || n != len("é€") {
			t.Errorf("packed=%t encoder nSrc=%d want=%d err=%v", packed, n, len("é€"), err)
		}
		_, n, err = GSM7(packed).NewDecoder().Transform(dst, []byte{0x41}, true)
		if err != nil || n != 1 {
			t.Errorf("packed=%t decoder nSrc=%d want=1 err=%v", packed, n, err)
		}
	}
	got, err := io.ReadAll(GSM7(false).NewDecoder().Reader(strings.NewReader("ABC")))
	if err != nil || string(got) != "ABC" {
		t.Errorf("Reader=%q err=%v", got, err)
	}
}

func TestGSM7TransformerIncompleteInput(t *testing.T) {
	for _, packed := range []bool{false, true} {
		encoder := GSM7(packed).NewEncoder()
		_, n, err := encoder.Transform(make([]byte, 10), []byte{0xc3}, false)
		if n != 0 || !errors.Is(err, transform.ErrShortSrc) {
			t.Errorf("packed=%t incomplete UTF8 nSrc=%d err=%v", packed, n, err)
		}
	}
	_, n, err := GSM7(false).NewDecoder().Transform(make([]byte, 10), []byte{0x1b}, false)
	if n != 0 || !errors.Is(err, transform.ErrShortSrc) {
		t.Errorf("incomplete escape nSrc=%d err=%v", n, err)
	}
}

func TestGSM7TransformerShortDestination(t *testing.T) {
	for _, packed := range []bool{false, true} {
		encoder := GSM7(packed).NewEncoder()
		dst := make([]byte, 1)
		nDst, nSrc, err := encoder.Transform(dst, []byte("ABC€"), true)
		if !errors.Is(err, transform.ErrShortDst) || nDst != 1 || nSrc == 0 {
			t.Errorf("packed=%t short dst nDst=%d nSrc=%d err=%v", packed, nDst, nSrc, err)
		}
		decoder := GSM7(packed).NewDecoder()
		encoded, err := GSM7(packed).NewEncoder().Bytes([]byte("ABC€"))
		if err != nil {
			t.Fatal(err)
		}
		nDst, nSrc, err = decoder.Transform(dst, encoded, true)
		if !errors.Is(err, transform.ErrShortDst) || nDst != 1 || nSrc == 0 {
			t.Errorf("packed=%t decoder short dst nDst=%d nSrc=%d err=%v", packed, nDst, nSrc, err)
		}
	}
}

type singleByteReader struct{ src []byte }

func (r *singleByteReader) Read(dst []byte) (int, error) {
	if len(r.src) == 0 {
		return 0, io.EOF
	}
	dst[0] = r.src[0]
	r.src = r.src[1:]
	return 1, nil
}

func TestGSM7TransformerStreamingChunks(t *testing.T) {
	text := strings.Repeat("é€ABC{", 1000)
	for _, packed := range []bool{false, true} {
		encoded, err := io.ReadAll(transform.NewReader(&singleByteReader{src: []byte(text)}, GSM7(packed).NewEncoder()))
		if err != nil {
			t.Fatalf("packed=%t encoder: %v", packed, err)
		}
		expected, err := GSM7(packed).NewEncoder().Bytes([]byte(text))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(encoded, expected) {
			t.Fatalf("packed=%t streaming encoded differs", packed)
		}
		decoded, err := io.ReadAll(GSM7(packed).NewDecoder().Reader(&singleByteReader{src: encoded}))
		if err != nil || string(decoded) != text {
			t.Fatalf("packed=%t decoded len=%d want=%d err=%v", packed, len(decoded), len(text), err)
		}
	}
}

func FuzzGSM7RoundTrip(f *testing.F) {
	for _, text := range []string{"", "ABC", "é€", "^{}\\[~]|€", strings.Repeat("a", 152) + "€", "@£$¥èéùìòÇ", "AAAAAAA", "abcdefG", "AAAAAAA@", "AAAAAAA\r", "AAAAAAA\r\r", "AAAAAA\r"} {
		f.Add(text)
	}
	f.Fuzz(func(t *testing.T, text string) {
		if len(text) > 64*1024 {
			t.Skip()
		}
		septets, err := GSM7BIT.Encode(text)
		if err != nil {
			if len(ValidateGSM7String(text)) == 0 {
				t.Fatalf("valid alphabet rejected: %v", err)
			}
			return
		}
		decoded, err := GSM7BIT.Decode(septets)
		if err != nil || decoded != text {
			t.Fatalf("unpacked round trip: %q != %q, err=%v", decoded, text, err)
		}
		for _, packed := range []bool{false, true} {
			expectedText := text
			if packed && len(septets) > 0 && len(septets)%8 == 0 && septets[len(septets)-1] == 0 {
				// A literal eighth @ is indistinguishable from legacy zero
				// padding. Test that wire collision separately; all other
				// unambiguous texts must decode to the exact source text.
				expectedText = text[:len(text)-1]
			}
			if packed && len(septets)%8 == 1 && len(septets) > 1 &&
				septets[len(septets)-2] == 0x0d && septets[len(septets)-1] == 0x0d {
				// Two literal CRs straddling an octet boundary have the same
				// wire bytes as one CR with the mandatory duplicated padding.
				expectedText = text[:len(text)-1]
			}
			encoding := GSM7(packed)
			encoded, err := encoding.NewEncoder().Bytes([]byte(text))
			if err != nil {
				t.Fatal(err)
			}
			if packed {
				exact, err := DecodeGSM7Packed(encoded, 0, len(septets))
				if err != nil || exact != text {
					t.Fatalf("exact packed round trip: %q != %q, err=%v", exact, text, err)
				}
				inferred, err := DecodeGSM7Packed(encoded, 0, -1)
				if err != nil || inferred != expectedText {
					t.Fatalf("inferred packed round trip: %q != %q, err=%v", inferred, expectedText, err)
				}
			}
			whole, err := encoding.NewDecoder().Bytes(encoded)
			if err != nil || string(whole) != expectedText {
				t.Fatalf("packed=%t whole text=%q want=%q err=%v", packed, whole, expectedText, err)
			}
			streaming, err := io.ReadAll(transform.NewReader(&singleByteReader{src: []byte(text)}, encoding.NewEncoder()))
			if err != nil || !bytes.Equal(streaming, encoded) {
				t.Fatalf("packed=%t streaming encode mismatch, err=%v", packed, err)
			}
			actual, err := io.ReadAll(encoding.NewDecoder().Reader(&singleByteReader{src: encoded}))
			if err != nil || string(actual) != expectedText {
				t.Fatalf("packed=%t streaming text=%q want=%q err=%v", packed, actual, expectedText, err)
			}
		}
	})
}

func TestGSM7PackedPaddingRoundTrip(t *testing.T) {
	for _, count := range []int{7, 15, 23} {
		for r, septet := range forwardLookup {
			text := strings.Repeat("A", count-1) + string(r)
			encoded, err := GSM7BITPACKED.Encode(text)
			if err != nil {
				t.Fatal(err)
			}
			// This API uses CR in its seven spare bits to distinguish padding from @.
			want := byte(septet>>6) | (0x0d << 1)
			if encoded[len(encoded)-1] != want {
				t.Errorf("%d septets ending %q: final=%02x want=%02x", count, r, encoded[len(encoded)-1], want)
			}
			decoded, err := GSM7BITPACKED.Decode(encoded)
			if err != nil || decoded != text {
				t.Errorf("%d septets ending %q: decoded=%q want=%q err=%v", count, r, decoded, text, err)
			}
		}
	}
}

func TestGSM7PackedLegacyZeroPadding(t *testing.T) {
	// Seven ASCII GSM septets packed independently with zero padding. The
	// last octet's bit zero belongs to G; the seven upper bits are padding.
	raw := []byte{0x61, 0xf1, 0x98, 0x5c, 0x36, 0x1f, 0x01}
	got, err := GSM7BITPACKED.Decode(raw)
	if err != nil || got != "abcdefG" {
		t.Fatalf("decoded %x as %q, err=%v", raw, got, err)
	}
}

func TestGSM7UndefinedExtensionFallback(t *testing.T) {
	// TS 23.038 §6.2.1.1 requires undefined extensions to use the default
	// alphabet; the reserved escape-to-another-extension code displays space.
	for _, tc := range []struct {
		raw  []byte
		want string
	}{
		{[]byte{0x1b, 0x00}, "@"}, {[]byte{0x1b, 0x01}, "£"},
		{[]byte{0x1b, 0x41}, "A"}, {[]byte{0x1b, 0x7f}, "à"},
		{[]byte{0x1b, 0x1b}, " "}, {[]byte{0x1b, 0x14}, "^"},
	} {
		got, err := GSM7BIT.Decode(tc.raw)
		if err != nil || got != tc.want {
			t.Errorf("raw=%x got=%q want=%q err=%v", tc.raw, got, tc.want, err)
		}
		if bad := ValidateGSM7Buffer(tc.raw); len(bad) != 0 {
			t.Errorf("valid fallback %x rejected: %x", tc.raw, bad)
		}
		packed := referenceGSM7Pack(tc.raw, 0)
		got, err = GSM7BITPACKED.Decode(packed)
		if err != nil || got != tc.want {
			t.Errorf("packed=%x got=%q want=%q err=%v", packed, got, tc.want, err)
		}
	}
}

func TestGSM7PackedDecodeFillAndCount(t *testing.T) {
	// Hand-packed A vectors cover every UDH fill alignment. At fill=2 the
	// final octet contains the high A bit plus the CR-padding convention.
	vectors := [][]byte{{0x41}, {0x82}, {0x04, 0x1b}, {0x08, 0x02}, {0x10, 0x04}, {0x20, 0x08}, {0x40, 0x10}}
	for fill, raw := range vectors {
		for _, count := range []int{1, -1} {
			got, err := DecodeGSM7Packed(raw, uint8(fill), count)
			if err != nil || got != "A" {
				t.Errorf("fill=%d count=%d raw=%x got=%q err=%v", fill, count, raw, got, err)
			}
		}
	}
	for fill := uint8(0); fill <= 6; fill++ {
		for count := 1; count <= 40; count++ {
			text := strings.Repeat("A", count-1) + "@"
			septets := append(bytes.Repeat([]byte{0x41}, count-1), 0)
			raw := referenceGSM7Pack(septets, fill)
			got, err := DecodeGSM7Packed(raw, fill, count)
			if err != nil || got != text {
				t.Fatalf("fill=%d septets=%d exact=%q want=%q err=%v", fill, count, got, text, err)
			}
		}
	}
	for _, tc := range []struct {
		raw   []byte
		fill  uint8
		count int
	}{
		{[]byte{0x41}, 7, 1}, {[]byte{0x41}, 0, -2}, {[]byte{0x41}, 0, 2},
		{nil, 0, 1}, {[]byte{0x1b}, 0, 1},
	} {
		_, err := DecodeGSM7Packed(tc.raw, tc.fill, tc.count)
		if err == nil {
			t.Errorf("accepted raw=%x fill=%d count=%d", tc.raw, tc.fill, tc.count)
		}
	}
}

func TestGSM7PackedTerminalPaddingAmbiguity(t *testing.T) {
	for _, count := range []int{8, 16, 24} {
		// SMPP carries only an octet count. Legacy seven-zero-bit padding
		// and a literal final @ share identical bytes. The package's CR
		// padding avoids this collision for newly encoded shorter text.
		prefix := strings.Repeat("A", count-1)
		zeroPadded := referenceGSM7Pack(bytes.Repeat([]byte{0x41}, count-1), 0)
		zeroPadded[len(zeroPadded)-1] &= 1
		withAt, err := GSM7BITPACKED.Encode(prefix + "@")
		if err != nil || !bytes.Equal(withAt, zeroPadded) {
			t.Fatalf("terminal @ collision raw=%x padding=%x err=%v", withAt, zeroPadded, err)
		}
		// TS 23.038 §6.1.2.3.1's duplicated CR is identical to a literal
		// second CR. This ambiguity cannot be resolved without a count.
		oneCR, err := GSM7BITPACKED.Encode(prefix + "\r")
		if err != nil {
			t.Fatal(err)
		}
		twoCR, err := GSM7BITPACKED.Encode(prefix + "\r\r")
		if err != nil || !bytes.Equal(oneCR, twoCR) {
			t.Fatalf("terminal CR collision first=%x second=%x err=%v", oneCR, twoCR, err)
		}
		for _, tc := range []struct {
			raw      []byte
			text     string
			inferred string
			count    int
		}{
			{withAt, prefix, prefix, count - 1},
			{withAt, prefix + "@", prefix, count},
			{oneCR, prefix + "\r", prefix + "\r", count},
			{twoCR, prefix + "\r\r", prefix + "\r", count + 1},
		} {
			inferred, err := GSM7BITPACKED.Decode(tc.raw)
			if err != nil || inferred != tc.inferred {
				t.Fatalf("inferred %q want=%q raw=%x err=%v", inferred, tc.inferred, tc.raw, err)
			}
			exact, err := DecodeGSM7Packed(tc.raw, 0, tc.count)
			if err != nil || exact != tc.text {
				t.Fatalf("counted %q want=%q err=%v", exact, tc.text, err)
			}
		}
	}
}

func referenceGSM7Pack(septets []byte, fill uint8) []byte {
	bits := int(fill) + len(septets)*7
	if len(septets) == 0 {
		return nil
	}
	raw := make([]byte, (bits+7)/8)
	for i, s := range septets {
		for bit := 0; bit < 7; bit++ {
			if s&(1<<bit) == 0 {
				continue
			}
			offset := int(fill) + i*7 + bit
			raw[offset/8] |= 1 << uint(offset%8)
		}
	}
	if bits%8 == 1 {
		raw[len(raw)-1] |= 0x0d << 1
	}
	return raw
}

func TestGSM7PackedSplitPacksFillDirectly(t *testing.T) {
	for _, count := range []int{7, 8, 15, 16, 23, 24, 152, 153} {
		text := strings.Repeat("A", count)
		parts, err := GSM7BITPACKED.(Splitter).EncodeSplit(text, 134)
		if err != nil || len(parts) != 1 {
			t.Fatalf("count=%d segments=%d err=%v", count, len(parts), err)
		}
		want := referenceGSM7Pack(bytes.Repeat([]byte{0x41}, count), 1)
		if !bytes.Equal(parts[0], want) {
			t.Errorf("count=%d packed=%x want=%x", count, parts[0], want)
		}
		got, err := DecodeGSM7Packed(parts[0], 1, -1)
		if err != nil || got != text {
			t.Errorf("count=%d decoded=%q want=%q err=%v", count, got, text, err)
		}
	}
}
