package data_test

import (
	"bytes"
	"testing"

	"github.com/gomaja/go-smpp/data"
)

func TestPackedDefaultDecodeAndSelection(t *testing.T) {
	initial := data.GSM7BIT
	for _, tc := range []struct {
		name string
		opts data.GSM7Options
		text string
	}{
		{"default", data.GSM7Options{Packed: true}, "A£€@"},
		{"locking", data.GSM7Options{Packed: true, LockingShift: 1}, "Ğ€@"},
		{"single", data.GSM7Options{Packed: true, SingleShift: 2}, "Á€@"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			saved := data.GSM7BIT
			t.Cleanup(func() { data.GSM7BIT = saved })
			configured, err := data.NewGSM7(tc.opts)
			if err != nil {
				t.Fatal(err)
			}
			for _, replacement := range []data.Encoding{configured, data.NewCustomEncoding(0, configured)} {
				data.GSM7BIT = replacement
				wire, err := replacement.Encode(tc.text)
				if err != nil {
					t.Fatal(err)
				}
				if data.FindEncoding("A€") != replacement {
					t.Error("FindEncoding did not retain the packed candidate")
				}
				if data.FindEncoding("Ğ") != data.UCS2 {
					t.Error("FindEncoding selected national language tables")
				}
				got, err := data.FromDataCoding(0).Decode(wire)
				if err != nil || got != tc.text {
					t.Errorf("FromDataCoding(0)=%q,%v; want %q", got, err, tc.text)
				}
				got, err = data.DecodeGSM7Packed(wire, 0, -1)
				if err != nil || got != tc.text {
					t.Errorf("DecodeGSM7Packed=%q,%v; want %q", got, err, tc.text)
				}
			}
		})
	}
	t.Run("legacy", func(t *testing.T) {
		saved := data.GSM7BIT
		t.Cleanup(func() { data.GSM7BIT = saved })
		data.GSM7BIT = data.GSM7BITPACKED
		// Independent packed vector for eight A septets, TS 23.038 §6.1.2.1.1.
		wire := []byte{0xc1, 0x60, 0x30, 0x18, 0x0c, 0x06, 0x83}
		for _, count := range []int{-1, 8} {
			got, err := data.DecodeGSM7Packed(wire, 0, count)
			if err != nil || got != "AAAAAAAA" {
				t.Errorf("count=%d: %q,%v", count, got, err)
			}
		}
		got, err := data.FromDataCoding(0).Decode(wire)
		if err != nil || got != "AAAAAAAA" {
			t.Errorf("FromDataCoding(0)=%q,%v", got, err)
		}
		enc := data.FindEncoding("£€")
		if enc != data.GSM7BIT {
			t.Fatalf("FindEncoding returned %T", enc)
		}
		raw, err := enc.Encode("£€")
		want, wantErr := data.GSM7BITPACKED.Encode("£€")
		if err != nil || wantErr != nil || !bytes.Equal(raw, want) {
			t.Fatalf("selection wire=%x,%v; want %x,%v", raw, err, want, wantErr)
		}
		if data.FindEncoding("😀") != data.UCS2 {
			t.Fatal("unrepresentable text did not select UCS2")
		}
	})
	if data.GSM7BIT != initial {
		t.Fatal("subtest did not restore GSM7BIT")
	}
}

func TestPackedDefaultCodingRetainsWireValue(t *testing.T) {
	saved := data.GSM7BIT
	t.Cleanup(func() { data.GSM7BIT = saved })
	data.GSM7BIT = data.ASCII
	enc := data.FromDataCoding(0)
	if enc.DataCoding() != 0 {
		t.Errorf("coding=%02x; want 00", enc.DataCoding())
	}
	got, err := enc.Decode([]byte{'`', '@'})
	if err != nil || got != "`@" {
		t.Fatalf("custom alphabet=%q,%v", got, err)
	}
}

func TestUnpackedGSM7PreservesAlphabet(t *testing.T) {
	saved := data.GSM7BIT
	t.Cleanup(func() { data.GSM7BIT = saved })
	data.GSM7BIT = data.ASCII
	for _, tc := range []struct {
		opts data.GSM7Options
		text string
		want []byte
	}{
		{data.GSM7Options{Packed: true}, "@€", []byte{0, 0x1b, 0x65}},
		{data.GSM7Options{Packed: true, LockingShift: 1, SingleShift: 2}, "ĞÁ", []byte{0x0b, 0x1b, 0x41}},
		{data.GSM7Options{LockingShift: 1}, "Ğ", []byte{0x0b}},
	} {
		enc, err := data.NewGSM7(tc.opts)
		if err != nil {
			t.Fatal(err)
		}
		for _, wrapped := range []data.Encoding{enc, data.NewCustomEncoding(0, data.NewCustomEncoding(0, enc))} {
			alphabet, err := data.UnpackedGSM7(wrapped)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := alphabet.Encode(tc.text)
			if err != nil || !bytes.Equal(raw, tc.want) {
				t.Errorf("unpacked=%x,%v; want %x", raw, err, tc.want)
			}
			got, err := alphabet.Decode(tc.want)
			if err != nil || got != tc.text {
				t.Errorf("decoded=%q,%v; want %q", got, err, tc.text)
			}
		}
		if got := enc.(interface{ GSM7Options() data.GSM7Options }).GSM7Options(); got != tc.opts {
			t.Fatal("resolution changed caller options")
		}
	}
	for _, enc := range []data.Encoding{nil, data.GSM7BITPACKED, data.NewCustomEncoding(0, data.GSM7BITPACKED)} {
		alphabet, err := data.UnpackedGSM7(enc)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := alphabet.Encode("@")
		if err != nil || !bytes.Equal(raw, []byte{0}) {
			t.Errorf("legacy resolution=%x,%v", raw, err)
		}
	}
	alphabet, err := data.UnpackedGSM7(data.ASCII)
	if err != nil || alphabet != data.ASCII {
		t.Fatalf("custom alphabet changed: %T,%v", alphabet, err)
	}
}

func TestNilGSM7DefaultLookup(t *testing.T) {
	saved := data.GSM7BIT
	t.Cleanup(func() { data.GSM7BIT = saved })
	data.GSM7BIT = nil
	for code := range 256 {
		enc := data.FromDataCoding(byte(code))
		if enc == nil || enc.DataCoding() != byte(code) {
			t.Fatalf("coding %02x: %T", code, enc)
		}
	}
	for _, enc := range []data.Encoding{data.FromDataCoding(0), data.FromDataCoding(0x10), data.FindEncoding("AAAAAAAA")} {
		raw, err := enc.Encode("AAAAAAAA")
		if err != nil || !bytes.Equal(raw, []byte("AAAAAAAA")) {
			t.Fatalf("nil default Encode=%x,%v", raw, err)
		}
		got, err := enc.Decode(raw)
		if err != nil || got != "AAAAAAAA" {
			t.Fatalf("nil default Decode=%q,%v", got, err)
		}
	}
	if data.FindEncoding("😀") != data.UCS2 {
		t.Fatal("nil default selected GSM for unrepresentable text")
	}
	got, err := data.DecodeGSM7Packed([]byte{0xc1, 0x60, 0x30, 0x18, 0x0c, 0x06, 0x83}, 0, 8)
	if err != nil || got != "AAAAAAAA" {
		t.Fatalf("nil packed helper=%q,%v", got, err)
	}
}
