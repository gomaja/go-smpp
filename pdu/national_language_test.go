package pdu

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strings"
	"testing"

	"github.com/gomaja/go-smpp/data"
	"github.com/gomaja/go-sms/encoding/gsm7"
)

// nliFrame assembles complete SMPP v3.4 sections 4.4.1/4.6.1 frames without
// invoking the product's marshaler. Addresses and sequence make field shifts visible.
func nliFrame(id uint32, coding byte, header, payload []byte) []byte {
	esm := byte(0x03)
	if len(header) > 0 {
		esm |= 0x40
	}
	body := []byte("\x00\x01\x01src\x00\x01\x01dst\x00")
	body = append(body, esm, 0, 0, 0, 0, 0, 0, coding, 0, byte(len(header)+len(payload)))
	body = append(body, header...)
	body = append(body, payload...)
	frame := make([]byte, 16, 16+len(body))
	binary.BigEndian.PutUint32(frame, uint32(16+len(body)))
	binary.BigEndian.PutUint32(frame[4:], id)
	binary.BigEndian.PutUint32(frame[12:], 0x123456)
	return append(frame, body...)
}

func nliMarshal(t *testing.T, p PDU) []byte {
	t.Helper()
	b := NewBuffer(nil)
	p.Marshal(b)
	if err := b.Err(); err != nil {
		t.Fatal(err)
	}
	return append([]byte(nil), b.Bytes()...)
}

func nliParsedMessage(t *testing.T, frame []byte) (PDU, *ShortMessage) {
	t.Helper()
	p, err := Parse(bytes.NewReader(frame))
	if err != nil {
		t.Fatalf("Parse(%x): %v", frame, err)
	}
	switch p := p.(type) {
	case *SubmitSM:
		return p, &p.Message
	case *DeliverSM:
		return p, &p.Message
	default:
		t.Fatalf("unexpected PDU %T", p)
		return nil, nil
	}
}

func nliHeader(ies ...InfoElement) []byte {
	if len(ies) == 0 {
		return nil
	}
	header := []byte{0}
	for _, ie := range ies {
		header = append(header, ie.ID, byte(len(ie.Data)))
		header = append(header, ie.Data...)
	}
	header[0] = byte(len(header) - 1)
	return header
}

// TS 23.038 Annex A locking/single tables and TS 23.040
// sections 9.2.3.24.15/16: the selected table belongs to each segment.
func TestNationalLanguageReceiveWire(t *testing.T) {
	for _, tc := range []struct {
		name    string
		header  []byte
		septets []byte
		want    string
	}{
		{"Turkish locking", []byte{3, 0x25, 1, 1}, []byte{0x0b}, "Ğ"},
		{"Turkish single", []byte{3, 0x24, 1, 1}, []byte{0x1b, 0x47}, "Ğ"},
		{"Portuguese locking", []byte{3, 0x25, 1, 3}, []byte{4}, "ê"},
		{"Spanish single", []byte{3, 0x24, 1, 2}, []byte{0x1b, 0x41}, "Á"},
		{"Hindi locking", []byte{3, 0x25, 1, 6}, []byte{3}, "अ"},
		{"last repeated valid", []byte{6, 0x25, 1, 1, 0x25, 1, 3}, []byte{4}, "ê"},
		{"last repeated single", []byte{6, 0x24, 1, 1, 0x24, 1, 2}, []byte{0x1b, 0x41}, "Á"},
		{"unknown after valid is ignored", []byte{6, 0x25, 1, 1, 0x25, 1, 255}, []byte{4}, "€"},
		{"reserved after valid is ignored", []byte{6, 0x25, 1, 1, 0x25, 1, 0}, []byte{4}, "€"},
		{"Spanish locking resets default", []byte{6, 0x25, 1, 1, 0x25, 1, 2}, []byte{4}, "è"},
		{"overlong locking uses first octet", []byte{4, 0x25, 2, 1, 255}, []byte{4}, "€"},
		{"overlong single uses first octet", []byte{4, 0x24, 2, 2, 255}, []byte{0x1b, 0x41}, "Á"},
		{"absent defaults", nil, []byte{4}, "è"},
		{"reserved zero", []byte{3, 0x25, 1, 0}, []byte{4}, "è"},
		{"unknown", []byte{3, 0x25, 1, 255}, []byte{4}, "è"},
		{"Spanish locking fallback", []byte{3, 0x25, 1, 2}, []byte{4}, "è"},
	} {
		for _, id := range []uint32{4, 5} {
			t.Run(fmt.Sprintf("%s/pdu%d", tc.name, id), func(t *testing.T) {
				wire := nliFrame(id, 0, tc.header, tc.septets)
				p, m := nliParsedMessage(t, wire)
				got, err := m.GetMessage()
				if err != nil || got != tc.want {
					t.Fatalf("GetMessage=%q,%v; want %q", got, err, tc.want)
				}
				if got := nliMarshal(t, p); !bytes.Equal(got, wire) {
					t.Errorf("forwarding changed frame: got %x want %x", got, wire)
				}
			})
		}
	}
}

func TestNationalLanguageReceiveResetsPerSegment(t *testing.T) {
	var joined strings.Builder
	for i, tc := range []struct {
		nli    byte
		septet byte
		want   string
	}{
		{1, 4, "€"}, {3, 4, "ê"}, {0, 4, "è"},
	} {
		ies := []InfoElement{NewIEConcatMessage(3, byte(i+1), 42)}
		if tc.nli != 0 {
			ies = append(ies, InfoElement{ID: 0x25, Data: []byte{tc.nli}})
		}
		_, m := nliParsedMessage(t, nliFrame(5, 0, nliHeader(ies...), []byte{tc.septet}))
		text, err := m.GetMessage()
		if err != nil || text != tc.want {
			t.Fatalf("part %d=%q,%v; want %q", i, text, err, tc.want)
		}
		joined.WriteString(text)
	}
	if joined.String() != "€êè" {
		t.Fatal(joined.String())
	}
}

func TestNationalLanguageIgnoredForUCS2AndMalformedUDH(t *testing.T) {
	for _, id := range []uint32{4, 5} {
		wire := nliFrame(id, 8, []byte{3, 0x25, 1, 6}, []byte{0, 0x41})
		p, m := nliParsedMessage(t, wire)
		if text, err := m.GetMessage(); err != nil || text != "A" {
			t.Fatalf("UCS2=%q,%v", text, err)
		}
		if got := nliMarshal(t, p); !bytes.Equal(got, wire) {
			t.Fatal("UCS2 header was changed")
		}
		// Bounded UDH ends halfway through its final IE: ignore the entire
		// header, including the earlier valid Hindi IE (TS 23.040 section 9.2.3.24).
		header := []byte{6, 0x25, 1, 6, 0x24, 2, 1}
		wire = nliFrame(id, 0, header, []byte{4})
		p, m = nliParsedMessage(t, wire)
		if text, err := m.GetMessage(); err != nil || text != "è" {
			t.Fatalf("ignored UDH=%q,%v", text, err)
		}
		if len(m.UDH()) != 0 || !bytes.Equal(m.IgnoredUDH(), header) {
			t.Fatalf("ignored header=%x parsed=%v", m.IgnoredUDH(), m.UDH())
		}
		if got := nliMarshal(t, p); !bytes.Equal(got, wire) {
			t.Fatal("ignored header forwarding changed")
		}
	}
}

type nliCase struct {
	name            string
	locking, single int
	text            string
	septets         []byte
}

// These five literal mappings are TS 23.038 Annex A.3.1/A.2.1/A.3.3/A.2.2/A.3.6.
// Larger messages use the dependency to avoid maintaining a second charset table.
var nliCases = []nliCase{
	{"Turkish locking", 1, 0, "Ğ", []byte{0x0b}},
	{"Turkish single", 0, 1, "Ğ", []byte{0x1b, 0x47}},
	{"Turkish both", 1, 1, "Ğ^", []byte{0x0b, 0x1b, 0x14}},
	{"Portuguese", 3, 0, "ê", []byte{4}},
	{"Spanish single", 0, 2, "Á", []byte{0x1b, 0x41}},
	{"Hindi", 6, 0, "अ", []byte{3}},
}

func nliCodec(t *testing.T, tc nliCase, packed bool) data.Encoding {
	t.Helper()
	enc, err := data.NewGSM7(data.GSM7Options{Packed: packed, LockingShift: tc.locking, SingleShift: tc.single})
	if err != nil {
		t.Fatal(err)
	}
	return enc
}

func nliCheckHeader(t *testing.T, header UDH, tc nliCase) {
	t.Helper()
	seen := map[byte]int{}
	for _, ie := range header {
		want := 0
		switch ie.ID {
		case 0x24:
			want = tc.single
		case 0x25:
			want = tc.locking
		default:
			continue
		}
		seen[ie.ID]++
		if want == 0 || len(ie.Data) != 1 || int(ie.Data[0]) != want {
			t.Fatalf("unexpected NLI IE %+v for %+v", ie, tc)
		}
	}
	for id, want := range map[byte]int{0x24: tc.single, 0x25: tc.locking} {
		count := 0
		if want != 0 {
			count = 1
		}
		if seen[id] != count {
			t.Fatalf("NLI IE %02x count=%d, want%d", id, seen[id], count)
		}
	}
}

func nliExpectedSeptets(t *testing.T, text string, tc nliCase) []byte {
	t.Helper()
	encoded, err := gsm7.Encode([]byte(text), gsm7.WithCharset(tc.locking), gsm7.WithExtCharset(tc.single))
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func nliExpectedPacked(t *testing.T, septets []byte, fill int) []byte {
	t.Helper()
	input := append([]byte(nil), septets...)
	bits := fill + len(input)*7
	// Existing SMPP CR convention: TS 23.038 section 6.1.2.3.1. The
	// dependency supplies the independent packing primitive and UDH alignment.
	if len(input) > 0 && (bits%8 == 1 || bits%8 == 0 && input[len(input)-1] == 0x0d) {
		input = append(input, 0x0d)
	}
	packed, err := gsm7.Pack7Bit(input, fill)
	if err != nil {
		t.Fatal(err)
	}
	return packed
}

func nliSendingPDU(t *testing.T, id uint32, message ShortMessage) PDU {
	t.Helper()
	src, err := NewAddressWithTonNpiAddr(1, 1, "src")
	if err != nil {
		t.Fatal(err)
	}
	dst, err := NewAddressWithTonNpiAddr(1, 1, "dst")
	if err != nil {
		t.Fatal(err)
	}
	esmClass := byte(3)
	// Existing concat-only callers set UDHI themselves. Leave it unset for
	// NLI headers to prove the new automatic flag behavior in both PDU types.
	hasNLI := false
	for _, ie := range message.UDH() {
		if ie.ID == 0x24 || ie.ID == 0x25 {
			hasNLI = true
		}
	}
	if len(message.UDH()) > 0 && !hasNLI {
		esmClass |= 0x40
	}
	var p PDU
	if id == 4 {
		submit := NewSubmitSM().(*SubmitSM)
		submit.SourceAddr, submit.DestAddr, submit.EsmClass, submit.Message = src, dst, esmClass, message
		p = submit
	} else {
		delivery := NewDeliverSM().(*DeliverSM)
		delivery.SourceAddr, delivery.DestAddr, delivery.EsmClass, delivery.Message = src, dst, esmClass, message
		p = delivery
	}
	p.SetSequenceNumber(0x123456)
	return p
}

func nliCheckCompleteFrame(t *testing.T, id uint32, message ShortMessage, tc nliCase, packed bool, text string) {
	t.Helper()
	nliCheckHeader(t, message.UDH(), tc)
	header := nliHeader(message.UDH()...)
	septets := nliExpectedSeptets(t, text, tc)
	payload := septets
	if packed {
		payload = nliExpectedPacked(t, septets, (7-len(header)*8%7)%7)
	}
	wire := nliMarshal(t, nliSendingPDU(t, id, message))
	if expected := nliFrame(id, 0, header, payload); !bytes.Equal(wire, expected) {
		t.Fatalf("complete frame\ngot %x\nwant%x", wire, expected)
	}
	_, parsed := nliParsedMessage(t, wire)
	var got string
	var err error
	if packed {
		got, err = parsed.GetMessageWithEncoding(data.GSM7BITPACKED)
	} else {
		got, err = parsed.GetMessage()
	}
	if err != nil || got != text {
		t.Fatalf("wire text=%q,%v; want%q", got, err, text)
	}
}

func TestNationalLanguageSendCompleteFrames(t *testing.T) {
	for _, tc := range nliCases {
		for _, packed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/packed%t", tc.name, packed), func(t *testing.T) {
				if got := nliExpectedSeptets(t, tc.text, tc); !bytes.Equal(got, tc.septets) {
					t.Fatalf("literal oracle=%x want%x", got, tc.septets)
				}
				var m ShortMessage
				caller := InfoElement{ID: 0x70, Data: []byte{0xaa, 0xbb}}
				if err := m.SetUDH(UDH{caller}); err != nil {
					t.Fatal(err)
				}
				if err := m.SetMessageWithEncoding(tc.text, nliCodec(t, tc, packed)); err != nil {
					t.Fatal(err)
				}
				if len(m.UDH()) < 1 || m.UDH()[0].ID != caller.ID || !bytes.Equal(m.UDH()[0].Data, caller.Data) {
					t.Fatal("caller IE was not retained in order")
				}
				for _, id := range []uint32{4, 5} {
					nliCheckCompleteFrame(t, id, m, tc, packed, tc.text)
				}
			})
		}
	}
}

func TestNationalLanguageSplitBudgetsAndFrames(t *testing.T) {
	alignments := map[int]bool{}
	for _, tc := range nliCases {
		for _, packed := range []bool{false, true} {
			for _, wide := range []bool{false, true} {
				for extra := 0; extra < 7; extra++ {
					t.Run(fmt.Sprintf("%s/packed%t/wide%t/extra%d", tc.name, packed, wide, extra), func(t *testing.T) {
						caller := InfoElement{ID: 0x70, Data: bytes.Repeat([]byte{0xab}, extra)}
						header := UDH{caller}
						if wide {
							header = append(UDH{NewIEConcatMessage16Bit(1, 1, 0)}, header...)
						}
						var m ShortMessage
						if err := m.SetUDH(header); err != nil {
							t.Fatal(err)
						}
						text := strings.Repeat(tc.text, 181)
						if err := m.SetLongMessageWithEnc(text, nliCodec(t, tc, packed)); err != nil {
							t.Fatal(err)
						}
						submit := nliSendingPDU(t, 4, m).(*SubmitSM)
						parts, err := submit.Split()
						if err != nil {
							t.Fatal(err)
						}
						if len(parts) < 2 {
							t.Fatalf("wanted multipart, got%d", len(parts))
						}
						var joined strings.Builder
						var reference []byte
						allSeptets := nliExpectedSeptets(t, text, tc)
						consumed := 0
						for index, part := range parts {
							h := part.Message.UDH()
							nliCheckHeader(t, h, tc)
							hb := nliHeader(h...)
							fill := (7 - len(hb)*8%7) % 7
							alignments[fill] = true
							concatID := byte(0)
							if wide {
								concatID = 8
							}
							concatCount, callerCount := 0, 0
							for _, ie := range h {
								if ie.ID == 0x70 {
									callerCount++
									if !bytes.Equal(ie.Data, caller.Data) {
										t.Fatal("caller IE changed")
									}
								}
								if ie.ID == 0 || ie.ID == 8 {
									concatCount++
									refLen := 1
									if wide {
										refLen = 2
									}
									if ie.ID != concatID || len(ie.Data) != refLen+2 {
										t.Fatalf("wrong concat width %+v", ie)
									}
									if int(ie.Data[refLen]) != len(parts) || int(ie.Data[refLen+1]) != index+1 {
										t.Fatalf("wrong concat numbering %+v", ie)
									}
									if index == 0 {
										reference = append([]byte(nil), ie.Data[:refLen]...)
									} else if !bytes.Equal(reference, ie.Data[:refLen]) {
										t.Fatal("reference changes between parts")
									}
								}
							}
							if concatCount != 1 || callerCount != 1 {
								t.Fatalf("concat=%d caller=%d", concatCount, callerCount)
							}
							payload, err := part.Message.GetMessageData()
							if err != nil {
								t.Fatal(err)
							}
							budget := 160 - (len(hb)*8+6)/7
							end := min(consumed+budget, len(allSeptets))
							if end > consumed && allSeptets[end-1] == 0x1b {
								end--
							}
							if end == consumed {
								t.Fatal("oracle cannot fit next character")
							}
							expectedPayload := allSeptets[consumed:end]
							if packed {
								expectedPayload = nliExpectedPacked(t, expectedPayload, fill)
							}
							if !bytes.Equal(payload, expectedPayload) {
								t.Fatalf("segment%d payload=%x want greedy escape-safe %x", index, payload, expectedPayload)
							}
							consumed = end
							var septets []byte
							if packed {
								if len(hb)+len(payload) > 140 {
									t.Fatalf("radio bytes=%d", len(hb)+len(payload))
								}
								// Use an exact count inferred from retained source below;
								// the independent raw unpacker sees at most one padding septet.
								septets = gsm7.Unpack7Bit(payload, fill)
							} else {
								septets = payload
								if len(septets) > budget {
									t.Fatalf("septets%d budget%d", len(septets), budget)
								}
							}
							if len(septets) > 0 && septets[len(septets)-1] == 0x1b {
								t.Fatal("split ended with ESC")
							}
							textPart, err := part.Message.GetMessageWithEncoding(nliCodec(t, tc, packed))
							if err != nil {
								t.Fatal(err)
							}
							joined.WriteString(textPart)
							for _, id := range []uint32{4, 5} {
								nliCheckCompleteFrame(t, id, part.Message, tc, packed, textPart)
							}
						}
						if consumed != len(allSeptets) {
							t.Fatalf("consumed%d septets of%d", consumed, len(allSeptets))
						}
						if joined.String() != text {
							t.Fatalf("reassembled text mismatch: got%q want%q", joined.String(), text)
						}
					})
				}
			}
		}
	}
	if len(alignments) != 7 {
		t.Fatalf("tested fill values%v; want all0..6", alignments)
	}
}

func TestNationalLanguageCanonicalHeaderBudgets(t *testing.T) {
	for _, tc := range []struct {
		language         nliCase
		header, capacity int
	}{
		{nliCase{"default", 0, 0, "A", []byte{0x41}}, 6, 153},
		{nliCase{"Turkish locking", 1, 0, "Ğ", []byte{0x0b}}, 9, 149},
		{nliCase{"Turkish both", 1, 1, "Ğ", []byte{0x0b}}, 12, 146},
	} {
		for _, packed := range []bool{false, true} {
			t.Run(fmt.Sprintf("H%d/packed%t", tc.header, packed), func(t *testing.T) {
				var m ShortMessage
				if err := m.SetLongMessageWithEnc(strings.Repeat(tc.language.text, 181), nliCodec(t, tc.language, packed)); err != nil {
					t.Fatal(err)
				}
				parts, err := m.split()
				if err != nil {
					t.Fatal(err)
				}
				if len(parts) != 2 {
					t.Fatalf("parts%d want2", len(parts))
				}
				for i, part := range parts {
					if h := part.UDH().UDHL(); h != tc.header {
						t.Fatalf("header%d want%d", h, tc.header)
					}
					chars := tc.capacity
					if i == 1 {
						chars = 181 - tc.capacity
					}
					want := strings.Repeat(tc.language.text, chars)
					nliCheckCompleteFrame(t, 4, *part, tc.language, packed, want)
					nliCheckCompleteFrame(t, 5, *part, tc.language, packed, want)
				}
			})
		}
	}
}

func TestNationalLanguageSendConflictsAreAtomic(t *testing.T) {
	tr := nliCases[0]
	var m ShortMessage
	if err := m.SetMessageWithEncoding("Ğ", nliCodec(t, tr, false)); err != nil {
		t.Fatal(err)
	}
	before := nliMarshal(t, nliSendingPDU(t, 4, m))
	if err := m.SetUDH(UDH{{ID: 0x25, Data: []byte{3}}}); err == nil {
		t.Fatal("conflicting locking IE accepted")
	}
	if got := nliMarshal(t, nliSendingPDU(t, 4, m)); !bytes.Equal(got, before) {
		t.Fatal("failed header setter changed message")
	}
	if err := m.SetMessageWithEncoding("A", data.GSM7BIT); err == nil {
		t.Fatal("default codec accepted non-default header")
	}
	if got := nliMarshal(t, nliSendingPDU(t, 4, m)); !bytes.Equal(got, before) {
		t.Fatal("failed codec setter changed message")
	}
	var plain ShortMessage
	if err := plain.SetMessageWithEncoding("A", nliCodec(t, nliCase{}, false)); err != nil {
		t.Fatal(err)
	}
	if len(plain.UDH()) != 0 {
		t.Fatalf("default codec emitted NLI0: %+v", plain.UDH())
	}
	nliCheckCompleteFrame(t, 4, plain, nliCase{}, false, "A")
}

func TestNationalLanguageReceivedMessagesResegmentWithoutChangingSeptets(t *testing.T) {
	for _, tc := range nliCases {
		for _, packed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/packed%t", tc.name, packed), func(t *testing.T) {
				// Incoming SMPP can hold more unpacked septets than one radio SMS.
				// Remarshal before decoding to ensure receive preserves raw framing.
				text := strings.Repeat(tc.text, 81)
				septets := nliExpectedSeptets(t, text, tc)
				for len(septets) < 170 {
					text += tc.text
					septets = nliExpectedSeptets(t, text, tc)
				}
				header := UDH{{ID: 0x70, Data: []byte{0xab}}}
				if tc.locking != 0 {
					header = append(header, InfoElement{ID: 0x25, Data: []byte{byte(tc.locking)}})
				}
				if tc.single != 0 {
					header = append(header, InfoElement{ID: 0x24, Data: []byte{byte(tc.single)}})
				}
				hb := nliHeader(header...)
				payload := septets
				if packed {
					fill := (7 - len(hb)*8%7) % 7
					payload = nliExpectedPacked(t, septets, fill)
					// Retain the existing raw-packed ambiguity guard: choose
					// a message without a whole spare padding septet.
					for len(gsm7.Unpack7Bit(payload, fill)) > len(septets) {
						text += tc.text
						septets = nliExpectedSeptets(t, text, tc)
						payload = nliExpectedPacked(t, septets, fill)
					}
				}
				wire := nliFrame(4, 0, hb, payload)
				p, m := nliParsedMessage(t, wire)
				if got := nliMarshal(t, p); !bytes.Equal(got, wire) {
					t.Fatal("raw incoming wire changed")
				}
				if packed {
					if err := m.SetMessageDataWithEncoding(payload, data.GSM7BITPACKED); err != nil {
						t.Fatal(err)
					}
				}
				parts, err := p.(*SubmitSM).Split()
				if err != nil {
					t.Fatal(err)
				}
				if len(parts) < 2 {
					t.Fatalf("expected received long message to split, got%d", len(parts))
				}
				var joined strings.Builder
				for _, part := range parts {
					var piece string
					if packed {
						piece, err = part.Message.GetMessageWithEncoding(data.GSM7BITPACKED)
					} else {
						piece, err = part.Message.GetMessage()
					}
					if err != nil {
						t.Fatal(err)
					}
					joined.WriteString(piece)
					nliCheckCompleteFrame(t, 4, part.Message, tc, packed, piece)
				}
				if joined.String() != text {
					t.Fatalf("resegmented text%q want%q", joined.String(), text)
				}
			})
		}
	}
}

func TestNationalLanguagePackedReceiveUsesHeader(t *testing.T) {
	for _, tc := range nliCases {
		for extra := 0; extra < 7; extra++ {
			t.Run(fmt.Sprintf("%s/extra%d", tc.name, extra), func(t *testing.T) {
				ies := UDH{{ID: 0x70, Data: bytes.Repeat([]byte{0xab}, extra)}}
				if tc.locking != 0 {
					ies = append(ies, InfoElement{ID: 0x25, Data: []byte{byte(tc.locking)}})
				}
				if tc.single != 0 {
					ies = append(ies, InfoElement{ID: 0x24, Data: []byte{byte(tc.single)}})
				}
				header := nliHeader(ies...)
				payload := nliExpectedPacked(t, tc.septets, (7-len(header)*8%7)%7)
				for _, id := range []uint32{4, 5} {
					wire := nliFrame(id, 0, header, payload)
					p, m := nliParsedMessage(t, wire)
					got, err := m.GetMessageWithEncoding(data.GSM7BITPACKED)
					if err != nil || got != tc.text {
						t.Fatalf("packed received text=%q,%v; want%q", got, err, tc.text)
					}
					if got := nliMarshal(t, p); !bytes.Equal(got, wire) {
						t.Fatal("packed incoming frame changed")
					}
				}
			})
		}
	}
}

func TestNationalLanguageReceiveZeroLengthIE(t *testing.T) {
	// A complete zero-length IE is distinct from a truncated header. It has
	// no NLI octet and does not affect the other header IEs or payload framing.
	for _, id := range []uint32{4, 5} {
		for _, kind := range []byte{0x24, 0x25} {
			header := []byte{2, kind, 0}
			wire := nliFrame(id, 0, header, []byte{4})
			p, m := nliParsedMessage(t, wire)
			if got, err := m.GetMessage(); err != nil || got != "è" {
				t.Fatalf("zero-length IE=%q,%v", got, err)
			}
			if len(m.UDH()) != 1 {
				t.Fatal("complete IE was not preserved")
			}
			if got := nliMarshal(t, p); !bytes.Equal(got, wire) {
				t.Fatal("zero-length IE forwarding changed")
			}
		}
	}
}

func TestNationalLanguageLongConstructorAndHeaderChanges(t *testing.T) {
	for _, packed := range []bool{false, true} {
		t.Run(fmt.Sprintf("packed%t", packed), func(t *testing.T) {
			tc := nliCases[0]
			enc := nliCodec(t, tc, packed)
			parts, err := NewLongMessageWithEncoding(strings.Repeat("Ğ", 181), enc)
			if err != nil {
				t.Fatal(err)
			}
			if len(parts) != 2 {
				t.Fatalf("constructor returned%d parts; want2", len(parts))
			}
			var text strings.Builder
			for _, part := range parts {
				piece, err := part.GetMessageWithEncoding(enc)
				if err != nil {
					t.Fatal(err)
				}
				text.WriteString(piece)
				for _, id := range []uint32{4, 5} {
					nliCheckCompleteFrame(t, id, *part, tc, packed, piece)
				}
			}
			if text.String() != strings.Repeat("Ğ", 181) {
				t.Fatal("long constructor lost text")
			}
			var m ShortMessage
			if err := m.SetMessageWithEncoding("ĞĞĞĞĞĞĞ", enc); err != nil {
				t.Fatal(err)
			}
			for size := 0; size < 7; size++ {
				header := UDH{{ID: 0x25, Data: []byte{1}}, {ID: 0x70, Data: bytes.Repeat([]byte{0xaa}, size)}}
				if err := m.SetUDH(header); err != nil {
					t.Fatal(err)
				}
				for _, id := range []uint32{4, 5} {
					nliCheckCompleteFrame(t, id, m, tc, packed, "ĞĞĞĞĞĞĞ")
				}
			}
		})
	}
}

func TestNationalLanguageReceivedFallbackSeptetsAreNotRecoded(t *testing.T) {
	// ESC 0x41 is unassigned in the default single-shift table and displays
	// locking-table A (TS 23.038 section 6.2.1.1). Re-encoding that text would
	// silently replace two received septets by one and change segment boundaries.
	original := bytes.Repeat([]byte{0x1b, 0x41}, 86)
	for _, packed := range []bool{false, true} {
		t.Run(fmt.Sprintf("packed%t", packed), func(t *testing.T) {
			header := []byte{3, 0x25, 1, 1}
			payload := original
			if packed {
				payload = nliExpectedPacked(t, original, (7-len(header)*8%7)%7)
			}
			p, m := nliParsedMessage(t, nliFrame(4, 0, header, payload))
			if packed {
				if err := m.SetMessageDataWithEncoding(payload, data.GSM7BITPACKED); err != nil {
					t.Fatal(err)
				}
			}
			parts, err := p.(*SubmitSM).Split()
			if err != nil {
				t.Fatal(err)
			}
			if len(parts) != 2 {
				t.Fatalf("received172 septets need2 parts, got%d", len(parts))
			}
			consumed := 0
			var joined strings.Builder
			for _, part := range parts {
				nliCheckHeader(t, part.Message.UDH(), nliCase{locking: 1})
				hb := nliHeader(part.Message.UDH()...)
				budget := 160 - (len(hb)*8+6)/7
				end := min(consumed+budget, len(original))
				if original[end-1] == 0x1b {
					end--
				}
				want := original[consumed:end]
				if packed {
					want = nliExpectedPacked(t, want, (7-len(hb)*8%7)%7)
				}
				got, err := part.Message.GetMessageData()
				if err != nil || !bytes.Equal(got, want) {
					t.Fatalf("raw fallback bytes=%x,%v; want%x", got, err, want)
				}
				for _, id := range []uint32{4, 5} {
					wire := nliMarshal(t, nliSendingPDU(t, id, part.Message))
					if expected := nliFrame(id, 0, hb, want); !bytes.Equal(wire, expected) {
						t.Fatal("complete raw fallback frame changed")
					}
					_, parsed := nliParsedMessage(t, wire)
					var text string
					if packed {
						text, err = parsed.GetMessageWithEncoding(data.GSM7BITPACKED)
					} else {
						text, err = parsed.GetMessage()
					}
					if err != nil || text != strings.Repeat("A", (end-consumed)/2) {
						t.Fatalf("fallback text%q,%v", text, err)
					}
					if id == 4 {
						joined.WriteString(text)
					}
				}
				consumed = end
			}
			if consumed != len(original) || joined.String() != strings.Repeat("A", 86) {
				t.Fatal("fallback reassembly changed")
			}
		})
	}
}

func TestNationalLanguageSingleShiftResetsBetweenConcatParts(t *testing.T) {
	for _, wide := range []bool{false, true} {
		var joined strings.Builder
		for index, tc := range []struct {
			nli     byte
			septets []byte
			want    string
		}{
			{1, []byte{0x1b, 0x47}, "Ğ"}, {2, []byte{0x1b, 0x41}, "Á"}, {0, []byte{0x1b, 0x41}, "A"},
		} {
			concat := NewIEConcatMessage(3, byte(index+1), 42)
			if wide {
				concat = NewIEConcatMessage16Bit(3, byte(index+1), 0x1234)
			}
			ies := UDH{concat}
			if tc.nli != 0 {
				ies = append(ies, InfoElement{ID: 0x24, Data: []byte{tc.nli}})
			}
			p, m := nliParsedMessage(t, nliFrame(5, 0, nliHeader(ies...), tc.septets))
			got, err := m.GetMessage()
			if err != nil || got != tc.want {
				t.Fatalf("wide%t part%d=%q,%v; want%q", wide, index, got, err, tc.want)
			}
			if raw := nliMarshal(t, p); !bytes.Equal(raw, nliFrame(5, 0, nliHeader(ies...), tc.septets)) {
				t.Fatal("mixed-table concatenation forwarding changed")
			}
			joined.WriteString(got)
		}
		if joined.String() != "ĞÁA" {
			t.Fatal("single shift leaked between segments")
		}
	}
}

func TestNationalLanguageCustomCodingPreservesWireValue(t *testing.T) {
	for _, packed := range []bool{false, true} {
		t.Run(fmt.Sprintf("packed%t", packed), func(t *testing.T) {
			tc := nliCases[0]
			enc := data.NewCustomEncoding(0x10, data.NewCustomEncoding(0, nliCodec(t, tc, packed)))
			var m ShortMessage
			if err := m.SetMessageWithEncoding("Ğ", enc); err != nil {
				t.Fatal(err)
			}
			nliCheckHeader(t, m.UDH(), tc)
			hb := nliHeader(m.UDH()...)
			payload := tc.septets
			if packed {
				payload = nliExpectedPacked(t, payload, (7-len(hb)*8%7)%7)
			}
			for _, id := range []uint32{4, 5} {
				wire := nliMarshal(t, nliSendingPDU(t, id, m))
				if expected := nliFrame(id, 0x10, hb, payload); !bytes.Equal(wire, expected) {
					t.Fatalf("custom coding frame=%x want%x", wire, expected)
				}
				_, parsed := nliParsedMessage(t, wire)
				if parsed.Encoding().DataCoding() != 0x10 {
					t.Fatal("wire coding was lost")
				}
				var got string
				var err error
				if packed {
					got, err = parsed.GetMessageWithEncoding(data.GSM7BITPACKED)
				} else {
					got, err = parsed.GetMessage()
				}
				if err != nil || got != "Ğ" {
					t.Fatalf("custom coding text=%q,%v", got, err)
				}
			}
		})
	}
}

func TestNationalLanguageRawPackedAmbiguityStillRejected(t *testing.T) {
	tc := nliCases[2]
	septets := nliExpectedSeptets(t, strings.Repeat(tc.text, 81), tc)
	header := nliHeader(InfoElement{ID: 0x70, Data: []byte{0xab}}, InfoElement{ID: 0x25, Data: []byte{1}}, InfoElement{ID: 0x24, Data: []byte{1}})
	fill := (7 - len(header)*8%7) % 7
	payload := nliExpectedPacked(t, septets, fill)
	if len(gsm7.Unpack7Bit(payload, fill)) != len(septets)+1 {
		t.Fatal("fixture must have a whole spare padding septet")
	}
	wire := nliFrame(4, 0, header, payload)
	p, m := nliParsedMessage(t, wire)
	if err := m.SetMessageDataWithEncoding(payload, data.GSM7BITPACKED); err != nil {
		t.Fatal(err)
	}
	if _, err := p.(*SubmitSM).Split(); err == nil {
		t.Fatal("ambiguous raw packed data must still require original text or a septet count")
	}
	if got := nliMarshal(t, p); !bytes.Equal(got, wire) {
		t.Fatal("failed raw split changed frame")
	}
}
