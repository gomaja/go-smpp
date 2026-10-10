package pdu

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/gomaja/go-smpp/data"
)

func TestErrorResponsesOmitBodyUnlessTLVs(t *testing.T) {
	for _, create := range []func() PDU{NewBindReceiverResp, NewBindTransmitterResp, NewBindTransceiverResp, NewSubmitSMResp} {
		for _, tlv := range []bool{false, true} {
			response := create()
			switch r := response.(type) {
			case *BindResp:
				r.CommandStatus = data.ESME_RBINDFAIL
				r.SystemID = "ignored"
			case *SubmitSMResp:
				r.CommandStatus = data.ESME_RSYSERR
				r.MessageID = "ignored"
			}
			if tlv {
				response.RegisterOptionalParam(NewField(TagAdditionalStatusInfoText, []byte("error\x00")))
			}
			b := NewBuffer(nil)
			response.Marshal(b)
			if !tlv && (b.Len() != 16 || binary.BigEndian.Uint32(b.Bytes()[:4]) != 16) {
				t.Errorf("%T error length=%d, want 16", response, b.Len())
			}
			parsed, err := Parse(bytes.NewReader(b.Bytes()))
			if err != nil {
				t.Fatal(err)
			}
			if tlv {
				var field Field
				switch r := parsed.(type) {
				case *BindResp:
					field = r.OptionalParameters[TagAdditionalStatusInfoText]
				case *SubmitSMResp:
					field = r.OptionalParameters[TagAdditionalStatusInfoText]
				}
				if !bytes.Equal(field.Data, []byte("error\x00")) {
					t.Fatalf("%T lost error TLV", parsed)
				}
			}
		}
	}
}
