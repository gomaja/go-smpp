package pdu_test

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/gomaja/go-smpp/data"
	"github.com/gomaja/go-smpp/pdu"
)

type packedMainRow struct {
	Fill    int    `json:"f"`
	N       int    `json:"n"`
	Pattern string `json:"p"`
	Wire    []byte `json:"w"`
	Decoded string `json:"d"`
	Error   string `json:"e,omitempty"`
}

func packedMainText(pattern string, n int) string {
	switch pattern {
	case "A", "@", "CR":
		if pattern == "CR" {
			pattern = "\r"
		}
		return strings.Repeat(pattern, n)
	case "last@", "lastCR":
		if n == 0 {
			return ""
		}
		tail := "@"
		if pattern == "lastCR" {
			tail = "\r"
		}
		return strings.Repeat("A", n-1) + tail
	default:
		alphabet := []rune("\f^{}\\[~]|€")
		text := make([]rune, n)
		for i := range text {
			text[i] = alphabet[i%len(alphabet)]
		}
		return string(text)
	}
}

func packedMainHeader(fill int) pdu.UDH {
	if fill == 0 {
		return nil
	}
	for n := 0; n < 7; n++ {
		header := pdu.UDH{{ID: 0x70, Data: make([]byte, n)}}
		if (7-(header.UDHL()*8)%7)%7 == fill {
			return header
		}
	}
	panic("unreachable fill")
}

// The fixture was captured from main before replacing the codecs. It records
// all 0..300 character lengths, seven UDH fill offsets and six text patterns.
// Inferred decoding deliberately uses recorded text, including lost terminal
// @/CR ambiguity; it must not be replaced with an assumed exact roundtrip.
// TS 23.040 section 9.2.3.24 governs UDH alignment; TS 23.038 sections
// 6.1.2.1.1 and 6.1.2.3.1 describe packing and the CR padding convention.
func TestPackedMainCompatibility(t *testing.T) {
	fixture, err := os.Open("testdata/packed-main.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := fixture.Close(); err != nil {
			t.Error(err)
		}
	}()
	zipped, err := gzip.NewReader(fixture)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := zipped.Close(); err != nil {
			t.Error(err)
		}
	}()
	decoder := json.NewDecoder(zipped)
	patterns := []string{"A", "@", "CR", "last@", "lastCR", "extensions"}
	rows := 0
	for {
		var row packedMainRow
		if err := decoder.Decode(&row); err != nil {
			if err == io.EOF {
				break
			}
			t.Fatal(err)
		}
		fill := rows / (301 * len(patterns))
		n := rows / len(patterns) % 301
		pattern := patterns[rows%len(patterns)]
		if row.Fill != fill || row.N != n || row.Pattern != pattern {
			t.Fatalf("fixture row %d out of sequence: %+v", rows, row)
		}
		rows++
		name := fmt.Sprintf("fill=%d/n=%d/%s", row.Fill, row.N, row.Pattern)
		header := packedMainHeader(row.Fill)
		var message pdu.ShortMessage
		if err := message.SetUDH(header); err != nil {
			t.Fatalf("%s SetUDH: %v", name, err)
		}
		text := packedMainText(row.Pattern, row.N)
		if err := message.SetLongMessageWithEnc(text, data.GSM7BITPACKED); err != nil {
			t.Fatalf("%s encode: %v", name, err)
		}
		got, err := message.GetMessageData()
		if err != nil || !bytes.Equal(got, row.Wire) {
			t.Fatalf("%s packed=%x,%v; main=%x", name, got, err, row.Wire)
		}
		decoded, err := data.DecodeGSM7Packed(row.Wire, uint8(row.Fill), -1)
		errorText := ""
		if err != nil {
			errorText = err.Error()
		}
		if decoded != row.Decoded || errorText != row.Error {
			t.Fatalf("%s decode=%q,%v; main=%q,%q", name, decoded, err, row.Decoded, row.Error)
		}
		if row.Fill == 0 {
			codecWire, err := data.GSM7BITPACKED.Encode(text)
			if err != nil || !bytes.Equal(codecWire, row.Wire) {
				t.Fatalf("%s public codec encode=%x,%v; main=%x", name, codecWire, err, row.Wire)
			}
			codecText, err := data.GSM7BITPACKED.Decode(row.Wire)
			if err != nil || codecText != row.Decoded {
				t.Fatalf("%s public codec decode=%q,%v; main=%q", name, codecText, err, row.Decoded)
			}
		}
		cached, err := message.GetMessageWithEncoding(data.GSM7BITPACKED)
		if err != nil || cached != text {
			t.Fatalf("%s cached source=%q,%v; want=%q", name, cached, err, text)
		}
		if len(row.Wire)+header.UDHL() > 255 {
			continue
		}
		wire := pdu.NewBuffer(nil)
		if err := message.Marshal(wire); err != nil {
			t.Fatalf("%s marshal: %v", name, err)
		}
		headerWire, err := header.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		want := append([]byte{0, 0, byte(len(row.Wire) + len(headerWire))}, headerWire...)
		want = append(want, row.Wire...)
		if !bytes.Equal(wire.Bytes(), want) {
			t.Fatalf("%s short_message=%x; main payload/frame=%x", name, wire.Bytes(), want)
		}
		var received pdu.ShortMessage
		if err := received.Unmarshal(pdu.NewBuffer(wire.Bytes()), len(headerWire) > 0); err != nil {
			t.Fatalf("%s unmarshal: %v", name, err)
		}
		decoded, err = received.GetMessageWithEncoding(data.GSM7BITPACKED)
		if err != nil || decoded != row.Decoded {
			t.Fatalf("%s wire decode=%q,%v; main=%q", name, decoded, err, row.Decoded)
		}
	}
	if rows != 7*301*6 {
		t.Fatalf("fixture has %d rows; want 12642", rows)
	}
}
