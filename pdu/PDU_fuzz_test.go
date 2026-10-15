package pdu

import (
	"bytes"
	"encoding/hex"
	stderrors "errors"
	"reflect"
	"testing"
	"unicode/utf8"

	"github.com/gomaja/go-smpp/data"
)

func FuzzParse(f *testing.F) {
	for _, fixture := range specificationBodies() {
		f.Add(decodeTestFrame(data.CommandIDType(fixture.id), []byte(fixture.body)))
	}
	for _, id := range []data.CommandIDType{data.DATA_SM_RESP, data.DELIVER_SM_RESP} {
		f.Add(decodeTestFrame(id, []byte("unterminated")))
		f.Add(decodeTestFrame(id, bytes.Repeat([]byte{'I'}, data.MAX_PDU_LEN-16)))
	}
	for _, seed := range []string{
		"",
		"0000000f800000060000000000000001",
		"00000010000000150000000000000001",
		"00000010800000150000000000000001",
		"00000010800000010000000d00000004",
		"00000010800000040000005800000001",
		"0000001180000004000000580000000100",
		"00000011800000090000000d0000000100",
		"000000100000ffff0000000000000001",
	} {
		raw, err := hex.DecodeString(seed)
		if err != nil {
			f.Fatalf("invalid fuzz seed %q: %v", seed, err)
		}
		f.Add(raw)
	}
	// Latin-1, UTF-8 and arbitrary non-NUL C-Octet values must survive.
	for _, text := range []string{"é", "A\x80\xff", "\xfe\xc3"} {
		p := NewSubmitSM().(*SubmitSM)
		p.ServiceType = text
		var err error
		p.SourceAddr, err = NewAddressWithAddr(text)
		if err != nil {
			f.Fatal(err)
		}
		p.DestAddr = p.SourceAddr
		b := NewBuffer(nil)
		p.Marshal(b)
		f.Add(b.Bytes())
	}
	for _, p := range []PDU{NewDataSM(), NewAlertNotification()} {
		a, err := NewAddressWithAddr(string(bytes.Repeat([]byte{'A'}, 64)))
		if err != nil {
			f.Fatal(err)
		}
		switch v := p.(type) {
		case *DataSM:
			v.SourceAddr = a
			v.DestAddr = a
		case *AlertNotification:
			v.SourceAddr = a
			v.EsmeAddr = a
		}
		b := NewBuffer(nil)
		p.Marshal(b)
		if b.Err() != nil {
			f.Fatal(b.Err())
		}
		f.Add(b.Bytes())
	}
	ignored := NewSubmitSM().(*SubmitSM)
	ignored.EsmClass = data.SM_UDH_GSM
	if err := ignored.Message.SetMessageDataWithEncoding([]byte{4, 0, 3, 1, 2, 'h', 'i'}, data.GSM7BIT); err != nil {
		f.Fatal(err)
	}
	b := NewBuffer(nil)
	ignored.Marshal(b)
	f.Add(b.Bytes())
	// Unknown, zero-length, high-bit-length and repeatable callback TLVs.
	for _, fields := range [][]Field{
		{NewField(Tag(0x7777), []byte{1, 2, 3})},
		{NewField(TagAlertOnMessageDelivery, nil)},
		{NewField(TagMessagePayload, make([]byte, 32768))},
		{NewField(TagCallbackNum, []byte{1, 1, 1, '1'}), NewField(TagCallbackNum, []byte{1, 1, 1, '2'})},
	} {
		p := NewDataSM()
		for _, field := range fields {
			p.AppendOptionalParam(field)
		}
		b := NewBuffer(nil)
		p.Marshal(b)
		f.Add(b.Bytes())
	}

	// Peer inputs exceed specification maxima but remain wire-representable.
	peerSubmit := NewBuffer(nil)
	NewSubmitSM().Marshal(peerSubmit)
	peerBody := append([]byte(nil), peerSubmit.Bytes()[16:]...)
	longService := append(bytes.Repeat([]byte{'S'}, 512), peerBody...)
	f.Add(decodeTestFrame(data.SUBMIT_SM, longService))
	longAddress := append([]byte(nil), peerBody[:3]...)
	longAddress = append(longAddress, bytes.Repeat([]byte{'A'}, 512)...)
	longAddress = append(longAddress, peerBody[3:]...)
	f.Add(decodeTestFrame(data.SUBMIT_SM, longAddress))
	peerBody[len(peerBody)-1] = 255
	f.Add(decodeTestFrame(data.SUBMIT_SM, append(peerBody, bytes.Repeat([]byte{'A'}, 255)...)))
	peerMulti := NewBuffer(nil)
	NewSubmitMulti().Marshal(peerMulti)
	multiBody := peerMulti.Bytes()[16:]
	allDestinations := append([]byte(nil), multiBody[:4]...)
	allDestinations = append(allDestinations, 255)
	for range 255 {
		allDestinations = append(allDestinations, 1, 0, 0, 0)
	}
	allDestinations = append(allDestinations, multiBody[5:]...)
	f.Add(decodeTestFrame(data.SUBMIT_MULTI, allDestinations))
	longList := append([]byte(nil), multiBody[:4]...)
	longList = append(longList, 1, 2)
	longList = append(longList, bytes.Repeat([]byte{'L'}, 512)...)
	longList = append(longList, 0)
	longList = append(longList, multiBody[5:]...)
	f.Add(decodeTestFrame(data.SUBMIT_MULTI, longList))
	// Adding a tolerated response terminator must still obey MAX_PDU_LEN.
	f.Add(decodeTestFrame(data.BIND_TRANSCEIVER_RESP, bytes.Repeat([]byte{'I'}, data.MAX_PDU_LEN-16)))
	for _, binding := range []BindingType{Receiver, Transmitter, Transceiver} {
		p := NewBindRequest(binding)
		p.SystemID = string(bytes.Repeat([]byte{'I'}, 128))
		p.Password = string(bytes.Repeat([]byte{'P'}, 128))
		wire := NewBuffer(nil)
		p.Marshal(wire)
		if wire.Err() != nil {
			f.Fatal(wire.Err())
		}
		f.Add(wire.Bytes())
	}

	f.Add([]byte("\x00\x00\x00\x11\x80\x00\x00\x04000000000"))
	f.Add([]byte("\x00\x00\x00\x11\x80\x00\x00\x09300000000"))
	f.Fuzz(func(t *testing.T, raw []byte) {
		// Even rejected arbitrary frames must exercise the inverse property:
		// every PDU produced successfully by this library must parse.
		generated := NewSubmitSM().(*SubmitSM)
		content := bytes.ReplaceAll(raw, []byte{0}, nil)
		generated.ServiceType = string(content[:min(len(content), 5)])
		address, err := NewAddressWithAddr(string(content[:min(len(content), 20)]))
		if err != nil {
			t.Fatal(err)
		}
		generated.SourceAddr = address
		generated.DestAddr = address
		generated.AppendOptionalParam(NewField(Tag(0x7777), append([]byte(nil), raw[:min(len(raw), 65400)]...)))
		own := NewBuffer(nil)
		generated.Marshal(own)
		if own.Err() != nil {
			t.Fatalf("marshal bounded generated PDU: %v", own.Err())
		}
		ownParsed, err := Parse(bytes.NewReader(own.Bytes()))
		if err != nil {
			t.Fatalf("library rejected its own bytes: %v", err)
		}
		ownRound := NewBuffer(nil)
		ownParsed.Marshal(ownRound)
		if ownRound.Err() != nil || !bytes.Equal(own.Bytes(), ownRound.Bytes()) {
			t.Fatal("generated PDU changed after wire round trip")
		}

		ids := []data.CommandIDType{data.BIND_RECEIVER, data.BIND_TRANSMITTER, data.BIND_TRANSCEIVER, data.BIND_RECEIVER_RESP, data.BIND_TRANSMITTER_RESP, data.BIND_TRANSCEIVER_RESP, data.OUTBIND, data.SUBMIT_SM, data.SUBMIT_SM_RESP, data.SUBMIT_MULTI, data.SUBMIT_MULTI_RESP, data.DELIVER_SM, data.DELIVER_SM_RESP, data.DATA_SM, data.DATA_SM_RESP, data.QUERY_SM, data.QUERY_SM_RESP, data.CANCEL_SM, data.CANCEL_SM_RESP, data.REPLACE_SM, data.REPLACE_SM_RESP, data.ALERT_NOTIFICATION, data.UNBIND, data.UNBIND_RESP, data.ENQUIRE_LINK, data.ENQUIRE_LINK_RESP, data.GENERIC_NACK}
		selector := 0
		if len(raw) > 0 {
			selector = int(raw[0]) % len(ids)
		}
		variant, err := CreatePDUFromCmdID(ids[selector])
		if err != nil {
			t.Fatal(err)
		}
		variant.AppendOptionalParam(NewField(Tag(0x7777), append([]byte(nil), raw[:min(len(raw), 65000)]...)))
		variantWire := NewBuffer(nil)
		variant.Marshal(variantWire)
		if variantWire.Err() != nil {
			t.Fatalf("marshal generated %T: %v", variant, variantWire.Err())
		}
		variantParsed, err := Parse(bytes.NewReader(variantWire.Bytes()))
		if err != nil {
			t.Fatalf("library rejected generated %T: %v", variant, err)
		}
		variantRound := NewBuffer(nil)
		variantParsed.Marshal(variantRound)
		if variantRound.Err() != nil || !bytes.Equal(variantWire.Bytes(), variantRound.Bytes()) {
			t.Fatalf("generated %T changed bytes", variant)
		}

		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("Parse panicked for %x: %v", raw, r)
			}
		}()

		parsed, err := Parse(bytes.NewReader(raw))
		if err != nil || parsed == nil {
			var decoded *DecodeError
			if stderrors.As(err, &decoded) {
				if len(raw) < 16 {
					t.Fatal("decode error without a complete header")
				}
				var header [16]byte
				copy(header[:], raw)
				if decoded.Header != ParseHeader(header) || int(decoded.Header.CommandLength) > len(raw) {
					t.Fatalf("decode error lost the complete frame header: %+v", decoded.Header)
				}
				if decoded.Status == data.ESME_ROK || decoded.Err == nil {
					t.Fatal("decode error without response status or cause")
				}
			}
			return
		}

		originalHeader := parsed.GetHeader()
		buf := NewBuffer(nil)
		parsed.Marshal(buf)
		if err := buf.Err(); err != nil {
			// An unterminated tolerated response may need one extra NUL and exceed
			// MAX_PDU_LEN; no other parsed frame can fail wire representability.
			h := parsed.GetHeader()
			if !stderrors.Is(err, ErrEncode) || h.CommandLength != data.MAX_PDU_LEN || !trailingResponseString(parsed) || buf.Len() != 0 {
				t.Fatalf("marshal parsed %T: %v", parsed, err)
			}
			return
		}
		reparsed, err := Parse(bytes.NewReader(buf.Bytes()))
		if err != nil {
			t.Fatalf("reparse of marshaled %T failed: %v", parsed, err)
		}
		if normalizedErrorResponse(parsed) {
			if buf.Len() != 16 || len(reparsed.GetOptionalParameters()) != 0 {
				t.Fatal("failed response must normalize to a bare header")
			}
			want := originalHeader
			want.CommandLength = 16
			if reparsed.GetHeader() != want {
				t.Fatal("response normalization changed header")
			}
			return
		}
		if !reflect.DeepEqual(parsed, reparsed) {
			t.Fatalf("parsed content changed after marshal/reparse: %T\n%#v\n%#v", parsed, parsed, reparsed)
		}
	})
}

func FuzzUDHParse(f *testing.F) {
	for _, seed := range [][]byte{
		{}, {2, 0, 3, 'A', 'B', 'C', 'D'}, {6, 0, 3, 1, 2, 1, 0x70, 'A'},
		{5, 0, 3, 1, 2, 1}, {6, 8, 4, 0, 1, 2, 1}, {2, 0x70, 0, 'A'}, {4, 0, 3, 1, 2, 'h', 'i'}, {1, 0}, {0},
		// TS 23.040 sections 9.2.3.24.15–16: single/locking NLIs,
		// repeats, reserved selectors, and a bounded truncated final IE.
		{3, 0x25, 1, 1, 0x0b}, {3, 0x24, 1, 2, 0x1b, 0x41},
		{3, 0x25, 1, 3, 4}, {3, 0x25, 1, 6, 3},
		{6, 0x25, 1, 1, 0x25, 1, 255, 4},
		{6, 0x25, 1, 1, 0x24, 1, 1, 0x0b, 0x1b, 0x14},
		{3, 0x25, 1, 0, 4}, {6, 0x25, 1, 6, 0x24, 2, 1, 3},
		// Packed Turkish H4/fill3: terminal @/zero spare, CR spare,
		// and doubled CR (TS 23.038 §6.1.2.3.1).
		{3, 0x25, 1, 1, 0x08, 0x06, 0x83, 0xc1, 0x60, 0x30, 0x18, 0x0c, 0x06, 0x01},
		{3, 0x25, 1, 1, 0x08, 0x06, 0x83, 0xc1, 0x60, 0x30, 0x18, 0x0c, 0x06, 0x1b},
		{3, 0x25, 1, 1, 0x08, 0x06, 0x83, 0xc1, 0x60, 0x30, 0x18, 0x0c, 0x06, 0x1b, 0x0d},
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 0 && len(raw) <= 254 {
			p := NewSubmitSM().(*SubmitSM)
			if err := p.Message.SetMessageDataWithEncoding(raw, data.GSM7BIT); err != nil {
				t.Fatal(err)
			}
			wire := NewBuffer(nil)
			p.Marshal(wire)
			if wire.Err() != nil {
				t.Fatal(wire.Err())
			}
			// Craft peer input independently of the encoder's UDHI guard.
			wire.Bytes()[16+7] = data.SM_UDH_GSM
			parsed, err := Parse(bytes.NewReader(wire.Bytes()))
			if 1+int(raw[0]) <= len(raw) {
				if err != nil {
					t.Fatalf("bounded UDH must preserve PDU and payload: %v", err)
				}
				message := &parsed.(*SubmitSM).Message
				for _, codec := range []data.Encoding{data.GSM7BIT, data.GSM7BITPACKED} {
					text, err := message.GetMessageWithEncoding(codec)
					if err != nil || !utf8.ValidString(text) {
						t.Fatalf("bounded GSM7 UDH decoding: %q, %v", text, err)
					}
				}
				round := NewBuffer(nil)
				parsed.Marshal(round)
				if round.Err() != nil || !bytes.Equal(wire.Bytes(), round.Bytes()) {
					t.Fatal("ignored or parsed UDH changed wire bytes")
				}
			} else if err == nil {
				t.Fatal("truncated declared UDH was accepted")
			}
		}
		var u UDH
		n, err := u.UnmarshalBinary(raw)
		if err != nil {
			return
		}
		if n != 1+int(raw[0]) {
			t.Fatalf("consumed %d bytes beyond declared header %d", n, raw[0])
		}
		wire, err := u.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(raw[:n], wire) {
			t.Fatalf("header changed: %x -> %x", raw[:n], wire)
		}
	})
}

func trailingResponseString(p PDU) bool {
	switch p.(type) {
	case *BindResp, *SubmitSMResp, *DataSMResp, *DeliverSMResp:
		return true
	}
	return false
}
func normalizedErrorResponse(p PDU) bool {
	switch p.(type) {
	case *BindResp, *SubmitSMResp:
		return p.GetHeader().CommandStatus != data.ESME_ROK && len(p.GetOptionalParameters()) == 0
	}
	return false
}
