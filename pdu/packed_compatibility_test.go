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
// The recorded bytes remain the encoding oracle. Decoding follows the explicit
// CR rule checked by independent bit extraction below; recorded decoded text
// is retained only to classify migration differences. Source caching still
// preserves exact application text and is checked separately.
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
		oracleSeptets := packedMainSeptetOracle(row.Wire, row.Fill)
		oracleText, oracleErr := data.GSM7BIT.Decode(oracleSeptets)
		if oracleErr != nil {
			t.Fatalf("%s alphabet oracle: %v", name, oracleErr)
		}
		decoded, err := data.DecodeGSM7Packed(row.Wire, uint8(row.Fill), -1)
		if err != nil || decoded != oracleText {
			t.Fatalf("%s decode=%q,%v; CR oracle=%q", name, decoded, err, oracleText)
		}
		if row.Fill == 0 {
			codecWire, err := data.GSM7BITPACKED.Encode(text)
			if err != nil || !bytes.Equal(codecWire, row.Wire) {
				t.Fatalf("%s public codec encode=%x,%v; main=%x", name, codecWire, err, row.Wire)
			}
			codecText, err := data.GSM7BITPACKED.Decode(row.Wire)
			if err != nil || codecText != oracleText {
				t.Fatalf("%s public codec decode=%q,%v; CR oracle=%q", name, codecText, err, oracleText)
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
		if err != nil || decoded != oracleText {
			t.Fatalf("%s wire decode=%q,%v; CR oracle=%q", name, decoded, err, oracleText)
		}
	}
	if rows != 7*301*6 {
		t.Fatalf("fixture has %d rows; want 12642", rows)
	}
}

// Extract bits individually rather than calling a product or dependency packer.
// Under the selected CR rule, only a final complete padding septet containing
// CR is discarded. Zero septets and the doubled boundary CR remain content.
func packedMainSeptetOracle(wire []byte, fill int) []byte {
	if len(wire) == 0 {
		return nil
	}
	bits := len(wire)*8 - fill
	septets := make([]byte, bits/7)
	for i := range septets {
		for k := 0; k < 7; k++ {
			position := fill + i*7 + k
			septets[i] |= ((wire[position/8] >> uint(position%8)) & 1) << uint(k)
		}
	}
	if len(septets) > 0 && bits%7 == 0 && septets[len(septets)-1] == 0x0d {
		septets = septets[:len(septets)-1]
	}
	return septets
}

func TestPackedMainOracleLiteralPaddingCases(t *testing.T) {
	for _, tc := range []struct {
		name string
		wire []byte
		want string
	}{
		{"CR spare septet", []byte{0xc1, 0x60, 0x30, 0x18, 0x0c, 0x06, 0x1b}, "AAAAAAA"},
		{"zero spare septet retained", []byte{0xc1, 0x60, 0x30, 0x18, 0x0c, 0x06, 0x01}, "AAAAAAA@"},
		{"genuine eighth at retained", []byte{0, 0, 0, 0, 0, 0, 0}, "@@@@@@@@"},
		{"doubled wanted CR retained", []byte{0xc1, 0x60, 0x30, 0x18, 0x0c, 0x06, 0x1b, 0x0d}, "AAAAAAA\r\r"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			oracle, err := data.GSM7BIT.Decode(packedMainSeptetOracle(tc.wire, 0))
			if err != nil || oracle != tc.want {
				t.Fatalf("literal oracle=%q,%v; want%q", oracle, err, tc.want)
			}
			got, err := data.DecodeGSM7Packed(tc.wire, 0, -1)
			if err != nil || got != tc.want {
				t.Fatalf("DecodeGSM7Packed=%q,%v; want%q", got, err, tc.want)
			}
		})
	}
}

// Source septet counts distinguish a genuine terminal @/CR from an additional
// padding septet; comparing text alone would conflate these migration classes.
func TestPackedMainDecodePolicySummary(t *testing.T) {
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
	counts := map[string]int{"unchanged": 0, "genuine_final_at_kept": 0, "zero_spare_filler_retained": 0, "doubled_cr_retained": 0, "other": 0}
	examples := map[string]any{}
	rows := 0
	otherGenuineCR := 0
	for {
		var row packedMainRow
		if err := decoder.Decode(&row); err != nil {
			if err == io.EOF {
				break
			}
			t.Fatal(err)
		}
		rows++
		septets := packedMainSeptetOracle(row.Wire, row.Fill)
		text, err := data.GSM7BIT.Decode(septets)
		if err != nil {
			t.Fatal(err)
		}
		sourceText := packedMainText(row.Pattern, row.N)
		sourceCount := row.N
		if row.Pattern == "extensions" {
			sourceCount *= 2
		}
		class := "unchanged"
		if text != row.Decoded || row.Error != "" {
			class = "other"
			if len(septets) > 0 {
				last := septets[len(septets)-1]
				switch {
				case last == 0 && len(septets) == sourceCount && text == sourceText:
					class = "genuine_final_at_kept"
				case last == 0 && len(septets) == sourceCount+1:
					class = "zero_spare_filler_retained"
				case last == 0x0d && len(septets) == sourceCount+1:
					class = "doubled_cr_retained"
				case last == 0x0d && len(septets) == sourceCount && text == sourceText:
					otherGenuineCR++
				}
			}
		}
		counts[class]++
		if class != "unchanged" {
			if _, ok := examples[class]; !ok {
				examples[class] = map[string]any{"fill": row.Fill, "length": row.N, "pattern": row.Pattern, "wire_hex": fmt.Sprintf("%x", row.Wire), "source_septets": sourceCount, "oracle_septets": len(septets), "old_text": row.Decoded, "new_text": text}
			}
		}
	}
	want := map[string]int{"unchanged": 11328, "genuine_final_at_kept": 526, "zero_spare_filler_retained": 0, "doubled_cr_retained": 526, "other": 262}
	if rows != 12642 {
		t.Fatalf("rows%d want12642", rows)
	}
	for class, n := range want {
		if counts[class] != n {
			t.Errorf("class%s=%d want%d", class, counts[class], n)
		}
	}
	if otherGenuineCR != counts["other"] {
		t.Errorf("unexplained other changes:%d", counts["other"]-otherGenuineCR)
	}
	summary, err := json.Marshal(map[string]any{"rows": rows, "changed": rows - counts["unchanged"], "counts": counts, "other_genuine_final_cr_kept": otherGenuineCR, "minimal_examples": examples})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("policy_summary=%s", summary)
}
