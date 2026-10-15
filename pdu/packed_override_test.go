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
		name            string
		codec           data.Encoding
		alphabet        data.Encoding
		unit            string
		locking, single int
		packed          bool
	}{
		{"initial", initial, initial, "A£€@", 0, 0, false},
		{"legacy", data.GSM7BITPACKED, initial, "A£€@", 0, 0, true},
		{"configured", configured(data.GSM7Options{Packed: true}), initial, "A£€@", 0, 0, true},
		{"locking", configured(data.GSM7Options{Packed: true, LockingShift: 1}), configured(data.GSM7Options{LockingShift: 1}), "Ğ€@", 1, 0, true},
		{"single", configured(data.GSM7Options{Packed: true, SingleShift: 2}), configured(data.GSM7Options{SingleShift: 2}), "Á€@", 0, 2, true},
		{"custom", data.NewCustomEncoding(0, unpackedAlphabet{data.ASCII}), data.ASCII, "A`@", 0, 0, false},
		{"wrapped_packed", data.NewCustomEncoding(0, data.NewCustomEncoding(0, data.GSM7BITPACKED)), initial, "A£€@", 0, 0, true},
		{"wrapped_tables", data.NewCustomEncoding(0, configured(data.GSM7Options{Packed: true, LockingShift: 1, SingleShift: 2})), configured(data.GSM7Options{LockingShift: 1, SingleShift: 2}), "ĞÁ€@", 1, 2, true},
		{"unpacked_locking", configured(data.GSM7Options{LockingShift: 1}), configured(data.GSM7Options{LockingShift: 1}), "Ğ€@", 1, 0, false},
		{"unpacked_single", configured(data.GSM7Options{SingleShift: 2}), configured(data.GSM7Options{SingleShift: 2}), "Á€@", 0, 2, false},
		{"unpacked_both", configured(data.GSM7Options{LockingShift: 1, SingleShift: 2}), configured(data.GSM7Options{LockingShift: 1, SingleShift: 2}), "ĞÁ€@", 1, 2, false},
		{"unpacked_wrapped", data.NewCustomEncoding(0, configured(data.GSM7Options{LockingShift: 1, SingleShift: 2})), configured(data.GSM7Options{LockingShift: 1, SingleShift: 2}), "ĞÁ€@", 1, 2, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			saved := data.GSM7BIT
			t.Cleanup(func() { data.GSM7BIT = saved })
			data.GSM7BIT = tc.codec
			for _, sender := range []struct {
				name              string
				codec             data.Encoding
				alphabet          data.Encoding
				unit              string
				locking, single   int
				packed, automatic bool
			}{
				{"legacy", data.GSM7BITPACKED, tc.alphabet, tc.unit, tc.locking, tc.single, true, tc.packed},
				{"wrapped_legacy", data.NewCustomEncoding(0, data.GSM7BITPACKED), tc.alphabet, tc.unit, tc.locking, tc.single, true, tc.packed},
				{"explicit_plain", configured(data.GSM7Options{Packed: true}), initial, "Aè€@", 0, 0, true, false},
				{"explicit_nli", configured(data.GSM7Options{Packed: true, LockingShift: 1, SingleShift: 2}), configured(data.GSM7Options{LockingShift: 1, SingleShift: 2}), "ĞÁ€@", 1, 2, true, tc.packed},
				{"unpacked_nli", configured(data.GSM7Options{LockingShift: 1, SingleShift: 2}), configured(data.GSM7Options{LockingShift: 1, SingleShift: 2}), "ĞÁ€@", 1, 2, false, !tc.packed && tc.name != "custom"},
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
								checkOverrideNLI(t, part, sender.locking, sender.single)
								packet := pdu.NewSubmitSM().(*pdu.SubmitSM)
								packet.Message = *part
								if len(header) > 0 {
									packet.EsmClass = data.SM_UDH_GSM
								}
								received := packedReviewReceived(t, packet)
								checkOverrideNLI(t, received, sender.locking, sender.single)
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

func TestRawNilEncodingSelectionBeforeAndAfterSplit(t *testing.T) {
	initial := data.GSM7BIT
	configured, err := data.NewGSM7(data.GSM7Options{LockingShift: 1, SingleShift: 2})
	if err != nil {
		t.Fatal(err)
	}
	configuredPacked, err := data.NewGSM7(data.GSM7Options{Packed: true, LockingShift: 1, SingleShift: 2})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		enc  data.Encoding
	}{
		{"initial", initial}, {"ASCII", data.ASCII}, {"packed", data.GSM7BITPACKED},
		{"configured", configured}, {"configured_packed", configuredPacked},
		{"wrapped", data.NewCustomEncoding(0, configured)}, {"nil", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			saved := data.GSM7BIT
			t.Cleanup(func() { data.GSM7BIT = saved })
			data.GSM7BIT = tc.enc
			effective := data.DefaultGSM7()
			for _, constructor := range []bool{false, true} {
				for _, count := range []int{0, 1, 170} {
					text := strings.Repeat("A", count)
					raw, err := effective.Encode(text)
					if err != nil {
						t.Fatal(err)
					}
					var message pdu.ShortMessage
					if constructor {
						message, err = pdu.NewBinaryShortMessageWithEncoding(raw, nil)
					} else {
						err = message.SetMessageDataWithEncoding(raw, nil)
					}
					if err != nil {
						t.Fatal(err)
					}
					if message.Encoding() != nil {
						t.Error("raw setter changed nil encoding")
					}
					got, err := message.GetMessage()
					if err != nil || got != text {
						t.Fatalf("raw text=%q,%v; want %q", got, err, text)
					}
					wire := pdu.NewBuffer(nil)
					if err := message.Marshal(wire); err != nil {
						t.Fatal(err)
					}
					want := append([]byte{0, 0, byte(len(raw))}, raw...)
					if !bytes.Equal(wire.Bytes(), want) {
						t.Errorf("raw wire=%x; want %x", wire.Bytes(), want)
					}
					packet := pdu.NewSubmitSM().(*pdu.SubmitSM)
					packet.Message = message
					parts, err := packet.Split()
					if err != nil {
						t.Fatal(err)
					}
					if len(parts) == 1 {
						if parts[0] != packet || packet.Message.Encoding() != effective {
							t.Error("single part did not resolve the source encoding")
						}
					} else if packet.Message.Encoding() != nil {
						t.Error("multipart split changed the source encoding")
					}
					var joined strings.Builder
					for _, part := range parts {
						if part.Message.Encoding() != effective {
							t.Fatal("split part did not retain the effective default")
						}
						wire := pdu.NewBuffer(nil)
						if err := part.Message.Marshal(wire); err != nil {
							t.Fatal(err)
						}
						if wire.Bytes()[0] != effective.DataCoding() {
							t.Errorf("split coding=%02x; want %02x", wire.Bytes()[0], effective.DataCoding())
						}
						local, err := part.Message.GetMessageWithEncoding(part.Message.Encoding())
						if err != nil {
							t.Fatal(err)
						}
						got, err := packedReviewReceived(t, part).GetMessage()
						if err != nil || got != local {
							t.Fatalf("parsed text=%q,%v; local=%q", got, err, local)
						}
						joined.WriteString(got)
					}
					if joined.String() != text {
						t.Fatalf("split text=%q; want %q", joined.String(), text)
					}
				}
			}
		})
	}
}

func TestNilGSM7DefaultConstructors(t *testing.T) {
	saved := data.GSM7BIT
	t.Cleanup(func() { data.GSM7BIT = saved })
	data.GSM7BIT = nil
	for _, count := range []int{8, 320} {
		text := strings.Repeat("A", count)
		for _, constructor := range []func(string) ([]*pdu.ShortMessage, error){
			pdu.NewLongMessage,
			func(s string) ([]*pdu.ShortMessage, error) { return pdu.NewLongMessageWithEncoding(s, nil) },
			func(s string) ([]*pdu.ShortMessage, error) {
				var m pdu.ShortMessage
				if err := m.SetLongMessageWithEnc(s, nil); err != nil {
					return nil, err
				}
				packet := pdu.NewSubmitSM().(*pdu.SubmitSM)
				packet.Message = m
				packets, err := packet.Split()
				var parts []*pdu.ShortMessage
				for _, part := range packets {
					parts = append(parts, &part.Message)
				}
				return parts, err
			},
		} {
			parts, err := constructor(text)
			if err != nil {
				t.Fatal(err)
			}
			if (count == 8 && len(parts) != 1) || (count == 320 && len(parts) < 2) {
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
				t.Fatalf("nil default text=%q; want %q", joined.String(), text)
			}
		}
	}
	for _, constructor := range []func(string) (pdu.ShortMessage, error){
		pdu.NewShortMessage,
		func(s string) (pdu.ShortMessage, error) { return pdu.NewShortMessageWithEncoding(s, nil) },
		func(s string) (pdu.ShortMessage, error) {
			return pdu.NewShortMessageWithEncoding(s, data.GSM7BITPACKED)
		},
	} {
		message, err := constructor("AAAAAAAA")
		if err != nil {
			t.Fatal(err)
		}
		got, err := message.GetMessage()
		if err != nil || got != "AAAAAAAA" {
			t.Fatalf("nil default short=%q,%v", got, err)
		}
	}
	message, err := pdu.NewBinaryShortMessageWithEncoding([]byte{'A'}, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := message.GetMessage()
	if err != nil || got != "A" || message.Encoding() != nil {
		t.Fatalf("nil raw default=%q,%v enc=%T", got, err, message.Encoding())
	}
}

func checkOverrideNLI(t *testing.T, message *pdu.ShortMessage, locking, single int) {
	t.Helper()
	seen := map[byte]int{}
	for _, ie := range message.UDH() {
		var want int
		switch ie.ID {
		case 0x24:
			want = single
		case 0x25:
			want = locking
		default:
			continue
		}
		if want == 0 || len(ie.Data) != 1 || int(ie.Data[0]) != want {
			t.Errorf("NLI %02x=%x; want %d", ie.ID, ie.Data, want)
		}
		seen[ie.ID]++
	}
	for id, value := range map[byte]int{0x24: single, 0x25: locking} {
		count := 0
		if value != 0 {
			count = 1
		}
		if seen[id] != count {
			t.Errorf("NLI %02x count=%d; want %d with value %d", id, seen[id], count, value)
		}
	}
}

func TestRawNilEncodingPreservesReceivedNLI(t *testing.T) {
	saved := data.GSM7BIT
	t.Cleanup(func() { data.GSM7BIT = saved })
	codec, err := data.NewGSM7(data.GSM7Options{LockingShift: 1})
	if err != nil {
		t.Fatal(err)
	}
	data.GSM7BIT = codec
	message, err := pdu.NewBinaryShortMessageWithEncoding([]byte{4}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Raw bytes carry Portuguese locking selection, independently of the
	// current Turkish default (TS 23.040 §9.2.3.24.16).
	if err := message.SetUDH(pdu.UDH{{ID: 0x25, Data: []byte{3}}}); err != nil {
		t.Fatal(err)
	}
	if message.Encoding() != nil {
		t.Fatal("UDH setter changed nil encoding")
	}
	packet := pdu.NewSubmitSM().(*pdu.SubmitSM)
	packet.Message = message
	got, err := packedReviewReceived(t, packet).GetMessage()
	if err != nil || got != "ê" {
		t.Fatalf("received NLI=%q,%v; want ê", got, err)
	}
}
