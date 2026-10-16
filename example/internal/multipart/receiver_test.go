package multipart

import (
	"errors"
	"testing"
	"time"

	"github.com/gomaja/go-smpp/data"
	"github.com/gomaja/go-smpp/pdu"
	"github.com/gomaja/go-sms/encoding/ucs2"
)

func TestOddUCS2ReturnsTextWithoutReassembly(t *testing.T) {
	for _, coding := range []struct {
		name string
		enc  data.Encoding
	}{{"UCS2", data.UCS2}, {"UCS2Strict", data.UCS2Strict}} {
		for _, sar := range []bool{false, true} {
			for _, tc := range []struct {
				name string
				wire []byte
				want string
			}{
				{"prefix", []byte{0, 0x41, 0}, "A"},
				{"high_before_final_octet", []byte{0, 0x41, 0xd8, 0x3d, 0xde}, "A\ufffd"},
			} {
				metadata := "UDH"
				if sar {
					metadata = "SAR"
				}
				t.Run(coding.name+"/"+metadata+"/"+tc.name, func(t *testing.T) {
					var receiver Receiver
					first := part(t, "", 2, 1, 7, sar)
					if err := first.Message.SetMessageDataWithEncoding(tc.wire, coding.enc); err != nil {
						t.Fatal(err)
					}
					text, ready, err := receiver.Add(first)
					var dangling ucs2.ErrDanglingSurrogate
					if text != tc.want || ready || !errors.Is(err, ucs2.ErrInvalidLength) || errors.As(err, &dangling) {
						t.Fatalf("odd segment = %q, %t, %v; want %q, false, ErrInvalidLength", text, ready, err, tc.want)
					}
					// TS 23.040 §§9.2.3.24.1, 9.2.3.24.8: no octet or
					// surrogate from the malformed segment may enter the next one.
					tail := part(t, "", 2, 2, 7, sar)
					if err := tail.Message.SetMessageDataWithEncoding([]byte{0, 0x42}, coding.enc); err != nil {
						t.Fatal(err)
					}
					if text, ready, err := receiver.Add(tail); err != nil || ready || text != "" {
						t.Fatalf("tail after malformed segment = %q, %t, %v", text, ready, err)
					}
					if err := first.Message.SetMessageDataWithEncoding([]byte{0, 0x41}, coding.enc); err != nil {
						t.Fatal(err)
					}
					if text, ready, err := receiver.Add(first); err != nil || !ready || text != "AB" {
						t.Fatalf("corrected message = %q, %t, %v; want AB", text, ready, err)
					}
				})
			}
		}
	}
}

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

func TestSARUsesSixteenBitReference(t *testing.T) {
	// sar_msg_ref_num has two octets (SMPP v3.4 §5.3.2.22) and both are kept:
	// a SAR part joins a 16-bit UDH message with the same reference, but never
	// an 8-bit UDH message or a SAR message whose reference differs only in
	// its high octet.
	var receiver Receiver
	eight := part(t, "eight", 2, 1, 7, false)
	if err := eight.Message.SetUDH(pdu.UDH{pdu.NewIEConcatMessage(2, 1, 7)}); err != nil {
		t.Fatal(err)
	}
	if _, ready, err := receiver.Add(eight); err != nil || ready {
		t.Fatalf("8-bit first part = %t, %v", ready, err)
	}
	if _, ready, err := receiver.Add(part(t, "sar", 2, 2, 7, true)); err != nil || ready {
		t.Fatalf("SAR part completed an 8-bit message: %t, %v", ready, err)
	}
	if _, ready, err := receiver.Add(part(t, "A", 2, 1, 0x1234, true)); err != nil || ready {
		t.Fatalf("first SAR part = %t, %v", ready, err)
	}
	if _, ready, err := receiver.Add(part(t, "Y", 2, 2, 0xab34, true)); err != nil || ready {
		t.Fatalf("SAR references differing in the high octet joined: %t, %v", ready, err)
	}
	text, ready, err := receiver.Add(part(t, "B", 2, 2, 0x1234, false))
	if err != nil || !ready || text != "AB" {
		t.Fatalf("SAR and 16-bit UDH completion = %q, %t, %v", text, ready, err)
	}
	text, ready, err = receiver.Add(part(t, "X", 2, 1, 0xab34, true))
	if err != nil || !ready || text != "XY" || len(receiver.pending) != 2 {
		t.Fatalf("SAR completion = %q, %t, %v, pending=%d", text, ready, err, len(receiver.pending))
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

// Invalid trailing IEs must not change the selected reference width or hide
// earlier valid metadata (TS 23.040 §§9.2.3.24, 9.2.3.24.1, 9.2.3.24.8).
func TestLastApplicableConcatReference(t *testing.T) {
	for _, tc := range []struct {
		name string
		wide bool
		tail pdu.InfoElement
	}{
		{"short_16bit_after_8bit", false, pdu.InfoElement{ID: 8, Data: []byte{7}}},
		{"invalid_16bit_after_8bit", false, pdu.NewIEConcatMessage16Bit(0, 0, 7)},
		{"short_8bit_after_16bit", true, pdu.InfoElement{ID: 0, Data: []byte{7}}},
		{"invalid_8bit_after_16bit", true, pdu.NewIEConcatMessage(0, 0, 7)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var receiver Receiver
			first := part(t, "A", 2, 1, 7, false)
			last := part(t, "B", 2, 2, 7, false)
			var firstIE, lastIE pdu.InfoElement
			if tc.wide {
				firstIE = pdu.NewIEConcatMessage16Bit(2, 1, 7)
				lastIE = pdu.NewIEConcatMessage16Bit(2, 2, 7)
			} else {
				firstIE = pdu.NewIEConcatMessage(2, 1, 7)
				lastIE = pdu.NewIEConcatMessage(2, 2, 7)
			}
			if err := first.Message.SetUDH(pdu.UDH{firstIE, tc.tail}); err != nil {
				t.Fatal(err)
			}
			if err := last.Message.SetUDH(pdu.UDH{lastIE}); err != nil {
				t.Fatal(err)
			}
			if text, ready, err := receiver.Add(first); err != nil || ready || text != "" {
				t.Fatalf("first part = %q, %t, %v; want pending", text, ready, err)
			}
			if text, ready, err := receiver.Add(last); err != nil || !ready || text != "AB" {
				t.Fatalf("last part = %q, %t, %v; want AB", text, ready, err)
			}
		})
	}
}

// TS 23.040 §§9.2.3.24, 9.2.3.24.1, 9.2.3.24.8: unrelated IEs do not
// interrupt selection, and malformed IEs on either side do not replace it.
func TestReceiverInterleavedConcat(t *testing.T) {
	for _, tc := range []struct {
		name   string
		header pdu.UDH
		wire   []byte
		tail   pdu.InfoElement
	}{
		{
			"interleaved_8bit",
			pdu.UDH{{ID: 0x70, Data: []byte{0xaa}}, pdu.NewIEConcatMessage16Bit(3, 2, 0x5678), {ID: 0x71}, pdu.NewIEConcatMessage(2, 1, 7), {ID: 0x72, Data: []byte{0}}, {ID: 8, Data: []byte{0x12}}, {ID: 0x73}},
			[]byte{24, 0x70, 1, 0xaa, 8, 4, 0x56, 0x78, 3, 2, 0x71, 0, 0, 3, 7, 2, 1, 0x72, 1, 0, 8, 1, 0x12, 0x73, 0},
			pdu.NewIEConcatMessage(2, 2, 7),
		},
		{
			"interleaved_16bit",
			pdu.UDH{{ID: 0x70, Data: []byte{0xaa}}, pdu.NewIEConcatMessage(3, 2, 9), {ID: 0x71}, pdu.NewIEConcatMessage16Bit(2, 1, 0x1234), {ID: 0x72, Data: []byte{0}}, {ID: 0, Data: []byte{7}}, {ID: 0x73}},
			[]byte{24, 0x70, 1, 0xaa, 0, 3, 9, 3, 2, 0x71, 0, 8, 4, 0x12, 0x34, 2, 1, 0x72, 1, 0, 0, 1, 7, 0x73, 0},
			pdu.NewIEConcatMessage16Bit(2, 2, 0x1234),
		},
		{
			"malformed_before_8bit",
			pdu.UDH{{ID: 0x70}, {ID: 8, Data: []byte{0x12}}, {ID: 0x71}, pdu.NewIEConcatMessage(2, 1, 7), {ID: 0x72}},
			[]byte{14, 0x70, 0, 8, 1, 0x12, 0x71, 0, 0, 3, 7, 2, 1, 0x72, 0},
			pdu.NewIEConcatMessage(2, 2, 7),
		},
		{
			"malformed_before_16bit",
			pdu.UDH{{ID: 0x70}, {ID: 0, Data: []byte{7}}, {ID: 0x71}, pdu.NewIEConcatMessage16Bit(2, 1, 0x1234), {ID: 0x72}},
			[]byte{15, 0x70, 0, 0, 1, 7, 0x71, 0, 8, 4, 0x12, 0x34, 2, 1, 0x72, 0},
			pdu.NewIEConcatMessage16Bit(2, 2, 0x1234),
		},
	} {
		for _, mode := range []string{"constructed", "received"} {
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				var receiver Receiver
				first := part(t, "A", 2, 1, 7, false)
				if mode == "received" {
					// Literal UDH and payload; no production marshaler builds the fixture.
					wire := append([]byte{0, 0, byte(len(tc.wire) + 1)}, tc.wire...)
					wire = append(wire, 'A')
					if err := first.Message.Unmarshal(pdu.NewBuffer(wire), true); err != nil {
						t.Fatal(err)
					}
				} else if err := first.Message.SetUDH(tc.header); err != nil {
					t.Fatal(err)
				}
				last := part(t, "B", 2, 2, 7, false)
				if err := last.Message.SetUDH(pdu.UDH{tc.tail}); err != nil {
					t.Fatal(err)
				}
				if text, ready, err := receiver.Add(first); err != nil || ready || text != "" {
					t.Fatalf("first part = %q, %t, %v; want pending", text, ready, err)
				}
				if text, ready, err := receiver.Add(last); err != nil || !ready || text != "AB" {
					t.Fatalf("last part = %q, %t, %v; want AB", text, ready, err)
				}
			})
		}
	}
}
