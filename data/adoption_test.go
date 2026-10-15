package data_test

import (
	"bytes"
	"errors"
	"testing"

	"github.com/gomaja/go-smpp/data"
	"github.com/gomaja/go-sms/encoding/gsm7"
	"github.com/gomaja/go-sms/encoding/ucs2"
)

// Current TS 23.038 sections 6.2.1/6.2.1.1 defines these tables. The literal
// table is an independent wire oracle, including every assigned extension.
const adoptionDefaultAlphabet = "@£$¥èéùìòÇ\nØø\rÅåΔ_ΦΓΛΩΠΨΣΘΞ\x1bÆæßÉ !\"#¤%&'()*+,-./0123456789:;<=>?¡ABCDEFGHIJKLMNOPQRSTUVWXYZÄÖÑÜ§¿abcdefghijklmnopqrstuvwxyzäöñüà"

func adoptionPack(septets []byte) []byte {
	if len(septets) == 0 {
		return nil
	}
	padded := append([]byte(nil), septets...)
	bits := len(padded) * 7
	if bits%8 == 1 || bits%8 == 0 && padded[len(padded)-1] == 0x0d {
		padded = append(padded, 0x0d)
	}
	packed := make([]byte, (len(padded)*7+7)/8)
	for i, v := range padded {
		for k := 0; k < 7; k++ {
			if v&(1<<k) != 0 {
				bit := i*7 + k
				packed[bit/8] |= 1 << uint(bit%8)
			}
		}
	}
	return packed
}

func TestAdoptionDefaultAlphabetWire(t *testing.T) {
	table := []rune(adoptionDefaultAlphabet)
	if len(table) != 128 {
		t.Fatalf("oracle length %d", len(table))
	}
	tested := 0
	for code, r := range table {
		if code == 0x1b {
			continue
		}
		for _, enc := range []data.Encoding{data.GSM7BIT, data.GSM7BITPACKED} {
			want := []byte{byte(code)}
			if enc == data.GSM7BITPACKED {
				want = adoptionPack(want)
			}
			got, err := enc.Encode(string(r))
			if err != nil || !bytes.Equal(got, want) {
				t.Errorf("%T encode %U=%x,%v; want %x", enc, r, got, err, want)
			}
			text, err := enc.Decode(want)
			if err != nil || text != string(r) {
				t.Errorf("%T decode %x=%q,%v; want %q", enc, want, text, err, string(r))
			}
		}
		tested++
	}
	for _, extension := range []struct {
		r    rune
		code byte
	}{{'\f', 0x0a}, {'^', 0x14}, {'{', 0x28}, {'}', 0x29}, {'\\', 0x2f}, {'[', 0x3c}, {'~', 0x3d}, {']', 0x3e}, {'|', 0x40}, {'€', 0x65}} {
		for _, enc := range []data.Encoding{data.GSM7BIT, data.GSM7BITPACKED} {
			want := []byte{0x1b, extension.code}
			if enc == data.GSM7BITPACKED {
				want = adoptionPack(want)
			}
			got, err := enc.Encode(string(extension.r))
			if err != nil || !bytes.Equal(got, want) {
				t.Errorf("%T extension %U=%x,%v; want %x", enc, extension.r, got, err, want)
			}
			text, err := enc.Decode(want)
			if err != nil || text != string(extension.r) {
				t.Errorf("%T extension %x=%q,%v", enc, want, text, err)
			}
		}
		tested++
	}
	if tested != 137 {
		t.Fatalf("tested %d GSM7 characters; want 137", tested)
	}
}

// Catch stricter defaults accidentally reintroduced at the public boundary.
// Undefined extension slots use the main table; malformed septets and a
// terminal escape use go-sms's permissive space substitution.
func TestAdoptionPermissiveGSM7(t *testing.T) {
	for _, tc := range []struct {
		wire []byte
		want string
	}{
		{[]byte{0x1b}, " "}, {[]byte{0x1b, 0x1b}, " "}, {[]byte{0x1b, 0x41}, "A"},
		{[]byte{0x41, 0x1b}, "A "}, {[]byte{0x1b, 0x80}, " "}, {[]byte{0x80, 0x41, 0xff}, " A "},
	} {
		got, err := data.GSM7BIT.Decode(tc.wire)
		if err != nil || got != tc.want {
			t.Errorf("Decode(%x)=%q,%v; want %q,nil", tc.wire, got, err, tc.want)
		}
	}
	for octet := 0x80; octet <= 0xff; octet++ {
		for _, wire := range [][]byte{{byte(octet)}, {0x1b, byte(octet)}} {
			got, err := data.GSM7BIT.Decode(wire)
			if err != nil || got != " " {
				t.Errorf("Decode(%x)=%q,%v; want space, nil", wire, got, err)
			}
		}
	}
}

func TestAdoptionGSM7TypedEncodeErrors(t *testing.T) {
	for _, enc := range []data.Encoding{data.GSM7BIT, data.GSM7BITPACKED} {
		got, err := enc.Encode("é😀")
		var unencodable gsm7.ErrUnencodable
		if !errors.As(err, &unencodable) || unencodable.Offset != 2 || unencodable.Rune != '😀' {
			t.Errorf("%T unencodable=%x,%T %v; want gsm7.ErrUnencodable at offset 2", enc, got, err, err)
		}
		got, err = enc.Encode(string([]byte{'A', 0xff}))
		var invalid gsm7.ErrInvalidUTF8
		if !errors.As(err, &invalid) || invalid.Offset != 1 || invalid.Byte != 0xff {
			t.Errorf("%T invalidUTF8=%x,%T %v; want gsm7.ErrInvalidUTF8 at offset 1", enc, got, err, err)
		}
	}
}

func TestAdoptionUCS2DecodeErrors(t *testing.T) {
	for _, tc := range []struct {
		name     string
		wire     []byte
		want     string
		odd      bool
		dangling []byte
	}{
		{"odd", []byte{0}, "", true, nil},
		{"odd discards prefix", []byte{0, 0x41, 0}, "", true, nil},
		{"high", []byte{0xd8, 0}, "", false, []byte{0xd8, 0}},
		{"A high", []byte{0, 0x41, 0xd8, 0}, "A", false, []byte{0xd8, 0}},
		{"low high", []byte{0xdc, 0, 0xd8, 0}, "\ufffd", false, []byte{0xd8, 0}},
		{"high A", []byte{0xd8, 0, 0, 0x41}, "\ufffdA", false, nil},
		{"low", []byte{0xdc, 0}, "\ufffd", false, nil},
		{"pair", []byte{0xd8, 0x3d, 0xde, 0}, "😀", false, nil},
		{"BE BOM", []byte{0xfe, 0xff, 0, 0x41}, "\ufeffA", false, nil},
		{"LE BOM is data", []byte{0xff, 0xfe, 0x41, 0}, "\ufffe\u4100", false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := data.UCS2.Decode(tc.wire)
			if got != tc.want {
				t.Errorf("Decode=%q,%v; want %q", got, err, tc.want)
			}
			switch {
			case tc.odd:
				if !errors.Is(err, ucs2.ErrInvalidLength) {
					t.Errorf("odd error=%T %v", err, err)
				}
			case tc.dangling != nil:
				var dangling ucs2.ErrDanglingSurrogate
				if !errors.As(err, &dangling) || !bytes.Equal(dangling, tc.dangling) {
					t.Errorf("dangling error=%T %v", err, err)
				}
			default:
				if err != nil {
					t.Error(err)
				}
			}
		})
	}
}

// The strict option changes encoding acceptance only: it retains data_coding
// 0x08 and the shared UTF-16BE decoder, including typed errors and prefixes.
func TestAdoptionUCS2Strict(t *testing.T) {
	if data.UCS2Strict.DataCoding() != 0x08 || data.UCS2.DataCoding() != 0x08 {
		t.Fatal("both UCS2 modes must retain wire coding 0x08")
	}
	for _, tc := range []struct {
		text string
		wire []byte
	}{
		{"", nil}, {"A漢\ufeff\uffff", []byte{0, 0x41, 0x6f, 0x22, 0xfe, 0xff, 0xff, 0xff}},
	} {
		for _, enc := range []data.Encoding{data.UCS2, data.UCS2Strict} {
			got, err := enc.Encode(tc.text)
			if err != nil || !bytes.Equal(got, tc.wire) {
				t.Errorf("%T Encode(%q)=%x,%v; want %x", enc, tc.text, got, err, tc.wire)
			}
		}
	}
	for _, tc := range []struct {
		text string
		wire []byte
		r    rune
	}{{"A𐀀", []byte{0, 0x41, 0xd8, 0, 0xdc, 0}, 0x10000}, {"A😀", []byte{0, 0x41, 0xd8, 0x3d, 0xde, 0}, 0x1f600}, {"A\U0010ffff", []byte{0, 0x41, 0xdb, 0xff, 0xdf, 0xff}, 0x10ffff}} {
		got, err := data.UCS2.Encode(tc.text)
		if err != nil || !bytes.Equal(got, tc.wire) {
			t.Errorf("UTF16 encoding %q=%x,%v; want %x", tc.text, got, err, tc.wire)
		}
		got, err = data.UCS2Strict.Encode(tc.text)
		var strict ucs2.ErrUnencodable
		if !errors.As(err, &strict) || strict.Index != 1 || strict.Rune != tc.r || got != nil {
			t.Errorf("strict encoding %q=%x,%T %v; want nil, ucs2.ErrUnencodable at index 1", tc.text, got, err, err)
		}
		for _, enc := range []data.Encoding{data.UCS2, data.UCS2Strict} {
			decoded, err := enc.Decode(tc.wire)
			if err != nil || decoded != tc.text {
				t.Errorf("%T shared decoding=%q,%v; want %q", enc, decoded, err, tc.text)
			}
		}
	}
	for _, enc := range []data.Encoding{data.UCS2, data.UCS2Strict} {
		got, err := enc.Decode([]byte{0, 0x41, 0xd8, 0})
		var dangling ucs2.ErrDanglingSurrogate
		if got != "A" || !errors.As(err, &dangling) || !bytes.Equal(dangling, []byte{0xd8, 0}) {
			t.Errorf("%T prefix=%q,%T %v", enc, got, err, err)
		}
		got, err = enc.Decode([]byte{0, 0x41, 0})
		if got != "" || !errors.Is(err, ucs2.ErrInvalidLength) {
			t.Errorf("%T odd=%q,%T %v", enc, got, err, err)
		}
	}
}

func TestAdoptionUCS2RejectsInvalidUTF8(t *testing.T) {
	for _, enc := range []data.Encoding{data.UCS2, data.UCS2Strict} {
		for _, input := range [][]byte{{0xff}, {0xc0, 0xaf}, {0xed, 0xa0, 0x80}, {0xf4, 0x90, 0x80, 0x80}, {0xe2, 0x82}, {'A', 0xff, 'B'}} {
			got, err := enc.Encode(string(input))
			if err == nil || got != nil {
				t.Errorf("%T Encode(%x)=%x,%v; want nil, error", enc, input, got, err)
			}
		}
		// A literal replacement character is valid text, distinct from malformed
		// UTF-8 that would produce U+FFFD only through lossy rune conversion.
		got, err := enc.Encode("\ufffd")
		if err != nil || !bytes.Equal(got, []byte{0xff, 0xfd}) {
			t.Errorf("%T literal replacement=%x,%v", enc, got, err)
		}
	}
}

func TestAdoptionUCS2SplitterGuards(t *testing.T) {
	for _, enc := range []data.Encoding{data.UCS2, data.UCS2Strict} {
		splitter, ok := enc.(data.Splitter)
		if !ok {
			t.Fatalf("%T does not support splitting", enc)
		}
		for _, input := range []string{string([]byte{0xff}), string([]byte{'A', 0xed, 0xa0, 0x80})} {
			if splitter.ShouldSplit(input, 140) {
				t.Errorf("%T invalid UTF8 must not request segmentation", enc)
			}
			parts, err := splitter.EncodeSplit(input, 140)
			if err == nil || parts != nil {
				t.Errorf("%T split malformed UTF8=%x,%v; want nil, error", enc, parts, err)
			}
		}
		if splitter.ShouldSplit("漢A", 4) || !splitter.ShouldSplit("漢A", 3) {
			t.Errorf("%T split budget must count encoded octets", enc)
		}
		parts, err := splitter.EncodeSplit("漢A", 3)
		if err != nil || len(parts) != 2 || !bytes.Equal(parts[0], []byte{0x6f, 0x22}) || !bytes.Equal(parts[1], []byte{0, 0x41}) {
			t.Errorf("%T split BMP=%x,%v", enc, parts, err)
		}
	}
	strict := data.UCS2Strict.(data.Splitter)
	if strict.ShouldSplit("😀", 140) {
		t.Error("strict astral input must not request segmentation")
	}
	parts, err := strict.EncodeSplit("A😀", 2)
	var astral ucs2.ErrUnencodable
	if parts != nil || !errors.As(err, &astral) || astral.Index != 1 || astral.Rune != '😀' {
		t.Errorf("strict split=%x,%T %v; want original-index strict error", parts, err, err)
	}
	compatibility := data.UCS2.(data.Splitter)
	if compatibility.ShouldSplit("😀", 4) || !compatibility.ShouldSplit("😀", 3) {
		t.Error("UTF16 astral must consume four octets")
	}
	parts, err = compatibility.EncodeSplit("A😀B", 4)
	expected := [][]byte{{0, 0x41}, {0xd8, 0x3d, 0xde, 0}, {0, 0x42}}
	if err != nil || len(parts) != len(expected) {
		t.Fatalf("UTF16 split=%x,%v", parts, err)
	}
	for i := range expected {
		if !bytes.Equal(parts[i], expected[i]) {
			t.Errorf("UTF16 part %d=%x; want %x", i, parts[i], expected[i])
		}
	}
}
