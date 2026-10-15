package pdu

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/gomaja/go-smpp/data"
	"github.com/gomaja/go-sms/encoding/gsm7"
)

func TestNationalLanguageIEConstructors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		id     byte
		makeIE func(int) (InfoElement, error)
	}{
		{"single", 0x24, NewIENationalLanguageSingleShift},
		{"locking", 0x25, NewIENationalLanguageLockingShift},
	} {
		for _, selector := range []int{-1, 0, 1, 2, 3, 6, 13, 14, 255, 256} {
			ie, err := tc.makeIE(selector)
			valid := selector > 0 && selector < 14 && (tc.id != 0x25 || selector != 2)
			if !valid {
				if err == nil {
					t.Errorf("%s(%d) accepted invalid selector", tc.name, selector)
				}
				continue
			}
			if err != nil || ie.ID != tc.id || !bytes.Equal(ie.Data, []byte{byte(selector)}) {
				t.Errorf("%s(%d)=%+v,%v", tc.name, selector, ie, err)
			}
		}
	}
}

func TestNationalLanguageIgnoredForBinaryCodings(t *testing.T) {
	for _, coding := range []byte{2, 4} {
		payload := []byte{0xff, 0, 0x1b}
		wire := nliFrame(5, coding, []byte{3, 0x25, 1, 6}, payload)
		packet, message := nliParsedMessage(t, wire)
		if _, err := message.GetMessage(); !errors.Is(err, data.ErrNotImplDecode) {
			t.Fatalf("binary coding %d attempted text decoding: %v", coding, err)
		}
		got, err := message.GetMessageData()
		if err != nil || !bytes.Equal(got, payload) || !bytes.Equal(nliMarshal(t, packet), wire) {
			t.Fatalf("binary coding %d changed received bytes", coding)
		}
	}
}

func TestNationalLanguageSendingCannotHideIEsInIgnoredUDH(t *testing.T) {
	wire := nliFrame(5, 0, []byte{6, 0x25, 1, 6, 0x24, 2, 1}, []byte{4})
	packet, message := nliParsedMessage(t, wire)
	codec, err := data.NewGSM7(data.GSM7Options{LockingShift: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := message.SetLongMessageWithEnc("Ğ", codec); err == nil {
		t.Fatal("selected NLI would be hidden behind ignored raw header")
	}
	if !bytes.Equal(nliMarshal(t, packet), wire) {
		t.Fatal("failed setter changed ignored header or payload")
	}
	if err := message.SetUDH(UDH{{ID: 0x70, Data: []byte{1}}}); err != nil {
		t.Fatal(err)
	}
	if err := message.SetMessageWithEncoding("Ğ", codec); err != nil {
		t.Fatal(err)
	}
	_, received := nliParsedMessage(t, nliMarshal(t, packet))
	if text, err := received.GetMessage(); err != nil || text != "Ğ" {
		t.Fatalf("replacement header text=%q,%v", text, err)
	}
}

func TestNationalLanguageRawDoubleEscapeStaysTogether(t *testing.T) {
	// ESC ESC displays a space, then 65 is ordinary 'e' (TS 23.038
	// section 6.2.1.1). Splitting the pair changes the second ESC + 65 to €.
	septets := append(bytes.Repeat([]byte{'A'}, 147), 0x1b, 0x1b, 0x65)
	septets = append(septets, bytes.Repeat([]byte{'A'}, 10)...)
	for _, packed := range []bool{false, true} {
		header := UDH{{ID: 0x25, Data: []byte{1}}}
		payload := septets
		codec := data.GSM7BIT
		if packed {
			payload = nliExpectedPacked(t, septets, packedFillBits(header.UDHL()))
			codec = data.GSM7BITPACKED
		}
		p := NewSubmitSM().(*SubmitSM)
		if err := p.Message.SetUDH(header); err != nil {
			t.Fatal(err)
		}
		if err := p.Message.SetMessageDataWithEncoding(payload, codec); err != nil {
			t.Fatal(err)
		}
		parts, err := p.Split()
		if err != nil || len(parts) != 2 {
			t.Fatalf("packed=%v split=%d,%v", packed, len(parts), err)
		}
		var joined strings.Builder
		for i, part := range parts {
			_, received := nliParsedMessage(t, nliMarshal(t, part))
			text, err := received.GetMessageWithEncoding(codec)
			if err != nil {
				t.Fatal(err)
			}
			joined.WriteString(text)
			dataBytes, err := part.Message.GetMessageData()
			if err != nil {
				t.Fatal(err)
			}
			if packed {
				dataBytes = gsm7.Unpack7BitCR(dataBytes, packedFillBits(part.Message.headerLength()))
			}
			if i == 0 && len(dataBytes) != 149 {
				t.Errorf("packed=%v first septet budget=%d; want149", packed, len(dataBytes))
			}
		}
		want := strings.Repeat("A", 147) + " e" + strings.Repeat("A", 10)
		if joined.String() != want {
			t.Errorf("packed=%v decoded=%q; want%q", packed, joined.String(), want)
		}
	}
}

func TestNationalLanguageSetUDHPreservesConfiguredSelectors(t *testing.T) {
	enc, err := data.NewGSM7(data.GSM7Options{LockingShift: 1, SingleShift: 1})
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewShortMessageWithEncoding("Ğ", enc)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.SetUDH(UDH{{ID: 0x70, Data: []byte{7}}}); err != nil {
		t.Fatal(err)
	}
	nliCheckHeader(t, m.UDH(), nliCase{locking: 1, single: 1})
	previous, err := m.UDH().MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	if err := m.SetUDH(UDH{{ID: 0x25, Data: []byte{3}}}); err == nil {
		t.Fatal("conflicting replacement header accepted")
	}
	after, err := m.UDH().MarshalBinary()
	if err != nil || !bytes.Equal(previous, after) {
		t.Fatal("failed SetUDH changed header")
	}
}

func TestNationalLanguageLeavesLegacyEmptyLongMessagesUnchanged(t *testing.T) {
	for _, enc := range []data.Encoding{data.GSM7BIT, data.ASCII, data.UCS2, data.BINARY8BIT1, data.BINARY8BIT2} {
		parts, err := NewLongMessageWithEncoding("", enc)
		if err != nil || len(parts) != 1 {
			t.Errorf("%T empty constructor: %d parts, %v", enc, len(parts), err)
			continue
		}
		payload, err := parts[0].GetMessageData()
		if err != nil || payload != nil {
			t.Errorf("%T legacy empty long-message payload must remain nil: %x, %v", enc, payload, err)
		}
	}
}

func TestNationalLanguageEscapedZeroFitsPackedBudget(t *testing.T) {
	for _, terminal := range []byte{0, 0x0d} {
		// H133 becomes H138 after concat; fill2 leaves two payload octets.
		// ESC 00 fits and must not be mistaken for a padding septet. ESC CR
		// needs three octets under the existing CR-doubling rule, so cannot fit.
		header := UDH{{ID: 0x70, Data: bytes.Repeat([]byte{7}, 124)}, {ID: 0x24, Data: []byte{1}}, {ID: 0x25, Data: []byte{1}}}
		septets := bytes.Repeat([]byte{0x1b, terminal}, 6)
		payload := nliExpectedPacked(t, septets, packedFillBits(header.UDHL()))
		p := NewSubmitSM().(*SubmitSM)
		if err := p.Message.SetUDH(header); err != nil {
			t.Fatal(err)
		}
		if err := p.Message.SetMessageDataWithEncoding(payload, data.GSM7BITPACKED); err != nil {
			t.Fatal(err)
		}
		before := nliMarshal(t, p)
		parts, err := p.Split()
		if terminal == 0x0d {
			if err == nil {
				t.Fatal("CR doubling must not exceed the payload budget")
			}
			if !bytes.Equal(before, nliMarshal(t, p)) {
				t.Fatal("failed split changed source")
			}
			continue
		}
		if err != nil || len(parts) != 6 {
			t.Fatalf("escaped zero split=%d,%v; want6", len(parts), err)
		}
		for _, part := range parts {
			got, err := part.Message.GetMessageData()
			if err != nil || !bytes.Equal(got, []byte{0x6c, 0}) {
				t.Fatalf("packed escaped zero=%x,%v", got, err)
			}
			_, received := nliParsedMessage(t, nliMarshal(t, part))
			text, err := received.GetMessageWithEncoding(data.GSM7BITPACKED)
			if err != nil || text != "@" {
				t.Fatalf("escaped zero text=%q,%v", text, err)
			}
		}
	}
}
