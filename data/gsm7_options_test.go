package data_test

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/gomaja/go-smpp/data"
	"github.com/gomaja/go-sms/encoding/gsm7"
)

type gsm7Configuration interface {
	GSM7Options() data.GSM7Options
}

func optionsCodec(t *testing.T, opts data.GSM7Options) data.Encoding {
	t.Helper()
	codec, err := data.NewGSM7(opts)
	if err != nil {
		t.Fatal(err)
	}
	if codec == nil {
		t.Fatal("NewGSM7 returned a nil codec")
	}
	return codec
}

func TestGSM7OptionsValidation(t *testing.T) {
	for locking := 0; locking < 14; locking++ {
		for single := 0; single < 14; single++ {
			opts := data.GSM7Options{LockingShift: locking, SingleShift: single}
			codec, err := data.NewGSM7(opts)
			if locking == 2 {
				if err == nil || codec != nil {
					t.Errorf("Spanish locking shift accepted: %v,%v", codec, err)
				}
				continue
			}
			if err != nil || codec == nil {
				t.Errorf("valid selectors %+v rejected: %v", opts, err)
			}
		}
	}
	for _, selector := range []int{-1, 14, 15, 127, 255, 256, int(^uint(0) >> 1)} {
		for _, opts := range []data.GSM7Options{{LockingShift: selector}, {SingleShift: selector}} {
			codec, err := data.NewGSM7(opts)
			if err == nil || codec != nil {
				t.Errorf("invalid selectors %+v accepted: %v,%v", opts, codec, err)
			}
		}
	}
}

// Literal vectors from TS 23.038 Annex A distinguish locking/single shifts,
// independent selectors and a single-shift table that has no euro sign.
func TestGSM7OptionsWire(t *testing.T) {
	for _, tc := range []struct {
		name    string
		opts    data.GSM7Options
		text    string
		septets []byte
	}{
		{"default", data.GSM7Options{}, "@€", []byte{0, 0x1b, 0x65}},
		{"Turkish locking", data.GSM7Options{LockingShift: 1}, "ıĞ", []byte{7, 0x0b}},
		{"Turkish single", data.GSM7Options{SingleShift: 1}, "ıĞ", []byte{0x1b, 0x69, 0x1b, 0x47}},
		{"Spanish single", data.GSM7Options{SingleShift: 2}, "ÁÓ", []byte{0x1b, 0x41, 0x1b, 0x4f}},
		{"Portuguese locking", data.GSM7Options{LockingShift: 3}, "êã", []byte{4, 0x7b}},
		{"independent selectors", data.GSM7Options{LockingShift: 1, SingleShift: 2}, "ıÁ", []byte{7, 0x1b, 0x41}},
		{"Telugu locking default single", data.GSM7Options{LockingShift: 12}, "€", []byte{0x1b, 0x65}},
	} {
		for _, packed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/packed=%t", tc.name, packed), func(t *testing.T) {
				opts := tc.opts
				opts.Packed = packed
				codec := optionsCodec(t, opts)
				want := tc.septets
				if packed {
					want = optionsPack(tc.septets, 0)
				}
				got, err := codec.Encode(tc.text)
				if err != nil || !bytes.Equal(got, want) {
					t.Fatalf("Encode(%q)=%x,%v; want %x", tc.text, got, err, want)
				}
				decoded, err := codec.Decode(want)
				if err != nil || decoded != tc.text {
					t.Errorf("Decode(%x)=%q,%v; want %q", want, decoded, err, tc.text)
				}
				if codec.DataCoding() != 0 {
					t.Errorf("NLI codec changed SMPP coding to %02x", codec.DataCoding())
				}
				configured, ok := codec.(gsm7Configuration)
				if !ok {
					t.Fatal("codec does not expose options and aligned decoding")
				}
				if configured.GSM7Options() != opts {
					t.Errorf("options=%+v; want %+v", configured.GSM7Options(), opts)
				}
				copy := configured.GSM7Options()
				copy.Packed = !copy.Packed
				copy.LockingShift = 3
				if configured.GSM7Options() != opts {
					t.Error("changing returned options mutated codec")
				}
			})
		}
	}
	for _, packed := range []bool{false, true} {
		codec := optionsCodec(t, data.GSM7Options{Packed: packed, SingleShift: 12})
		_, err := codec.Encode("€")
		var unencodable gsm7.ErrUnencodable
		if !errors.As(err, &unencodable) || unencodable.Rune != '€' {
			t.Errorf("Telugu single shift used an unselected fallback: %T %v", err, err)
		}
	}
}

func optionsPack(septets []byte, fill uint8) []byte {
	if len(septets) == 0 {
		return nil
	}
	wire := make([]byte, (int(fill)+len(septets)*7+7)/8)
	for i, v := range septets {
		for k := 0; k < 7; k++ {
			if v&(1<<k) != 0 {
				bit := int(fill) + i*7 + k
				wire[bit/8] |= 1 << uint(bit%8)
			}
		}
	}
	return wire
}

func TestGSM7OptionsAlignedDecode(t *testing.T) {
	aligned := optionsCodec(t, data.GSM7Options{LockingShift: 1, SingleShift: 2})
	for fill := uint8(0); fill <= 6; fill++ {
		wire := optionsPack([]byte{7, 0x1b, 0x41}, fill)
		wire[0] |= byte((1 << fill) - 1)
		for _, count := range []int{-1, 3} {
			septets := gsm7.Unpack7BitCR(wire, int(fill))
			if count >= 0 {
				septets = gsm7.Unpack7Bit(wire, int(fill))[:count]
			}
			got, err := aligned.Decode(septets)
			want := "ıÁ"
			if count == -1 && fill == 4 {
				want += "@"
			}
			if err != nil || got != want {
				t.Errorf("fill=%d count=%d decoded=%q,%v", fill, count, got, err)
			}
		}
	}
	for _, tc := range []struct {
		fill  uint8
		count int
		wire  []byte
	}{
		{7, -1, nil}, {0, -2, nil}, {0, 1, nil}, {0, 2, []byte{0}},
	} {
		if _, err := data.DecodeGSM7Packed(tc.wire, tc.fill, tc.count); err == nil {
			t.Errorf("invalid fill/count accepted: %+v", tc)
		}
	}
}

func TestGSM7OptionsErrorsAndEmpty(t *testing.T) {
	for _, packed := range []bool{false, true} {
		codec := optionsCodec(t, data.GSM7Options{Packed: packed, LockingShift: 1, SingleShift: 2})
		got, err := codec.Encode("")
		if err != nil || got == nil || len(got) != 0 {
			t.Errorf("empty Encode=%#v,%v; want nonnil empty", got, err)
		}
		decoded, err := codec.Decode(nil)
		if err != nil || decoded != "" {
			t.Errorf("empty Decode=%q,%v", decoded, err)
		}
		_, err = codec.Encode("ı😀")
		var unencodable gsm7.ErrUnencodable
		if !errors.As(err, &unencodable) || unencodable.Offset != 2 || unencodable.Rune != '😀' {
			t.Errorf("typed error=%T %v", err, err)
		}
		_, err = codec.Encode(string([]byte{'A', 0xff}))
		var invalid gsm7.ErrInvalidUTF8
		if !errors.As(err, &invalid) || invalid.Offset != 1 || invalid.Byte != 0xff {
			t.Errorf("UTF8 error=%T %v", err, err)
		}
		splitter := codec.(data.Splitter)
		eight := strings.Repeat("ı", 8)
		encoded, err := codec.Encode(eight)
		if err != nil {
			t.Fatal(err)
		}
		if splitter.ShouldSplit(eight, uint(len(encoded))) || !splitter.ShouldSplit(eight, uint(len(encoded)-1)) {
			t.Error("ShouldSplit ignored the encoded octet budget")
		}
		if splitter.ShouldSplit("😀", 1) || splitter.ShouldSplit(string([]byte{0xff}), 1) {
			t.Error("ShouldSplit returned true for an encoding error")
		}
		for _, invalid := range []string{"😀", string([]byte{0xff})} {
			if parts, err := splitter.EncodeSplit(invalid, 10); err == nil || parts != nil {
				t.Errorf("split invalid=%x,%v", parts, err)
			}
		}
		parts, err := splitter.EncodeSplit("", 10)
		if err != nil || parts == nil || len(parts) != 0 {
			t.Errorf("empty split=%#v,%v", parts, err)
		}
		if _, err := splitter.EncodeSplit("A", 0); err == nil {
			t.Error("zero split budget accepted")
		}
	}
	codec := optionsCodec(t, data.GSM7Options{LockingShift: 1, SingleShift: 2})
	for _, tc := range []struct {
		wire []byte
		want string
	}{{[]byte{0x1b}, " "}, {[]byte{0x80}, " "}, {[]byte{0x1b, 0x80}, " "}, {[]byte{0x1b, 7}, "ı"}} {
		got, err := codec.Decode(tc.wire)
		if err != nil || got != tc.want {
			t.Errorf("permissive Decode(%x)=%q,%v; want %q", tc.wire, got, err, tc.want)
		}
	}
}

func TestGSM7OptionsSplit(t *testing.T) {
	for _, packed := range []bool{false, true} {
		codec := optionsCodec(t, data.GSM7Options{Packed: packed, LockingShift: 1, SingleShift: 2})
		splitter := codec.(data.Splitter)
		for _, text := range []string{"ıÁıÁ", strings.Repeat("ıÁ", 80), strings.Repeat("ı", 15) + "\r", strings.Repeat("Á", 8)} {
			for _, limit := range []uint{1, 2, 3, 7, 8, 9, 134, ^uint(0)} {
				parts, err := splitter.EncodeSplit(text, limit)
				if limit == 1 && (strings.Contains(text, "Á") || packed && strings.HasSuffix(text, "\r")) {
					if err == nil {
						t.Error("one byte fitted an extension or padded CR")
					}
					continue
				}
				if err != nil {
					t.Errorf("packed=%t text=%q limit=%d: %v", packed, text, limit, err)
					continue
				}
				var joined strings.Builder
				for _, part := range parts {
					if uint(len(part)) > limit {
						t.Errorf("packed=%t part exceeds %d: %x", packed, limit, part)
					}
					var decoded string
					if packed {
						septets := gsm7.Unpack7BitCR(part, 1)
						decoded, err = optionsCodec(t, data.GSM7Options{LockingShift: 1, SingleShift: 2}).Decode(septets)
					} else {
						decoded, err = codec.Decode(part)
					}
					if err != nil {
						t.Fatal(err)
					}
					joined.WriteString(decoded)
				}
				want := text
				if packed && limit == 3 && strings.HasSuffix(text, "\r") {
					// This fixture leaves CR alone in its last fill-one part;
					// inferred decoding retains the resulting doubled CR.
					want += "\r"
				}
				if joined.String() != want {
					t.Errorf("packed=%t limit=%d reassembled=%q; want %q", packed, limit, joined.String(), want)
				}
			}
		}
	}
}

func TestGSM7OptionsDefaultCompatibility(t *testing.T) {
	for _, packed := range []bool{false, true} {
		codec := optionsCodec(t, data.GSM7Options{Packed: packed})
		baseline := data.GSM7BIT
		if packed {
			baseline = data.GSM7BITPACKED
		}
		for n := 0; n <= 300; n++ {
			for _, unit := range []string{"A", "@", "\r", "^"} {
				text := strings.Repeat(unit, n)
				want, err := baseline.Encode(text)
				if err != nil {
					t.Fatal(err)
				}
				got, err := codec.Encode(text)
				if err != nil || !bytes.Equal(got, want) {
					t.Fatalf("packed=%t n=%d unit=%q encode=%x,%v; want %x", packed, n, unit, got, err, want)
				}
				expected, expectedErr := baseline.Decode(want)
				decoded, err := codec.Decode(want)
				if err != nil || expectedErr != nil || decoded != expected {
					t.Fatalf("packed=%t n=%d unit=%q decode=%q,%v; want %q,%v", packed, n, unit, decoded, err, expected, expectedErr)
				}
			}
		}
	}
	// Configured decoding follows the dependency's CR policy independently
	// of an unused high bit in the final octet.
	codec := optionsCodec(t, data.GSM7Options{Packed: true})
	wire, err := codec.Encode("AAAAAAA\r")
	if err != nil {
		t.Fatal(err)
	}
	wire[len(wire)-1] |= 0x80
	got, err := codec.Decode(wire)
	if err != nil || got != "AAAAAAA\r\r" {
		t.Errorf("standalone CR policy=%q,%v", got, err)
	}
	septets := gsm7.Unpack7BitCR(wire, 0)
	if !bytes.Equal(septets, []byte("AAAAAAA\r\r")) {
		t.Errorf("dependency CR policy=%x", septets)
	}
}

func TestGSM7OptionsConcurrentUse(t *testing.T) {
	codec := optionsCodec(t, data.GSM7Options{Packed: true, LockingShift: 1, SingleShift: 2})
	failures := make(chan error, 16)
	for worker := 0; worker < 16; worker++ {
		go func() {
			for n := 0; n < 100; n++ {
				wire, err := codec.Encode("ıÁ")
				if err != nil {
					failures <- err
					return
				}
				text, err := codec.Decode(wire)
				if err != nil || text != "ıÁ" {
					failures <- fmt.Errorf("concurrent decode=%q,%v", text, err)
					return
				}
			}
			failures <- nil
		}()
	}
	for worker := 0; worker < 16; worker++ {
		if err := <-failures; err != nil {
			t.Error(err)
		}
	}
}

func FuzzGSM7OptionsRoundTrip(f *testing.F) {
	for _, seed := range []struct {
		locking, single uint8
		packed          bool
		text            string
	}{
		{0, 0, false, "Hello €"}, {1, 2, true, "ıÁ"}, {0, 2, true, "ÁÓ"}, {3, 3, true, "êã"}, {12, 0, false, "€"}, {13, 13, true, "123"}, {1, 1, true, "AAAAAAA\r"},
	} {
		f.Add(seed.locking, seed.single, seed.packed, seed.text)
	}
	f.Fuzz(func(t *testing.T, locking, single uint8, packed bool, text string) {
		if len(text) > 64*1024 {
			t.Skip()
		}
		opts := data.GSM7Options{Packed: packed, LockingShift: int(locking), SingleShift: int(single)}
		codec, err := data.NewGSM7(opts)
		if err != nil {
			return
		}
		// Arbitrary received bytes must be safe under permissive decoding too.
		if _, err := codec.Decode([]byte(text)); err != nil {
			t.Fatalf("permissive decode: %v", err)
		}
		wire, err := codec.Encode(text)
		if err != nil {
			return
		}
		opts.Packed = false
		plain, err := data.NewGSM7(opts)
		if err != nil {
			t.Fatal(err)
		}
		septets, err := plain.Encode(text)
		if err != nil {
			t.Fatal(err)
		}
		if packed {
			wire = gsm7.Unpack7Bit(wire, 0)[:len(septets)]
		}
		decoded, err := plain.Decode(wire)
		if err != nil || decoded != text {
			t.Fatalf("locking=%d single=%d packed=%t got=%q,%v; want %q", locking, single, packed, decoded, err, text)
		}
	})
}
