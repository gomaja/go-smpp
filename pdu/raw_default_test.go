package pdu_test

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/gomaja/go-smpp/data"
	"github.com/gomaja/go-smpp/pdu"
)

func TestRawNilSplitWireCompatibility(t *testing.T) {
	saved := data.GSM7BIT
	t.Cleanup(func() { data.GSM7BIT = saved })
	// Keep this fixture independent of the mutable default's current value.
	codec, err := data.NewGSM7(data.GSM7Options{})
	if err != nil {
		t.Fatal(err)
	}
	data.GSM7BIT = codec
	for _, count := range []int{1, 170} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			packet := pdu.NewSubmitSM().(*pdu.SubmitSM)
			if err := packet.Message.SetMessageDataWithEncoding(bytes.Repeat([]byte{'A'}, count), nil); err != nil {
				t.Fatal(err)
			}
			parts, err := packet.Split()
			if err != nil {
				t.Fatal(err)
			}
			lengths := []int{1}
			if count == 170 {
				lengths = []int{153, 17}
			}
			if len(parts) != len(lengths) {
				t.Fatalf("parts=%d; want %d", len(parts), len(lengths))
			}
			for i, part := range parts {
				var header []byte
				if count == 170 {
					_, _, reference, found := part.Message.UDH().GetConcatInfo()
					if !found {
						t.Fatal("missing concatenation header")
					}
					header = []byte{5, 0, 3, byte(reference), 2, byte(i + 1)}
				}
				want := append([]byte{0, 0, byte(len(header) + lengths[i])}, header...)
				want = append(want, bytes.Repeat([]byte{'A'}, lengths[i])...)
				wire := pdu.NewBuffer(nil)
				if err := part.Message.Marshal(wire); err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(wire.Bytes(), want) {
					t.Fatalf("part %d wire=%x; want %x", i, wire.Bytes(), want)
				}
				got, err := part.Message.GetMessageWithEncoding(part.Message.Encoding())
				if err != nil || got != strings.Repeat("A", lengths[i]) {
					t.Fatalf("part %d text=%q,%v", i, got, err)
				}
			}
		})
	}
}

func TestGetMessageWithNilEncoding(t *testing.T) {
	initial := data.GSM7BIT
	configured := func(packed bool) data.Encoding {
		codec, err := data.NewGSM7(data.GSM7Options{Packed: packed, LockingShift: 1, SingleShift: 2})
		if err != nil {
			t.Fatal(err)
		}
		return codec
	}
	for _, tc := range []struct {
		name   string
		codec  data.Encoding
		packed bool
	}{
		{"initial", initial, false},
		{"packed", data.GSM7BITPACKED, true},
		{"configured_packed", configured(true), true},
		{"configured_unpacked", configured(false), false},
		{"wrapped", data.NewCustomEncoding(0, configured(false)), false},
		{"ASCII", data.ASCII, false},
		{"nil", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			saved := data.GSM7BIT
			t.Cleanup(func() { data.GSM7BIT = saved })
			data.GSM7BIT = tc.codec
			rawCodec := initial
			if tc.packed {
				rawCodec = data.GSM7BITPACKED
			} else if tc.name == "ASCII" {
				rawCodec = data.ASCII
			}
			for _, count := range []int{0, 1, 170} {
				t.Run(fmt.Sprint(count), func(t *testing.T) {
					text := strings.Repeat("A", count)
					if count > 0 {
						if tc.name == "ASCII" {
							text += "`@"
						} else {
							text += "£€@"
						}
					}
					raw, err := rawCodec.Encode(text)
					if err != nil {
						t.Fatal(err)
					}
					for _, stored := range []data.Encoding{nil, data.BINARY8BIT2} {
						message, err := pdu.NewBinaryShortMessageWithEncoding(raw, stored)
						if err != nil {
							t.Fatal(err)
						}
						got, err := message.GetMessageWithEncoding(nil)
						if err != nil || got != text {
							t.Fatalf("nil decode=%q,%v; want %q", got, err, text)
						}
						if message.Encoding() != stored {
							t.Fatal("decoding changed stored encoding")
						}
					}
				})
			}
			// A nil argument selects the current default, independently of the
			// stored codec. Received NLI IEs still select the decoding tables.
			if tc.name != "ASCII" {
				message, err := pdu.NewShortMessageWithEncoding("ĞÁ", configured(tc.packed))
				if err != nil {
					t.Fatal(err)
				}
				packet := pdu.NewSubmitSM().(*pdu.SubmitSM)
				packet.Message, packet.EsmClass = message, data.SM_UDH_GSM
				received := packedReviewReceived(t, packet)
				got, err := received.GetMessageWithEncoding(nil)
				if err != nil || got != "ĞÁ" {
					t.Fatalf("nil decode with NLI=%q,%v", got, err)
				}
				// Absent received NLI IEs select the default tables, even
				// when the effective codec has configured national tables.
				plain, err := pdu.NewBinaryShortMessageWithEncoding([]byte{4}, nil)
				if err != nil {
					t.Fatal(err)
				}
				got, err = plain.GetMessageWithEncoding(nil)
				if err != nil || got != "è" {
					t.Fatalf("nil decode without NLI=%q,%v; want è", got, err)
				}
			}
		})
	}
}
