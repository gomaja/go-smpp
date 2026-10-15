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
			invoke := func(delivery *pdu.DeliverSM) {
				t.Helper()
				response, closeBind := handler(delivery)
				if response == nil || response.GetSequenceNumber() != delivery.GetSequenceNumber() || closeBind {
					t.Fatalf("delivery response = %v, closeBind=%t", response, closeBind)
				}
			}
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
			invoke(delivery)
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
			invoke(tail)
			if strings.Contains(output.String(), "TAIL") {
				t.Fatalf("malformed segment entered reassembly: %q", output.String())
			}
			if err := delivery.Message.SetMessageWithEncoding("VALID", data.UCS2); err != nil {
				t.Fatal(err)
			}
			output.Reset()
			invoke(delivery)
			if !strings.Contains(output.String(), "VALIDTAIL") {
				t.Fatalf("corrected message missing: %q", output.String())
			}
		})
	}
}

func TestManualResponseReassemblesDelivery(t *testing.T) {
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(previous)
	handler := handlePDU()
	for index, text := range []string{"Hello ", "world"} {
		delivery := pdu.NewDeliverSM().(*pdu.DeliverSM)
		if err := delivery.Message.SetMessageWithEncoding(text, nil); err != nil {
			t.Fatal(err)
		}
		if err := delivery.Message.SetUDH(pdu.UDH{pdu.NewIEConcatMessage(2, byte(index+1), 7)}); err != nil {
			t.Fatal(err)
		}
		response, closeBind := handler(delivery)
		if response == nil || response.GetSequenceNumber() != delivery.GetSequenceNumber() || closeBind {
			t.Fatalf("delivery response = %v, closeBind=%t", response, closeBind)
		}
	}
	if !strings.Contains(output.String(), "Hello world") {
		t.Fatalf("reassembled text missing from %q", output.String())
	}
	unbind := pdu.NewUnbind()
	response, closeBind := handler(unbind)
	if response == nil || response.GetSequenceNumber() != unbind.GetSequenceNumber() || !closeBind {
		t.Fatalf("unbind response = %v, closeBind=%t", response, closeBind)
	}
}
