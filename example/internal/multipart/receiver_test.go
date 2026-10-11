package multipart

import (
	"testing"
	"time"

	"github.com/gomaja/go-smpp/pdu"
)

func part(t *testing.T, text string, total, sequence byte, reference uint16, sar bool) *pdu.DeliverSM {
	t.Helper()
	pd := pdu.NewDeliverSM().(*pdu.DeliverSM)
	if err := pd.Message.SetMessageWithEncoding(text, nil); err != nil {
		t.Fatal(err)
	}
	if sar {
		pd.RegisterOptionalParam(pdu.NewFieldUint16(pdu.TagSarMsgRefNum, reference))
		pd.RegisterOptionalParam(pdu.NewFieldUint8(pdu.TagSarTotalSegments, total))
		pd.RegisterOptionalParam(pdu.NewFieldUint8(pdu.TagSarSegmentSeqnum, sequence))
	} else {
		if err := pd.Message.SetUDH(pdu.UDH{pdu.NewIEConcatMessage16Bit(total, sequence, reference)}); err != nil {
			t.Fatal(err)
		}
	}
	return pd
}

func TestInvalidMetadataReturnsStandaloneText(t *testing.T) {
	for _, sar := range []bool{false, true} {
		for _, numbers := range [][2]byte{{0, 0}, {0, 1}, {2, 0}, {2, 3}} {
			var receiver Receiver
			text, ready, err := receiver.Add(part(t, "standalone", numbers[0], numbers[1], 7, sar))
			if err != nil || !ready || text != "standalone" || len(receiver.pending) != 0 {
				t.Fatalf("SAR=%t total/sequence=%v: got %q, %t, %v", sar, numbers, text, ready, err)
			}
		}
	}
}

func TestOutOfOrderDuplicatesAndEmptyParts(t *testing.T) {
	for _, sar := range []bool{false, true} {
		var receiver Receiver
		for _, pd := range []*pdu.DeliverSM{part(t, "C", 3, 3, 7, sar), part(t, "C", 3, 3, 7, sar), part(t, "", 3, 2, 7, sar), part(t, "", 3, 2, 7, sar)} {
			if _, ready, err := receiver.Add(pd); err != nil || ready {
				t.Fatalf("premature completion: ready=%t, err=%v", ready, err)
			}
		}
		text, ready, err := receiver.Add(part(t, "A", 3, 1, 7, sar))
		if err != nil || !ready || text != "AC" || len(receiver.pending) != 0 {
			t.Fatalf("completion = %q, %t, %v", text, ready, err)
		}
	}
}

func TestReferenceReuseAndAddressIsolation(t *testing.T) {
	var receiver Receiver
	if _, _, err := receiver.Add(part(t, "old", 2, 1, 7, false)); err != nil {
		t.Fatal(err)
	}
	// Changed total restarts the buffer; sequence 3 must not index the old size 2.
	if _, ready, err := receiver.Add(part(t, "C", 3, 3, 7, false)); err != nil || ready {
		t.Fatalf("changed total = %t, %v", ready, err)
	}
	other := part(t, "other", 3, 1, 7, false)
	other.SourceAddr.SetTon(1)
	if _, _, err := receiver.Add(other); err != nil {
		t.Fatal(err)
	}
	if _, _, err := receiver.Add(part(t, "A", 3, 1, 7, false)); err != nil {
		t.Fatal(err)
	}
	text, ready, err := receiver.Add(part(t, "B", 3, 2, 7, false))
	if err != nil || !ready || text != "ABC" || len(receiver.pending) != 1 {
		t.Fatalf("isolated completion = %q, %t, %v", text, ready, err)
	}
}

func TestCapacityExpiryAndMaximumSequence(t *testing.T) {
	var receiver Receiver
	for ref := range uint16(maxMessages) {
		if _, ready, err := receiver.Add(part(t, "tail", 255, 255, ref, false)); err != nil || ready {
			t.Fatalf("maximum sequence = %t, %v", ready, err)
		}
	}
	text, ready, err := receiver.Add(part(t, "overflow", 2, 1, 500, false))
	if err != nil || !ready || text != "overflow" || len(receiver.pending) != maxMessages {
		t.Fatalf("capacity = %q, %t, %v", text, ready, err)
	}
	for _, m := range receiver.pending {
		m.created = time.Now().Add(-retention)
	}
	if _, ready, err := receiver.Add(part(t, "fresh", 2, 1, 500, false)); err != nil || ready || len(receiver.pending) != 1 {
		t.Fatalf("expiry = %t, %v, pending=%d", ready, err, len(receiver.pending))
	}
}

func TestIncompleteSARAndLastUDH(t *testing.T) {
	var receiver Receiver
	pd := part(t, "standalone", 2, 1, 7, true)
	pd.RegisterOptionalParam(pdu.Field{Tag: pdu.TagSarMsgRefNum, Data: []byte{1}})
	text, ready, err := receiver.Add(pd)
	if err != nil || !ready || text != "standalone" {
		t.Fatalf("truncated SAR = %q, %t, %v", text, ready, err)
	}
	pd = part(t, "last", 2, 1, 7, false)
	// TS 23.040 §9.2.3.24: the first IE would wait for another part;
	// only the last IE completes this message immediately.
	if err := pd.Message.SetUDH(pdu.UDH{pdu.NewIEConcatMessage(2, 1, 1), pdu.NewIEConcatMessage(1, 1, 2)}); err != nil {
		t.Fatal(err)
	}
	text, ready, err = receiver.Add(pd)
	if err != nil || !ready || text != "last" {
		t.Fatalf("last IE = %q, %t, %v", text, ready, err)
	}
}

func TestReferenceWidthsAreIndependent(t *testing.T) {
	var receiver Receiver
	first := part(t, "eight-", 2, 1, 7, false)
	if err := first.Message.SetUDH(pdu.UDH{pdu.NewIEConcatMessage(2, 1, 7)}); err != nil {
		t.Fatal(err)
	}
	if _, ready, err := receiver.Add(first); err != nil || ready {
		t.Fatalf("8-bit first part = %t, %v", ready, err)
	}
	if _, ready, err := receiver.Add(part(t, "sixteen", 2, 2, 7, false)); err != nil || ready {
		t.Fatalf("mixed reference widths completed a message: %t, %v", ready, err)
	}
	last := part(t, "eight", 2, 2, 7, false)
	if err := last.Message.SetUDH(pdu.UDH{pdu.NewIEConcatMessage(2, 2, 7)}); err != nil {
		t.Fatal(err)
	}
	text, ready, err := receiver.Add(last)
	if err != nil || !ready || text != "eight-eight" {
		t.Fatalf("8-bit completion = %q, %t, %v", text, ready, err)
	}
	text, ready, err = receiver.Add(part(t, "sixteen-", 2, 1, 7, false))
	if err != nil || !ready || text != "sixteen-sixteen" || len(receiver.pending) != 0 {
		t.Fatalf("16-bit completion = %q, %t, %v", text, ready, err)
	}
}
