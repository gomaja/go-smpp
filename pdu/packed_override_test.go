package pdu_test

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/gomaja/go-smpp/data"
	"github.com/gomaja/go-smpp/pdu"
	"github.com/gomaja/go-sms/encoding/gsm7"
)

// This alphabet deliberately exposes no Splitter or GSM7Options method.
type unpackedAlphabet struct{ data.EncDec }

func TestPackedDefaultOverrideRoundTrip(t *testing.T) {
	initial := data.GSM7BIT
	configured := func(opts data.GSM7Options) data.Encoding {
		enc, err := data.NewGSM7(opts)
		if err != nil {
			t.Fatal(err)
		}
		return enc
	}
	for _, tc := range []struct {
		name     string
		codec    data.Encoding
		alphabet data.Encoding
		unit     string
		nli      bool
	}{
		{"initial", initial, initial, "A£€@", false},
		{"legacy", data.GSM7BITPACKED, initial, "A£€@", false},
		{"configured", configured(data.GSM7Options{Packed: true}), initial, "A£€@", false},
		{"locking", configured(data.GSM7Options{Packed: true, LockingShift: 1}), configured(data.GSM7Options{LockingShift: 1}), "Ğ€@", true},
		{"single", configured(data.GSM7Options{Packed: true, SingleShift: 2}), configured(data.GSM7Options{SingleShift: 2}), "Á€@", true},
		{"custom", data.NewCustomEncoding(0, unpackedAlphabet{data.ASCII}), data.ASCII, "A`@", false},
		{"wrapped_packed", data.NewCustomEncoding(0, data.NewCustomEncoding(0, data.GSM7BITPACKED)), initial, "A£€@", false},
		{"wrapped_tables", data.NewCustomEncoding(0, configured(data.GSM7Options{Packed: true, LockingShift: 1, SingleShift: 2})), configured(data.GSM7Options{LockingShift: 1, SingleShift: 2}), "ĞÁ€@", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			saved := data.GSM7BIT
			t.Cleanup(func() { data.GSM7BIT = saved })
			data.GSM7BIT = tc.codec
			for _, sender := range []struct {
				name                   string
				codec                  data.Encoding
				alphabet               data.Encoding
				unit                   string
				nli, packed, automatic bool
			}{
				{"legacy", data.GSM7BITPACKED, tc.alphabet, tc.unit, tc.nli, true, tc.name != "initial" && tc.name != "custom"},
				{"wrapped_legacy", data.NewCustomEncoding(0, data.GSM7BITPACKED), tc.alphabet, tc.unit, tc.nli, true, tc.name != "initial" && tc.name != "custom"},
				{"explicit_plain", configured(data.GSM7Options{Packed: true}), initial, "Aè€@", false, true, false},
				{"explicit_nli", configured(data.GSM7Options{Packed: true, LockingShift: 1, SingleShift: 2}), configured(data.GSM7Options{LockingShift: 1, SingleShift: 2}), "ĞÁ€@", true, true, tc.name != "initial" && tc.name != "custom"},
				{"unpacked_nli", configured(data.GSM7Options{LockingShift: 1, SingleShift: 2}), configured(data.GSM7Options{LockingShift: 1, SingleShift: 2}), "ĞÁ€@", true, false, tc.name == "initial"},
			} {
				for _, repeat := range []int{16, 100} {
					for _, construction := range []string{"constructor", "setter", "short"} {
						t.Run(fmt.Sprintf("%s/%d/%s", sender.name, repeat, construction), func(t *testing.T) {
							if construction == "short" && repeat != 16 {
								t.Skip("short setter requires a single message")
							}
							text := strings.Repeat(sender.unit, repeat)
							var parts []*pdu.ShortMessage
							var err error
							switch construction {
							case "constructor":
								parts, err = pdu.NewLongMessageWithEncoding(text, sender.codec)
							case "short":
								var part pdu.ShortMessage
								part, err = pdu.NewShortMessageWithEncoding(text, sender.codec)
								parts = []*pdu.ShortMessage{&part}
							default:
								packet := pdu.NewSubmitSM().(*pdu.SubmitSM)
								packet.Message = pdu.ShortMessage{}
								err = packet.Message.SetLongMessageWithEnc(text, sender.codec)
								if err == nil {
									var packets []*pdu.SubmitSM
									packets, err = packet.Split()
									for _, part := range packets {
										parts = append(parts, &part.Message)
									}
								}
							}
							if err != nil {
								t.Fatal(err)
							}
							if (repeat == 16 && len(parts) != 1) || (repeat == 100 && len(parts) < 2) {
								t.Fatalf("parts=%d", len(parts))
							}
							var joined strings.Builder
							for _, part := range parts {
								local, err := part.GetMessage()
								if err != nil {
									t.Fatal(err)
								}
								septets, err := sender.alphabet.Encode(local)
								if err != nil {
									t.Fatal(err)
								}
								header, err := part.UDH().MarshalBinary()
								if err != nil {
									t.Fatal(err)
								}
								want := septets
								if sender.packed {
									want, err = gsm7.Pack7BitCR(septets, (7-len(header)*8%7)%7)
								}
								raw, rawErr := part.GetMessageData()
								if err != nil || rawErr != nil || !bytes.Equal(raw, want) {
									t.Fatalf("payload=%x,%v; want %x,%v", raw, rawErr, want, err)
								}
								hasNLI := false
								for _, ie := range part.UDH() {
									if ie.ID == 0x24 || ie.ID == 0x25 {
										hasNLI = true
									}
								}
								if hasNLI != sender.nli {
									t.Fatalf("NLI=%v; want %v", hasNLI, sender.nli)
								}
								packet := pdu.NewSubmitSM().(*pdu.SubmitSM)
								packet.Message = *part
								if len(header) > 0 {
									packet.EsmClass = data.SM_UDH_GSM
								}
								received := packedReviewReceived(t, packet)
								got, err := received.GetMessageWithEncoding(sender.codec)
								if err != nil || got != local {
									t.Fatalf("parsed=%q,%v; want %q", got, err, local)
								}
								if sender.automatic {
									got, err = received.GetMessage()
									if err != nil || got != local {
										t.Fatalf("GetMessage=%q,%v; want %q", got, err, local)
									}
								}
								joined.WriteString(got)
							}
							if joined.String() != text {
								t.Fatalf("round trip=%q; want %q", joined.String(), text)
							}
						})
					}
				}
			}
		})
	}
}

func TestPackedDefaultOverrideObservation(t *testing.T) {
	saved := data.GSM7BIT
	t.Cleanup(func() { data.GSM7BIT = saved })
	data.GSM7BIT = data.GSM7BITPACKED
	for _, size := range []int{80, 320} {
		text := strings.Repeat("A", size)
		for _, constructor := range []func(string) ([]*pdu.ShortMessage, error){
			pdu.NewLongMessage,
			func(s string) ([]*pdu.ShortMessage, error) {
				return pdu.NewLongMessageWithEncoding(s, data.FindEncoding(s))
			},
		} {
			parts, err := constructor(text)
			if err != nil {
				t.Fatal(err)
			}
			var joined strings.Builder
			for _, part := range parts {
				packet := pdu.NewSubmitSM().(*pdu.SubmitSM)
				packet.Message = *part
				if len(part.UDH()) > 0 {
					packet.EsmClass = data.SM_UDH_GSM
				}
				got, err := packedReviewReceived(t, packet).GetMessage()
				if err != nil {
					t.Fatal(err)
				}
				joined.WriteString(got)
			}
			if joined.String() != text {
				t.Fatalf("size=%d round trip=%q", size, joined.String())
			}
		}
	}
	message, err := pdu.NewShortMessage("AAAAAAAA")
	if err != nil {
		t.Fatal(err)
	}
	packet := pdu.NewSubmitSM().(*pdu.SubmitSM)
	packet.Message = message
	got, err := packedReviewReceived(t, packet).GetMessage()
	if err != nil || got != "AAAAAAAA" {
		t.Fatalf("default short round trip=%q,%v", got, err)
	}
}

func TestPackedDefaultMissingNLIUsesDefaultTables(t *testing.T) {
	saved := data.GSM7BIT
	t.Cleanup(func() { data.GSM7BIT = saved })
	codec, err := data.NewGSM7(data.GSM7Options{Packed: true, LockingShift: 1, SingleShift: 2})
	if err != nil {
		t.Fatal(err)
	}
	data.GSM7BIT = codec
	// TS 23.038 §§6.2.1.2.4–5: absent received NLI IEs select defaults.
	// Septet 04 is è in the default table and € in Turkish locking.
	for _, enc := range []data.Encoding{codec, data.GSM7BITPACKED} {
		message, err := pdu.NewBinaryShortMessageWithEncoding([]byte{4}, enc)
		if err != nil {
			t.Fatal(err)
		}
		packet := pdu.NewSubmitSM().(*pdu.SubmitSM)
		packet.Message = message
		got, err := packedReviewReceived(t, packet).GetMessageWithEncoding(enc)
		if err != nil || got != "è" {
			t.Errorf("absent NLI=%q,%v; want è", got, err)
		}
	}
}

func TestPackedDefaultNilEncoding(t *testing.T) {
	initial := data.GSM7BIT
	codec, err := data.NewGSM7(data.GSM7Options{Packed: true, LockingShift: 1, SingleShift: 2})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		enc  data.Encoding
		unit string
	}{
		{"initial", initial, "A£€@"},
		{"packed", data.GSM7BITPACKED, "A£€@"},
		{"tables", codec, "ĞÁ€@"},
		{"wrapped_tables", data.NewCustomEncoding(0, codec), "ĞÁ€@"},
		{"custom", data.NewCustomEncoding(0, data.ASCII), "A`@"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			saved := data.GSM7BIT
			t.Cleanup(func() { data.GSM7BIT = saved })
			data.GSM7BIT = tc.enc
			for _, repeat := range []int{16, 100} {
				text := strings.Repeat(tc.unit, repeat)
				for _, constructor := range []func(string) ([]*pdu.ShortMessage, error){
					pdu.NewLongMessage,
					func(s string) ([]*pdu.ShortMessage, error) { return pdu.NewLongMessageWithEncoding(s, nil) },
				} {
					parts, err := constructor(text)
					if err != nil {
						t.Fatal(err)
					}
					if (repeat == 16 && len(parts) != 1) || (repeat == 100 && len(parts) < 2) {
						t.Fatalf("parts=%d", len(parts))
					}
					var joined strings.Builder
					for _, part := range parts {
						packet := pdu.NewSubmitSM().(*pdu.SubmitSM)
						packet.Message = *part
						if len(part.UDH()) > 0 {
							packet.EsmClass = data.SM_UDH_GSM
						}
						got, err := packedReviewReceived(t, packet).GetMessage()
						if err != nil {
							t.Fatal(err)
						}
						joined.WriteString(got)
					}
					if joined.String() != text {
						t.Errorf("default round trip=%q; want %q", joined.String(), text)
					}
				}
			}
		})
	}
}

func TestPackedDefaultNilRawSplit(t *testing.T) {
	saved := data.GSM7BIT
	t.Cleanup(func() { data.GSM7BIT = saved })
	data.GSM7BIT = data.GSM7BITPACKED
	text := strings.Repeat("A", 170)
	raw, err := data.GSM7BITPACKED.Encode(text)
	if err != nil {
		t.Fatal(err)
	}
	packet := pdu.NewSubmitSM().(*pdu.SubmitSM)
	if err := packet.Message.SetMessageDataWithEncoding(raw, nil); err != nil {
		t.Fatal(err)
	}
	if !packet.ShouldSplit() {
		t.Fatal("packed raw default ignored the 140-octet budget")
	}
	parts, err := packet.Split()
	if err != nil || len(parts) != 2 {
		t.Fatalf("split=%d,%v", len(parts), err)
	}
	var joined strings.Builder
	for _, part := range parts {
		got, err := packedReviewReceived(t, part).GetMessage()
		if err != nil {
			t.Fatal(err)
		}
		joined.WriteString(got)
	}
	if joined.String() != text {
		t.Fatalf("raw default=%q; want %q", joined.String(), text)
	}
}
