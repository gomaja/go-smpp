package data

import (
	"bufio"
	"bytes"
	"errors"

	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/gomaja/go-sms/encoding/gsm7"
	smsucs2 "github.com/gomaja/go-sms/encoding/ucs2"
)

// SMPP v3.4 Issue 1.2 §5.2.19 gives the direct alphabet assignments.
// TS 23.038 V19.0.0 §4 supplies the extended SMS coding groups and the
// receiver fallback for reserved codings. Rows are independent of codingMap.
func TestFromDataCodingAllOctets(t *testing.T) {
	rows := [16]string{
		"GSBLBOCHUOOGGOOG",
		"GGGGBBBBUUUUGGGG",
		"BBBBBBBBBBBBGGGG",
		"BBBBBBBBBBBBGGGG",
		"GGGGBBBBUUUUGGGG",
		"GGGGBBBBUUUUGGGG",
		"BBBBBBBBBBBBGGGG",
		"BBBBBBBBBBBBGGGG",
		"GGGGGGGGGGGGGGGG",
		"GGGGGGGGGGGGGGGG",
		"GGGGGGGGGGGGGGGG",
		"GGGGGGGGGGGGGGGG",
		"GGGGGGGGGGGGGGGG",
		"GGGGGGGGGGGGGGGG",
		"UUUUUUUUUUUUUUUU",
		"GGGGBBBBGGGGBBBB",
	}
	bases := map[byte]Encoding{'G': GSM7BIT, 'S': ASCII, 'B': BINARY8BIT2,
		'L': LATIN1, 'C': CYRILLIC, 'H': HEBREW, 'U': UCS2}
	for value := 0; value < 256; value++ {
		code := byte(value)
		t.Run(fmt.Sprintf("%02X", code), func(t *testing.T) {
			enc := FromDataCoding(code)
			if enc.DataCoding() != code {
				t.Fatalf("wire coding = %02X, want %02X", enc.DataCoding(), code)
			}
			kind := rows[value/16][value%16]
			if kind == 'O' {
				if _, ok := enc.(Splitter); ok {
					t.Fatal("unsupported alphabet advertises character-aware splitting")
				}
				if _, err := enc.Encode("A"); err != ErrNotImplEncode {
					t.Fatalf("unsupported alphabet encode error = %v", err)
				}
				if _, err := enc.Decode([]byte{'A'}); err != ErrNotImplDecode {
					t.Fatalf("unsupported alphabet decode error = %v", err)
				}
				return
			}
			base := EncDec(enc)
			if custom, ok := enc.(*CustomEncoding); ok {
				base = custom.BaseEncoding()
			}
			want := bases[kind]
			if code == BINARY8BIT1Coding {
				want = BINARY8BIT1
			}
			if base != want {
				t.Errorf("alphabet = %T, want %T (%c)", base, want, kind)
			}
			for _, text := range []string{"A", "é€", "אЯ", "😀"} {
				wantBytes, wantErr := want.Encode(text)
				gotBytes, gotErr := enc.Encode(text)
				if (wantErr == nil) != (gotErr == nil) || !bytes.Equal(gotBytes, wantBytes) {
					t.Errorf("Encode(%q) = %X, %v; want %X, %v", text, gotBytes, gotErr, wantBytes, wantErr)
				}
			}
		})
	}
}

// Unicode's official ISO mappings, table version 2.0 (headers updated
// 2015-12-02), are retained verbatim under testdata/unicode with their license.
// https://www.unicode.org/Public/MAPPINGS/ISO8859/8859-1.TXT
// https://www.unicode.org/Public/MAPPINGS/ISO8859/8859-5.TXT
// https://www.unicode.org/Public/MAPPINGS/ISO8859/8859-8.TXT
func TestISO8859OfficialUnicodeMappings(t *testing.T) {
	for _, tc := range []struct {
		name string
		enc  Encoding
	}{{"8859-1", LATIN1}, {"8859-5", CYRILLIC}, {"8859-8", HEBREW}} {
		t.Run(tc.name, func(t *testing.T) {
			fixture, err := os.Open("testdata/unicode/" + tc.name + ".TXT")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = fixture.Close() }()
			var assigned [256]bool
			scanner := bufio.NewScanner(fixture)
			for scanner.Scan() {
				fields := strings.Fields(scanner.Text())
				if len(fields) < 2 || !strings.HasPrefix(fields[0], "0x") {
					continue
				}
				value, err := strconv.ParseUint(fields[0][2:], 16, 8)
				if err != nil {
					t.Fatal(err)
				}
				scalar, err := strconv.ParseUint(fields[1][2:], 16, 32)
				if err != nil {
					t.Fatal(err)
				}
				assigned[value] = true
				// x/text does not preserve C1 controls in these two alphabets.
				// TestISO8859C1CurrentUpstreamBehavior records that limitation.
				if (tc.name == "8859-5" || tc.name == "8859-8") && value >= 0x80 && value <= 0x9f {
					continue
				}
				want := string(rune(scalar))
				got, err := tc.enc.Decode([]byte{byte(value)})
				if err != nil || got != want {
					t.Errorf("Decode(%02X) = %q, %v; want %q", value, got, err, want)
				}
				encoded, err := tc.enc.Encode(want)
				if err != nil || !bytes.Equal(encoded, []byte{byte(value)}) {
					t.Errorf("Encode(U+%04X) = %X, %v; want %02X", scalar, encoded, err, value)
				}
			}
			if err := scanner.Err(); err != nil {
				t.Fatal(err)
			}
			for value, ok := range assigned {
				if ok {
					continue
				}
				// x/text decoders replace undefined octets; that receive policy
				// does not turn U+FFFD into an encodable alphabet character.
				got, err := tc.enc.Decode([]byte{byte(value)})
				if err != nil || got != "\uFFFD" {
					t.Errorf("undefined octet %02X = %q, %v; want replacement", value, got, err)
				}
			}
			for _, text := range []string{"😀", "\uFFFD"} {
				if encoded, err := tc.enc.Encode(text); err == nil {
					t.Errorf("unrepresentable %q encoded as %X", text, encoded)
				}
			}
		})
	}
}

// Record the current x/text C1 limitation, not the official ISO mapping.
// Related discussion that requests the inverse change (Latin-1 replacement):
// https://github.com/golang/go/issues/23706
// This test intentionally fails once upstream preserves the controls, as the
// signal to revisit the limitation and restore full official mapping coverage.
func TestISO8859C1CurrentUpstreamBehavior(t *testing.T) {
	for _, tc := range []struct {
		name string
		enc  Encoding
	}{{"8859-5", CYRILLIC}, {"8859-8", HEBREW}} {
		t.Run(tc.name, func(t *testing.T) {
			for value := 0x80; value <= 0x9f; value++ {
				if got, err := tc.enc.Decode([]byte{byte(value)}); err != nil || got != "\ufffd" {
					t.Errorf("Decode(%02X) = %q, %v; want replacement; revisit upstream C1 limitation", value, got, err)
				}
				if encoded, err := tc.enc.Encode(string(rune(value))); err == nil {
					t.Errorf("Encode(U+%04X) = %X without error; revisit upstream C1 limitation", value, encoded)
				}
			}
		})
	}
}

// Latin-1 currently preserves all C1 controls. Revisit if upstream changes:
// https://github.com/golang/go/issues/23706 is related discussion that requests
// the inverse change: replacing Latin-1 C1 controls with U+FFFD.
func TestISO8859Latin1C1CurrentUpstreamPreservation(t *testing.T) {
	for value := 0x80; value <= 0x9f; value++ {
		want := string(rune(value))
		if got, err := LATIN1.Decode([]byte{byte(value)}); err != nil || got != want {
			t.Errorf("Decode(%02X) = %q, %v; want U+%04X; revisit upstream C1 behavior", value, got, err, value)
		}
		if got, err := LATIN1.Encode(want); err != nil || !bytes.Equal(got, []byte{byte(value)}) {
			t.Errorf("Encode(U+%04X) = %X, %v; want %02X; revisit upstream C1 behavior", value, got, err, value)
		}
	}
}

// TS 23.038 V19.0.0 §§6.2.1–6.2.1.1: independent table order, including
// uppercase Ç at 09 as printed by ETSI. Unicode GSM0338.TXT instead chooses
// lowercase ç and maps a standalone ESC to NBSP; those interpretations are
// not substituted for the current ETSI alphabet and escape mechanism here.
func TestGSM7CurrentETSIAlphabetAndExtensions(t *testing.T) {
	main := []rune("@£$¥èéùìòÇ\nØø\rÅåΔ_ΦΓΛΩΠΨΣΘΞ\x1bÆæßÉ !\"#¤%&'()*+,-./0123456789:;<=>?¡ABCDEFGHIJKLMNOPQRSTUVWXYZÄÖÑÜ§¿abcdefghijklmnopqrstuvwxyzäöñüà")
	if len(main) != 128 {
		t.Fatal("alphabet fixture must describe 128 septet positions")
	}
	extensions := map[byte]rune{0x0a: '\f', 0x14: '^', 0x28: '{', 0x29: '}',
		0x2f: '\\', 0x3c: '[', 0x3d: '~', 0x3e: ']', 0x40: '|', 0x65: '€'}
	for value, scalar := range main {
		code := byte(value)
		t.Run(fmt.Sprintf("%02X", code), func(t *testing.T) {
			if code != 0x1b {
				got, err := GSM7BIT.Decode([]byte{code})
				if err != nil || got != string(scalar) {
					t.Errorf("main table: got %q, %v; want U+%04X", got, err, scalar)
				}
				encoded, err := GSM7BIT.Encode(string(scalar))
				if err != nil || !bytes.Equal(encoded, []byte{code}) {
					t.Errorf("encode U+%04X = %X, %v; want %02X", scalar, encoded, err, code)
				}
			}
			want, assigned := extensions[code]
			if !assigned {
				want = scalar
				if code == 0x1b {
					want = ' '
				}
			}
			got, err := GSM7BIT.Decode([]byte{0x1b, code})
			if err != nil || got != string(want) {
				t.Errorf("extension table: got %q, %v; want U+%04X", got, err, want)
			}
			if assigned {
				encoded, err := GSM7BIT.Encode(string(want))
				if err != nil || !bytes.Equal(encoded, []byte{0x1b, code}) {
					t.Errorf("encode extension U+%04X = %X, %v", want, encoded, err)
				}
			}
		})
	}
	for value := 0x80; value <= 0xff; value++ {
		for _, input := range [][]byte{{byte(value)}, {0x1b, byte(value)}} {
			if text, err := GSM7BIT.Decode(input); err != nil || text != " " {
				t.Errorf("out-of-range %X text=%q error = %v", input, text, err)
			}
		}
	}
	for _, text := range []string{"\x1b", "\u00a0", "ç", "😀", string([]byte{0xff})} {
		if encoded, err := GSM7BIT.Encode(text); err == nil {
			t.Errorf("unsupported %q = %X without error", text, encoded)
		} else {
			var invalid gsm7.ErrInvalidUTF8
			var unencodable gsm7.ErrUnencodable
			if !errors.As(err, &invalid) && !errors.As(err, &unencodable) {
				t.Errorf("unsupported %q unexpected error %T: %v", text, err, err)
			}
		}
	}
	if text, err := GSM7BIT.Decode([]byte{0x1b}); err != nil || text != " " {
		t.Errorf("truncated escape text=%q error = %v", text, err)
	}
}

// SMPP §5.2.19 calls coding 08 UCS2. The existing API deliberately uses
// UTF-16BE compatibility, including supplementary pairs (RFC 2781 §2.1).
// Malformed input follows go-sms, including typed errors and decoded prefixes.
func TestUCS2UTF16BECompatibility(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		wire       []byte
	}{
		{"empty", "", nil},
		{"BMP", "AéאЯ", []byte{0, 0x41, 0, 0xe9, 0x05, 0xd0, 0x04, 0x2f}},
		{"supplementary", "😀", []byte{0xd8, 0x3d, 0xde, 0}},
		{"scalar_limits", "\U00010000\U0010ffff", []byte{0xd8, 0, 0xdc, 0, 0xdb, 0xff, 0xdf, 0xff}},
		{"literal_BOM", "\ufeffA", []byte{0xfe, 0xff, 0, 0x41}},
		{"opposite_BOM_is_content", "\ufffeA", []byte{0xff, 0xfe, 0, 0x41}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			encoded, err := UCS2.Encode(tc.text)
			if err != nil || !bytes.Equal(encoded, tc.wire) {
				t.Errorf("Encode = %X, %v; want %X", encoded, err, tc.wire)
			}
			decoded, err := UCS2.Decode(tc.wire)
			if err != nil || decoded != tc.text {
				t.Errorf("Decode = %q, %v; want %q", decoded, err, tc.text)
			}
		})
	}
	for _, tc := range []struct {
		name    string
		wire    []byte
		want    string
		errKind string
	}{
		{"odd_octet", []byte{0}, "", "odd"},
		{"odd_preserves_character", []byte{0, 0x41, 0}, "A", "odd"},
		{"high_surrogate", []byte{0xd8, 0x3d}, "", "dangling"},
		{"low_surrogate", []byte{0xde, 0}, "\ufffd", ""},
		{"reversed_pair", []byte{0xde, 0, 0xd8, 0x3d}, "\ufffd", "dangling"},
		{"high_then_BMP", []byte{0xd8, 0x3d, 0, 0x41}, "\ufffdA", ""},
		{"high_then_high", []byte{0xd8, 0x3d, 0xd8, 0x3d}, "\ufffd", "dangling"},
		{"high_before_final_octet", []byte{0xd8, 0x3d, 0xde}, "\ufffd", "odd"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			decoded, err := UCS2.Decode(tc.wire)
			var dangling smsucs2.ErrDanglingSurrogate
			if decoded != tc.want || tc.errKind == "odd" && !errors.Is(err, smsucs2.ErrInvalidLength) ||
				tc.errKind == "dangling" && !errors.As(err, &dangling) || tc.errKind == "" && err != nil {
				t.Errorf("Decode(%X)=%q,%v; want %q error=%s", tc.wire, decoded, err, tc.want, tc.errKind)
			}
			if tc.errKind == "odd" && errors.As(err, &dangling) {
				t.Errorf("odd payload must not carry a dangling surrogate: %v", err)
			}
		})
	}
	if encoded, err := UCS2.Encode(string([]byte{0xff})); err == nil || encoded != nil {
		t.Errorf("invalid UTF-8=%X,%v; want rejection", encoded, err)
	}
}

// SMPP v3.4 §5.2.19 selects IA5/ASCII explicitly. Sending checks the
// alphabet; receiving preserves octets under the documented transit policy.
func TestASCIIAllOctets(t *testing.T) {
	for value := 0; value < 256; value++ {
		wire := []byte{byte(value)}
		decoded, err := ASCII.Decode(wire)
		if err != nil || decoded != string(wire) {
			t.Errorf("Decode(%02X) = %q, %v", value, decoded, err)
		}
		encoded, err := ASCII.Encode(string(wire))
		if value <= 0x7f {
			if err != nil || !bytes.Equal(encoded, wire) {
				t.Errorf("ASCII %02X = %X, %v", value, encoded, err)
			}
		} else if err == nil {
			t.Errorf("non-ASCII source %02X accepted as %X", value, encoded)
		}
	}
}

// Unicode 18.0 §§3.9–3.10: BOM-aware UTF-16 variants consume a leading byte-order
// signature; fixed-order variants retain it as content and never swap order.
func TestUTF16ExplicitByteOrderAndBOM(t *testing.T) {
	for _, tc := range []struct {
		name        string
		enc         EncDec
		encoded     []byte
		empty       []byte
		big, little string
	}{
		{"BE", UTF16BE, []byte{0, 0x41, 0xd8, 0x3d, 0xde, 0}, nil, "\ufeffA", "\ufffe\u4100"},
		{"LE", UTF16LE, []byte{0x41, 0, 0x3d, 0xd8, 0, 0xde}, nil, "\ufffe\u4100", "\ufeffA"},
		{"BE_BOM", UTF16BEM, []byte{0xfe, 0xff, 0, 0x41, 0xd8, 0x3d, 0xde, 0}, []byte{0xfe, 0xff}, "A", "A"},
		{"LE_BOM", UTF16LEM, []byte{0xff, 0xfe, 0x41, 0, 0x3d, 0xd8, 0, 0xde}, []byte{0xff, 0xfe}, "A", "A"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wire, err := tc.enc.Encode("A😀")
			if err != nil || !bytes.Equal(wire, tc.encoded) {
				t.Errorf("Encode = %X, %v; want %X", wire, err, tc.encoded)
			}
			text, err := tc.enc.Decode(tc.encoded)
			if err != nil || text != "A😀" {
				t.Errorf("Decode = %q, %v", text, err)
			}
			empty, err := tc.enc.Encode("")
			if err != nil || !bytes.Equal(empty, tc.empty) {
				t.Errorf("Encode empty = %X, %v; want %X", empty, err, tc.empty)
			}
			for _, signature := range []struct {
				wire []byte
				want string
			}{{[]byte{0xfe, 0xff, 0, 0x41}, tc.big}, {[]byte{0xff, 0xfe, 0x41, 0}, tc.little}} {
				text, err := tc.enc.Decode(signature.wire)
				if err != nil || text != signature.want {
					t.Errorf("signature %X = %q, %v; want %q", signature.wire, text, err, signature.want)
				}
			}
			text, err = tc.enc.Decode([]byte{0})
			if err != nil || text != "\ufffd" {
				t.Errorf("odd length = %q, %v; want replacement", text, err)
			}
		})
	}
}
