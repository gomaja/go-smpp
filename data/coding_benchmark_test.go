package data_test

import (
	"strings"
	"testing"

	"github.com/gomaja/go-smpp/data"
)

var codingBenchmarkText string

// Measure complete string-in/string-out operations, including conversions
// required by the public codec API, on single-message GSM7 and UCS2 texts.
func BenchmarkCodingRoundtrip(b *testing.B) {
	for _, tc := range []struct {
		name, text string
		enc        data.Encoding
	}{
		{"GSM7_160", strings.Repeat("Hello world 1234", 10), data.GSM7BIT},
		{"GSM7Packed_160", strings.Repeat("Hello world 1234", 10), data.GSM7BITPACKED},
		{"UCS2_70", strings.Repeat("漢字日本語中文", 10), data.UCS2},
	} {
		b.Run(tc.name, func(b *testing.B) {
			wire, err := tc.enc.Encode(tc.text)
			if err != nil {
				b.Fatal(err)
			}
			decoded, err := tc.enc.Decode(wire)
			if err != nil || decoded != tc.text {
				b.Fatalf("roundtrip=%q, %v", decoded, err)
			}
			b.ReportAllocs()
			for b.Loop() {
				wire, err = tc.enc.Encode(tc.text)
				if err != nil {
					b.Fatal(err)
				}
				codingBenchmarkText, err = tc.enc.Decode(wire)
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
