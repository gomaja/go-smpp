package main

import (
	"testing"

	"github.com/gomaja/go-smpp/pdu"
)

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
