package pdu

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"reflect"
	"testing"

	"github.com/gomaja/go-smpp/data"
	"github.com/stretchr/testify/require"
)

func TestUnmarshalCStringStaysWithinDeclaredFrame(t *testing.T) {
	for _, id := range []data.CommandIDType{data.BIND_TRANSCEIVER_RESP, data.SUBMIT_SM_RESP, data.DATA_SM_RESP, data.DELIVER_SM_RESP, data.OUTBIND, data.QUERY_SM} {
		t.Run(fmt.Sprintf("%x", uint32(id)), func(t *testing.T) {
			wire := decodeTestFrame(id, []byte("unterminated"))
			following := decodeTestFrame(data.ENQUIRE_LINK, nil)
			buffer := NewBuffer(append(append([]byte(nil), wire...), following...))
			p, err := CreatePDUFromCmdID(id)
			require.NoError(t, err)
			err = p.Unmarshal(buffer)
			if id == data.OUTBIND || id == data.QUERY_SM {
				require.Error(t, err, "requests must retain their mandatory-field checks")
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, following, buffer.Bytes(), "a C-Octet reader consumed the following PDU")
		})
	}
}

func TestUnmarshalReusedPDUClearsAbsentOptionalParameters(t *testing.T) {
	for id := range pduMap {
		t.Run(fmt.Sprintf("%x", uint32(id)), func(t *testing.T) {
			p, err := CreatePDUFromCmdID(id)
			require.NoError(t, err)
			clean := NewBuffer(nil)
			p.Marshal(clean)
			require.NoError(t, clean.Err())
			withTLV := append(append([]byte(nil), clean.Bytes()...), 0x77, 0x77, 0, 1, 0xaa)
			binary.BigEndian.PutUint32(withTLV, uint32(len(withTLV)))
			require.NoError(t, p.Unmarshal(NewBuffer(withTLV)))
			require.Len(t, p.GetOptionalParameters(), 1)
			require.NoError(t, p.Unmarshal(NewBuffer(clean.Bytes())))
			require.Empty(t, p.GetOptionalParameters(), "previous frame's TLV leaked into a later frame")
			fresh, err := Parse(bytes.NewReader(clean.Bytes()))
			require.NoError(t, err)
			require.Equal(t, fresh, p)
		})
	}
}

func TestUnmarshalReusedResponseClearsOmittedBody(t *testing.T) {
	for _, id := range []data.CommandIDType{data.BIND_RECEIVER_RESP, data.BIND_TRANSMITTER_RESP, data.BIND_TRANSCEIVER_RESP, data.SUBMIT_SM_RESP, data.DATA_SM_RESP, data.DELIVER_SM_RESP, data.QUERY_SM_RESP, data.SUBMIT_MULTI_RESP} {
		for _, status := range []data.CommandStatusType{data.ESME_ROK, data.ESME_RSYSERR} {
			t.Run(fmt.Sprintf("%x/%x", uint32(id), uint32(status)), func(t *testing.T) {
				p, err := CreatePDUFromCmdID(id)
				require.NoError(t, err)
				switch c := p.(type) {
				case *BindResp:
					c.SystemID = "previous"
				case *QuerySMResp:
					c.MessageID, c.FinalDate, c.MessageState, c.ErrorCode = "previous", "261010123456000+", 8, 9
				case *SubmitMultiResp:
					c.MessageID = "previous"
					c.UnsuccessSMEs.Add(NewUnsuccessSME())
				default:
					reflect.ValueOf(p).Elem().FieldByName("MessageID").SetString("previous")
				}
				wire := decodeTestFrame(id, nil)
				binary.BigEndian.PutUint32(wire[8:], uint32(status))
				require.NoError(t, p.Unmarshal(NewBuffer(wire)))
				fresh, err := Parse(bytes.NewReader(wire))
				require.NoError(t, err)
				require.Equal(t, fresh, p, "omitted body retained fields from the previous response")
			})
		}
	}
}

func TestDataAndDeliverResponseTrailingCStringTolerance(t *testing.T) {
	for _, id := range []data.CommandIDType{data.DATA_SM_RESP, data.DELIVER_SM_RESP} {
		for _, text := range []string{"", "not-terminated", "A\x80\xff"} {
			for _, status := range []data.CommandStatusType{data.ESME_ROK, data.ESME_RSYSERR} {
				wire := decodeTestFrame(id, []byte(text))
				binary.BigEndian.PutUint32(wire[8:], uint32(status))
				following := decodeTestFrame(data.ENQUIRE_LINK, nil)
				reader := bytes.NewReader(append(append([]byte(nil), wire...), following...))
				p, err := Parse(reader)
				require.NoError(t, err, "cmd %x status %x text %q", uint32(id), uint32(status), text)
				require.Equal(t, text, reflect.ValueOf(p).Elem().FieldByName("MessageID").String())
				next, err := Parse(reader)
				require.NoError(t, err)
				require.Equal(t, data.ENQUIRE_LINK, next.GetHeader().CommandID)
				encoded := NewBuffer(nil)
				p.Marshal(encoded)
				require.NoError(t, encoded.Err())
				require.Equal(t, append([]byte(text), 0), encoded.Bytes()[16:])
			}
		}
		_, err := Parse(bytes.NewReader(decodeTestFrame(id, []byte{'A', 0, 4, 0})))
		var decoded *DecodeError
		require.ErrorAs(t, err, &decoded)
		require.Equal(t, data.ESME_RINVOPTPARSTREAM, decoded.Status)
	}
}

func FuzzUnmarshalFrameIsolation(f *testing.F) {
	for _, fixture := range specificationBodies() {
		f.Add(fixture.id, []byte(fixture.body))
	}
	f.Add(uint32(0x80000009), []byte("unterminated"))
	f.Add(uint32(0x80000103), []byte("unterminated"))
	f.Fuzz(func(t *testing.T, command uint32, body []byte) {
		p, err := CreatePDUFromCmdID(data.CommandIDType(command))
		if err != nil || len(body) > data.MAX_PDU_LEN-16 {
			return
		}
		wire := decodeTestFrame(data.CommandIDType(command), body)
		following := decodeTestFrame(data.ENQUIRE_LINK, nil)
		buffer := NewBuffer(append(append([]byte(nil), wire...), following...))
		p.AppendOptionalParam(NewField(Tag(0x7777), []byte("previous")))
		decodeErr := p.Unmarshal(buffer)
		require.Equal(t, following, buffer.Bytes(), "Unmarshal crossed command_length")
		fresh, parseErr := Parse(bytes.NewReader(wire))
		require.Equal(t, parseErr == nil, decodeErr == nil)
		if decodeErr == nil {
			require.Equal(t, fresh, p, "reusing a PDU changed decoded fields")
		}
	})
}
