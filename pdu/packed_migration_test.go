package pdu

import (
	"strings"
	"testing"

	"github.com/gomaja/go-smpp/data"
	"github.com/gomaja/go-sms/encoding/gsm7"
)

func TestReceivedPackedCRPolicy(t *testing.T) {
	// TS 23.038 §6.1.2.3.1 removes only a whole final CR padding septet.
	for _, header := range []UDH{nil, {{ID: 0x25, Data: []byte{1}}}} {
		fill := packedFillBits(header.UDHL())
		// Make the last character end on an octet boundary for every header.
		n := 8
		for (fill+7*n)%8 != 0 {
			n++
		}
		for _, tc := range []struct {
			name    string
			septets []byte
			cr      bool
			want    string
		}{
			{"final-at", append([]byte(strings.Repeat("A", n-1)), 0), false, strings.Repeat("A", n-1) + "@"},
			{"zero-spare", []byte(strings.Repeat("A", n-1)), false, strings.Repeat("A", n-1) + "@"},
			{"doubled-cr", append([]byte(strings.Repeat("A", n-1)), 13), true, strings.Repeat("A", n-1) + "\r\r"},
			{"cr-spare", []byte(strings.Repeat("A", n-1)), true, strings.Repeat("A", n-1)},
		} {
			t.Run(tc.name+strings.Repeat("H", len(header)), func(t *testing.T) {
				var payload []byte
				var err error
				if tc.cr {
					payload, err = gsm7.Pack7BitCR(tc.septets, fill)
				} else {
					payload, err = gsm7.Pack7Bit(tc.septets, fill)
				}
				if err != nil {
					t.Fatal(err)
				}
				h, err := header.MarshalBinary()
				if err != nil {
					t.Fatal(err)
				}
				_, msg := nliParsedMessage(t, nliFrame(5, 0, h, payload))
				got, err := msg.GetMessageWithEncoding(data.GSM7BITPACKED)
				if err != nil || got != tc.want {
					t.Fatalf("decode %x, fill%d=%q,%v; want%q", payload, fill, got, err, tc.want)
				}
			})
		}
	}
}
