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

func packedReviewMessage(t *testing.T, packet pdu.PDU) *pdu.ShortMessage {
	t.Helper()
	switch v := packet.(type) {
	case *pdu.SubmitSM:
		return &v.Message
	case *pdu.DeliverSM:
		return &v.Message
	default:
		t.Fatalf("unexpected PDU %T", packet)
		return nil
	}
}

func packedReviewReceived(t *testing.T, packet pdu.PDU) *pdu.ShortMessage {
	t.Helper()
	wire := pdu.NewBuffer(nil)
	packet.Marshal(wire)
	if err := wire.Err(); err != nil {
		t.Fatal(err)
	}
	received, err := pdu.Parse(bytes.NewReader(wire.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	return packedReviewMessage(t, received)
}

func TestLegacyPackedSettersUseSelectedDefaultAlphabet(t *testing.T) {
	saved := data.GSM7BIT
	t.Cleanup(func() { data.GSM7BIT = saved })
	data.GSM7BIT = data.ASCII
	legacy := data.GSM7BITPACKED
	for name, codec := range map[string]data.Encoding{
		"legacy": legacy,
		"custom": data.NewCustomEncoding(0, legacy),
		"nested": data.NewCustomEncoding(0, data.NewCustomEncoding(0, legacy)),
	} {
		for _, long := range []bool{false, true} {
			for _, command := range []string{"submit", "deliver"} {
				for _, text := range []string{"@", "`"} {
					t.Run(fmt.Sprintf("%s/long%v/%s/%s", name, long, command, text), func(t *testing.T) {
						packet := pdu.NewSubmitSM()
						if command == "deliver" {
							packet = pdu.NewDeliverSM()
						}
						message := packedReviewMessage(t, packet)
						var err error
						if long {
							err = message.SetLongMessageWithEnc(text, codec)
						} else {
							err = message.SetMessageWithEncoding(text, codec)
						}
						if err != nil {
							t.Fatal(err)
						}
						raw, err := message.GetMessageData()
						if err != nil || !bytes.Equal(raw, []byte(text)) {
							t.Fatalf("wire=%x,%v; want ASCII %x", raw, err, text)
						}
						got, err := packedReviewReceived(t, packet).GetMessageWithEncoding(codec)
						if err != nil || got != text {
							t.Fatalf("received=%q,%v; want%q", got, err, text)
						}
					})
				}
			}
		}
		t.Run(name+"/multipart", func(t *testing.T) {
			packet := pdu.NewSubmitSM().(*pdu.SubmitSM)
			text := strings.Repeat("`", 300)
			if err := packet.Message.SetLongMessageWithEnc(text, codec); err != nil {
				t.Fatal(err)
			}
			parts, err := packet.Split()
			if err != nil {
				t.Fatal(err)
			}
			var joined strings.Builder
			for _, part := range parts {
				got, err := packedReviewReceived(t, part).GetMessageWithEncoding(codec)
				if err != nil {
					t.Fatal(err)
				}
				joined.WriteString(got)
			}
			if joined.String() != text {
				t.Fatalf("joined=%q; want%q", joined.String(), text)
			}
		})
	}
}

func TestConfiguredPackedSettersKeepExplicitTables(t *testing.T) {
	saved := data.GSM7BIT
	t.Cleanup(func() { data.GSM7BIT = saved })
	data.GSM7BIT = data.ASCII
	for name, opts := range map[string]data.GSM7Options{
		"default": {Packed: true}, "Turkish": {Packed: true, LockingShift: 1},
	} {
		t.Run(name, func(t *testing.T) {
			codec, err := data.NewGSM7(opts)
			if err != nil {
				t.Fatal(err)
			}
			for _, enc := range []data.Encoding{codec, data.NewCustomEncoding(0, codec)} {
				m, err := pdu.NewShortMessageWithEncoding("@", enc)
				if err != nil {
					t.Fatal(err)
				}
				raw, err := m.GetMessageData()
				want := []byte{0}
				if opts.LockingShift != 0 {
					want = []byte{0, 0}
				} // H4/fill3.
				if err != nil || !bytes.Equal(raw, want) {
					t.Fatalf("explicit wire=%x,%v; want%x", raw, err, want)
				}
				if err := m.SetMessageWithEncoding("`", enc); err == nil {
					t.Fatal("explicit GSM tables accepted backtick")
				}
			}
		})
	}
}

// Replace the selector with a same-sized Filler IE, preserving header alignment
// (TS 23.040 §9.2.3.24.17). Absent IEs select defaults (§9.2.3.24.15–16).
func packedReviewRemoveSelector(t *testing.T, message *pdu.ShortMessage, id byte) {
	t.Helper()
	for i, ie := range message.UDH() {
		if ie.ID == id {
			message.UDH()[i] = pdu.InfoElement{ID: 0x26, Data: []byte{0}}
			return
		}
	}
	t.Fatalf("missing fixture selector%02x", id)
}

func TestPackedCacheRejectsMissingSelector(t *testing.T) {
	for _, tc := range []struct {
		name       string
		id         byte
		text, want string
	}{
		// TS 23.038 Annex A.3.1: Turkish locking 0b=Ğ; default 0b=Ø.
		{"locking", 0x25, "Ğ", "Ø"},
		// Annex A.2.1: Turkish ESC 47=Ğ; default unassigned extension falls back to G.
		{"single", 0x24, "Ğ", "G"},
	} {
		for _, packed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/packed%v", tc.name, packed), func(t *testing.T) {
				opts := data.GSM7Options{Packed: packed, LockingShift: 1, SingleShift: 1}
				if tc.id == 0x24 {
					opts.LockingShift = 0
				}
				codec, err := data.NewGSM7(opts)
				if err != nil {
					t.Fatal(err)
				}
				packet := pdu.NewDeliverSM().(*pdu.DeliverSM)
				packet.EsmClass = data.SM_UDH_GSM
				if err := packet.Message.SetMessageWithEncoding(tc.text, codec); err != nil {
					t.Fatal(err)
				}
				packedReviewRemoveSelector(t, &packet.Message, tc.id)
				for _, message := range []*pdu.ShortMessage{&packet.Message, packedReviewReceived(t, packet)} {
					got, err := message.GetMessageWithEncoding(codec)
					if err != nil || got != tc.want {
						t.Errorf("GetMessageWithEncoding=%q,%v; want%q", got, err, tc.want)
					}
				}
				got, err := packet.Message.GetMessage()
				if err != nil || got != tc.want {
					t.Errorf("GetMessage=%q,%v; want%q", got, err, tc.want)
				}
			})
		}
	}
}

func TestPackedSplitRejectsMissingSelectorCache(t *testing.T) {
	for _, tc := range []struct {
		name       string
		id         byte
		opts       data.GSM7Options
		text, want string
	}{
		{"locking", 0x25, data.GSM7Options{Packed: true, LockingShift: 1, SingleShift: 1}, strings.Repeat("Ğ", 167) + "\r", strings.Repeat("Ø", 167) + "\r\r"},
		// H7/fill0: 83 extension pairs + A + CR = 168 septets,
		// so terminal CR doubles on encoding (TS 23.038 §6.1.2.3.1).
		{"single", 0x24, data.GSM7Options{Packed: true, SingleShift: 1, LockingShift: 3}, strings.Repeat("Ğ", 83) + "A\r", strings.Repeat("G", 83) + "A\r\r"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			codec, err := data.NewGSM7(tc.opts)
			if err != nil {
				t.Fatal(err)
			}
			packet := pdu.NewSubmitSM().(*pdu.SubmitSM)
			if err := packet.Message.SetLongMessageWithEnc(tc.text, codec); err != nil {
				t.Fatal(err)
			}
			packedReviewRemoveSelector(t, &packet.Message, tc.id)
			received := packedReviewReceived(t, packet)
			expected, err := received.GetMessageWithEncoding(codec)
			if err != nil || expected != tc.want {
				t.Fatalf("fixture wire text=%q,%v; want%q", expected, err, tc.want)
			}
			parts, err := packet.Split()
			if err != nil {
				t.Fatal(err)
			}
			if len(parts) != 2 {
				t.Fatalf("parts%d; want2", len(parts))
			}
			var joined strings.Builder
			for _, part := range parts {
				got, err := packedReviewReceived(t, part).GetMessageWithEncoding(codec)
				if err != nil {
					t.Fatal(err)
				}
				joined.WriteString(got)
			}
			if joined.String() != expected {
				t.Fatalf("joined=%q; current wire=%q", joined.String(), expected)
			}
		})
	}
}

func TestPackedSplitMayEndOnZeroSeptet(t *testing.T) {
	text := strings.Repeat("@", 300)
	packet := pdu.NewSubmitSM().(*pdu.SubmitSM)
	if err := packet.Message.SetUDH(pdu.UDH{{ID: 0x70}}); err != nil {
		t.Fatal(err)
	}
	if err := packet.Message.SetLongMessageWithEnc(text, data.GSM7BITPACKED); err != nil {
		t.Fatal(err)
	}
	header, err := packet.Message.UDH().MarshalBinary()
	if err != nil || !bytes.Equal(header, []byte{2, 0x70, 0}) {
		t.Fatalf("caller UDH=%x,%v", header, err)
	}
	parts, err := packet.Split()
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 2 {
		t.Fatalf("parts=%d; want2 of150 septets", len(parts))
	}
	var joined strings.Builder
	for i, part := range parts {
		h, err := part.Message.UDH().MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		if len(h) != 8 || h[0] != 7 || h[1] != 0 || h[2] != 3 || h[4] != 2 || int(h[5]) != i+1 || !bytes.Equal(h[6:], []byte{0x70, 0}) {
			t.Fatalf("part%d header=%x", i+1, h)
		}
		raw, err := part.Message.GetMessageData()
		if err != nil {
			t.Fatal(err)
		}
		if len(raw) != 132 || !bytes.Equal(raw, make([]byte, 132)) {
			t.Fatalf("part%d payload=%x; want132 zero octets", i+1, raw)
		}
		fill := (7 - len(h)*8%7) % 7
		septets := gsm7.Unpack7BitCR(raw, fill)
		if len(septets) != 150 || !bytes.Equal(septets, make([]byte, 150)) {
			t.Fatalf("part%d septets=%x; want150zeros", i+1, septets)
		}
		got, err := packedReviewReceived(t, part).GetMessageWithEncoding(data.GSM7BITPACKED)
		if err != nil || got != strings.Repeat("@", 150) {
			t.Fatalf("part%d text=%q,%v", i+1, got, err)
		}
		joined.WriteString(got)
	}
	if joined.String() != text {
		t.Fatal("joined text changed")
	}
}
