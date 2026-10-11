package main

import (
	"bytes"
	"log"
	"strings"
	"testing"

	"github.com/gomaja/go-smpp/pdu"
)

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
