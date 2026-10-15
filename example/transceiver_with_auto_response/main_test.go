package main

import (
	"bytes"
	"log"
	"strings"
	"testing"

	"github.com/gomaja/go-smpp/data"
	"github.com/gomaja/go-smpp/pdu"
	"github.com/gomaja/go-sms/encoding/ucs2"
)

func TestHandlerLogsPartialUCS2(t *testing.T) {
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	t.Cleanup(func() { log.SetOutput(previous) })
	for _, multipart := range []bool{false, true} {
		name := "standalone"
		if multipart {
			name = "multipart"
		}
		t.Run(name, func(t *testing.T) {
			handler := handlePDU()
			delivery := pdu.NewDeliverSM().(*pdu.DeliverSM)
			payload := []byte{0, 'K', 0, 'E', 0, 'E', 0, 'P', 0, '-', 0, 'M', 0, 'E', 0xff}
			if err := delivery.Message.SetMessageDataWithEncoding(payload, data.UCS2); err != nil {
				t.Fatal(err)
			}
			if multipart {
				if err := delivery.Message.SetUDH(pdu.UDH{pdu.NewIEConcatMessage(2, 1, 7)}); err != nil {
					t.Fatal(err)
				}
			}
			output.Reset()
			handler(delivery, true)
			found := false
			for _, line := range strings.Split(output.String(), "\n") {
				if strings.Contains(line, "KEEP-ME") && strings.Contains(line, ucs2.ErrInvalidLength.Error()) {
					found = true
				}
			}
			if !found {
				t.Fatalf("partial text and decoding error missing from the same log entry: %q", output.String())
			}
			if !multipart {
				return
			}
			tail := pdu.NewDeliverSM().(*pdu.DeliverSM)
			if err := tail.Message.SetMessageWithEncoding("TAIL", data.UCS2); err != nil {
				t.Fatal(err)
			}
			if err := tail.Message.SetUDH(pdu.UDH{pdu.NewIEConcatMessage(2, 2, 7)}); err != nil {
				t.Fatal(err)
			}
			output.Reset()
			handler(tail, true)
			if strings.Contains(output.String(), "TAIL") {
				t.Fatalf("malformed segment entered reassembly: %q", output.String())
			}
			if err := delivery.Message.SetMessageWithEncoding("VALID", data.UCS2); err != nil {
				t.Fatal(err)
			}
			output.Reset()
			handler(delivery, true)
			if !strings.Contains(output.String(), "VALIDTAIL") {
				t.Fatalf("corrected message missing: %q", output.String())
			}
		})
	}
}

func TestInvalidConcatenationDoesNotPanic(t *testing.T) {
	for _, info := range [][2]byte{{0, 1}, {2, 0}, {2, 3}} {
		t.Run(string([]byte{'0' + info[0], '0' + info[1]}), func(t *testing.T) {
			pd := pdu.NewDeliverSM().(*pdu.DeliverSM)
			if err := pd.Message.SetMessageWithEncoding("part", nil); err != nil {
				t.Fatal(err)
			}
			if err := pd.Message.SetUDH(pdu.UDH{pdu.NewIEConcatMessage(info[0], info[1], 1)}); err != nil {
				t.Fatal(err)
			}
			handlePDU()(pd, true)
		})
	}
}
