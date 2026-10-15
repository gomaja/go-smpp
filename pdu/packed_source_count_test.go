package pdu

import (
	"bytes"
	"strings"
	"testing"

	"github.com/gomaja/go-smpp/data"
	"github.com/gomaja/go-sms/encoding/gsm7"
)

func TestPackedNationalLanguageSplitRetainsSourceCount(t *testing.T) {
	codec, err := data.NewGSM7(data.GSM7Options{Packed: true, LockingShift: 1})
	if err != nil {
		t.Fatal(err)
	}
	// H4/fill3 ends 163 septets on an octet boundary. Encoding doubles the
	// terminal CR; retained source text supplies its exact count when splitting.
	text := strings.Repeat("Ğ", 162) + "\r"
	for _, mutate := range []string{"none", "payload", "header"} {
		p := NewSubmitSM().(*SubmitSM)
		if err := p.Message.SetLongMessageWithEnc(text, codec); err != nil {
			t.Fatal(err)
		}
		expected := text
		if mutate == "payload" {
			raw, err := p.Message.GetMessageData()
			if err != nil {
				t.Fatal(err)
			}
			// Change only the first septet from Turkish Ğ (0x0b) to A (0x41).
			raw[0] = (raw[0] & 7) | 8
			raw[1] = (raw[1] & 0xfc) | 2
			expected = "A" + strings.Repeat("Ğ", 161) + "\r\r"
		}
		if mutate == "header" {
			// UDH storage is mutable: current received tables outrank cached text.
			p.Message.UDH()[0].Data[0] = 3
			decoder := gsm7.NewDecoder(gsm7.WithCharset(3))
			decoded, err := decoder.Decode(append(bytes.Repeat([]byte{0x0b}, 162), 13, 13))
			if err != nil {
				t.Fatal(err)
			}
			expected = string(decoded)
		}
		before, err := p.Message.GetMessageData()
		if err != nil {
			t.Fatal(err)
		}
		before = bytes.Clone(before)
		parts, err := p.Split()
		if err != nil {
			t.Fatal(err)
		}
		if len(parts) != 2 {
			t.Fatalf("mode%s: %d parts; want2", mutate, len(parts))
		}
		var joined strings.Builder
		for _, part := range parts {
			_, received := nliParsedMessage(t, nliMarshal(t, part))
			got, err := received.GetMessageWithEncoding(codec)
			if err != nil {
				t.Fatal(err)
			}
			joined.WriteString(got)
		}
		if got := joined.String(); got != expected {
			t.Fatalf("mode%s joined%q; want%q", mutate, got, expected)
		}
		after, err := p.Message.GetMessageData()
		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("split changed source bytes")
		}
	}
}
