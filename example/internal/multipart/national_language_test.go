package multipart

import (
	"bytes"
	"testing"

	"github.com/gomaja/go-smpp/data"
	"github.com/gomaja/go-smpp/pdu"
)

func TestNationalLanguageTablesBelongToEachReceivedPart(t *testing.T) {
	// TS 23.038 Annex A.3.1/A.3.3/default: septet 04 means €, ê, è.
	// Reassembly must decode every part with its own UDH before joining it.
	var receiver Receiver
	for _, index := range []int{2, 0, 1} {
		pd := pdu.NewDeliverSM().(*pdu.DeliverSM)
		pd.EsmClass = data.SM_UDH_GSM
		if err := pd.Message.SetMessageDataWithEncoding([]byte{4}, data.GSM7BIT); err != nil {
			t.Fatal(err)
		}
		header := pdu.UDH{pdu.NewIEConcatMessage16Bit(3, byte(index+1), 0x1234)}
		if index != 2 {
			nli := byte(1)
			if index == 1 {
				nli = 3
			}
			header = append(header, pdu.InfoElement{ID: 0x25, Data: []byte{nli}})
		}
		if err := pd.Message.SetUDH(header); err != nil {
			t.Fatal(err)
		}
		wire := pdu.NewBuffer(nil)
		pd.Marshal(wire)
		if err := wire.Err(); err != nil {
			t.Fatal(err)
		}
		parsed, err := pdu.Parse(bytes.NewReader(wire.Bytes()))
		if err != nil {
			t.Fatal(err)
		}
		text, ready, err := receiver.Add(parsed.(*pdu.DeliverSM))
		if err != nil {
			t.Fatal(err)
		}
		if index == 1 {
			if !ready || text != "€êè" {
				t.Fatalf("completion=%q,%v", text, ready)
			}
		} else if ready {
			t.Fatal("completed before every segment arrived")
		}
	}
}
