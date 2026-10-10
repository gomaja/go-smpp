package pdu

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strings"
	"testing"

	"github.com/gomaja/go-smpp/data"
	"github.com/stretchr/testify/require"
)

// Each body is assembled independently of Marshal in the field order of SMPP
// v3.4 Issue 1.2 Tables 4-1–4-30. Distinct values expose shifted octet fields.
func specificationBodies() []struct {
	name string
	id   uint32
	body string
} {
	bind := "id\x00pw\x00st\x00\x34\x01\x09range\x00"
	addresses := "\x01\x01src\x00\x02\x09dst\x00"
	message := "s\x00" + addresses + "\x03\x04\x02\x00\x00\x01\x01\x04\x09\x03xyz"
	return []struct {
		name string
		id   uint32
		body string
	}{
		{"bind_transmitter", 2, bind}, {"bind_transmitter_resp", 0x80000002, "smsc\x00\x02\x10\x00\x01\x34"},
		{"bind_receiver", 1, bind}, {"bind_receiver_resp", 0x80000001, "smsc\x00\x02\x10\x00\x01\x34"},
		{"bind_transceiver", 9, bind}, {"bind_transceiver_resp", 0x80000009, "smsc\x00\x02\x10\x00\x01\x34"},
		{"outbind", 0x0b, "id\x00pw\x00"},
		{"unbind", 6, ""}, {"unbind_resp", 0x80000006, ""}, {"generic_nack", 0x80000000, ""},
		{"submit_sm", 4, message + "\x13\x0c\x00\x00"}, {"submit_sm_resp", 0x80000004, "mid\x00"},
		{"submit_multi", 0x21, "s\x00\x01\x01src\x00\x02\x01\x02\x09dst\x00\x02list\x00\x03\x04\x02\x00\x00\x01\x01\x04\x09\x03xyz"},
		{"submit_multi_resp", 0x80000021, "mid\x00\x02\x01\x01a\x00\x00\x00\x00\x0b\x02\x09b\x00\x80\x00\x00\x01"},
		{"deliver_sm", 5, message + "\x04\x27\x00\x01\x02"}, {"deliver_sm_resp", 0x80000005, "\x00"},
		{"data_sm", 0x103, "s\x00" + addresses + "\x02\x01\xf5\x04\x24\x00\x03xyz\x02\x04\x00\x02\x12\x34"},
		{"data_sm_resp", 0x80000103, "mid\x00\x04\x25\x00\x01\x01\x04\x23\x00\x03\x03\x12\x34\x00\x1d\x00\x03ok\x00\x04\x20\x00\x01\x01"},
		{"query_sm", 3, "mid\x00\x01\x01src\x00"}, {"query_sm_resp", 0x80000003, "mid\x00261010123456000+\x00\x02\xe1"},
		{"cancel_sm", 8, "s\x00mid\x00" + addresses}, {"cancel_sm_resp", 0x80000008, ""},
		{"replace_sm", 7, "mid\x00\x01\x01src\x00261010123456000+\x00000001000000000R\x00\x01\x09\x03xyz"}, {"replace_sm_resp", 0x80000007, ""},
		{"enquire_link", 0x15, ""}, {"enquire_link_resp", 0x80000015, ""},
		{"alert_notification", 0x102, addresses + "\x04\x22\x00\x01\x01"},
	}
}

func TestSMPP34Section4WireLayouts(t *testing.T) {
	for _, fixture := range specificationBodies() {
		t.Run(fixture.name, func(t *testing.T) {
			wire := decodeTestFrame(data.CommandIDType(fixture.id), []byte(fixture.body))
			p, err := Parse(bytes.NewReader(wire))
			require.NoError(t, err)
			require.EqualValues(t, fixture.id, p.GetHeader().CommandID)
			checkSpecificationFields(t, p)
			encoded := NewBuffer(nil)
			p.Marshal(encoded)
			require.NoError(t, encoded.Err())
			require.Equal(t, wire, encoded.Bytes())
			if p.CanResponse() {
				response := p.GetResponse()
				require.EqualValues(t, fixture.id|0x80000000, response.GetHeader().CommandID)
				require.Equal(t, p.GetSequenceNumber(), response.GetSequenceNumber())
			} else {
				require.Nil(t, p.GetResponse())
			}
		})
	}
}

func checkSpecificationFields(t *testing.T, p PDU) {
	t.Helper()
	address := func(a Address, ton, npi byte, text string) {
		t.Helper()
		require.Equal(t, ton, a.Ton())
		require.Equal(t, npi, a.Npi())
		require.Equal(t, text, a.Address())
	}
	message := func(m ShortMessage) {
		t.Helper()
		require.EqualValues(t, 9, m.SmDefaultMsgID)
		require.Equal(t, []byte("xyz"), m.messageData)
	}
	controls := func(service string, esm, protocol, priority, registered, replace byte, schedule, validity string, m ShortMessage) {
		t.Helper()
		require.Equal(t, "s", service)
		require.EqualValues(t, 3, esm)
		require.EqualValues(t, 4, protocol)
		require.EqualValues(t, 2, priority)
		require.EqualValues(t, 1, registered)
		require.EqualValues(t, 1, replace)
		require.Empty(t, schedule)
		require.Empty(t, validity)
		require.EqualValues(t, 4, m.Encoding().DataCoding())
		message(m)
	}
	switch c := p.(type) {
	case *BindRequest:
		require.Equal(t, "id", c.SystemID)
		require.Equal(t, "pw", c.Password)
		require.Equal(t, "st", c.SystemType)
		require.EqualValues(t, 0x34, c.InterfaceVersion)
		require.Equal(t, AddressRange{Ton: 1, Npi: 9, AddressRange: "range"}, c.AddressRange)
	case *BindResp:
		require.Equal(t, "smsc", c.SystemID)
		field, ok := c.GetOptionalParam(TagScInterfaceVersion)
		require.True(t, ok)
		require.Equal(t, []byte{0x34}, field.Data)
	case *Outbind:
		require.Equal(t, "id", c.SystemID)
		require.Equal(t, "pw", c.Password)
	case *SubmitSM:
		address(c.SourceAddr, 1, 1, "src")
		address(c.DestAddr, 2, 9, "dst")
		controls(c.ServiceType, c.EsmClass, c.ProtocolID, c.PriorityFlag, c.RegisteredDelivery, c.ReplaceIfPresentFlag, c.ScheduleDeliveryTime, c.ValidityPeriod, c.Message)
	case *DeliverSM:
		address(c.SourceAddr, 1, 1, "src")
		address(c.DestAddr, 2, 9, "dst")
		controls(c.ServiceType, c.EsmClass, c.ProtocolID, c.PriorityFlag, c.RegisteredDelivery, c.ReplaceIfPresentFlag, c.ScheduleDeliveryTime, c.ValidityPeriod, c.Message)
	case *SubmitSMResp:
		require.Equal(t, "mid", c.MessageID)
	case *DeliverSMResp:
		require.Empty(t, c.MessageID)
	case *QuerySMResp:
		require.Equal(t, "mid", c.MessageID)
		require.Equal(t, "261010123456000+", c.FinalDate)
		require.EqualValues(t, 2, c.MessageState)
		require.EqualValues(t, 0xe1, c.ErrorCode)
	case *QuerySM:
		require.Equal(t, "mid", c.MessageID)
		address(c.SourceAddr, 1, 1, "src")
	case *ReplaceSM:
		require.Equal(t, "mid", c.MessageID)
		address(c.SourceAddr, 1, 1, "src")
		require.Equal(t, "261010123456000+", c.ScheduleDeliveryTime)
		require.Equal(t, "000001000000000R", c.ValidityPeriod)
		require.EqualValues(t, 1, c.RegisteredDelivery)
		message(c.Message)
	case *CancelSM:
		require.Equal(t, "s", c.ServiceType)
		require.Equal(t, "mid", c.MessageID)
		address(c.SourceAddr, 1, 1, "src")
		address(c.DestAddr, 2, 9, "dst")
	case *AlertNotification:
		address(c.SourceAddr, 1, 1, "src")
		address(c.EsmeAddr, 2, 9, "dst")
		field, ok := c.GetOptionalParam(TagMsAvailabilityStatus)
		require.True(t, ok)
		require.Equal(t, []byte{1}, field.Data)
	case *DataSM:
		require.Equal(t, "s", c.ServiceType)
		address(c.SourceAddr, 1, 1, "src")
		address(c.DestAddr, 2, 9, "dst")
		require.EqualValues(t, 2, c.EsmClass)
		require.EqualValues(t, 1, c.RegisteredDelivery)
		require.EqualValues(t, 0xf5, c.DataCoding)
		require.Equal(t, []Field{NewField(TagMessagePayload, []byte("xyz")), NewFieldUint16(TagUserMessageReference, 0x1234)}, c.OptionalParameters)
	case *DataSMResp:
		require.Equal(t, "mid", c.MessageID)
		require.Equal(t, []Field{NewFieldUint8(TagDeliveryFailureReason, 1), NewField(TagNetworkErrorCode, []byte{3, 0x12, 0x34}), NewField(TagAdditionalStatusInfoText, []byte("ok\x00")), NewFieldUint8(TagDpfResult, 1)}, c.OptionalParameters)
	case *SubmitMultiResp:
		require.Equal(t, "mid", c.MessageID)
		entries := c.UnsuccessSMEs.Get()
		require.Len(t, entries, 2)
		address(entries[0].Address, 1, 1, "a")
		address(entries[1].Address, 2, 9, "b")
		require.EqualValues(t, 0x0b, entries[0].ErrorStatusCode())
		require.Equal(t, uint32(0x80000001), uint32(entries[1].ErrorStatusCode()))
	case *SubmitMulti:
		address(c.SourceAddr, 1, 1, "src")
		controls(c.ServiceType, c.EsmClass, c.ProtocolID, c.PriorityFlag, c.RegisteredDelivery, c.ReplaceIfPresentFlag, c.ScheduleDeliveryTime, c.ValidityPeriod, c.Message)
		entries := c.DestAddrs.Get()
		require.Len(t, entries, 2)
		require.True(t, entries[0].IsAddress())
		address(entries[0].Address(), 2, 9, "dst")
		require.True(t, entries[1].IsDistributionList())
		require.Equal(t, "list", entries[1].DistributionList().Name())
		message(c.Message)
	}
}

func TestQuerySMRespFinalDateAndOctetBoundaries(t *testing.T) {
	for _, final := range []string{"", "261010123456000+", strings.Repeat("\xff", 512)} {
		for _, state := range []byte{0, 1, 8, 255} {
			for _, code := range []byte{0, 1, 128, 255} {
				body := append([]byte("mid\x00"+final+"\x00"), state, code)
				p, err := Parse(bytes.NewReader(decodeTestFrame(data.QUERY_SM_RESP, body)))
				require.NoError(t, err)
				c := p.(*QuerySMResp)
				require.Equal(t, final, c.FinalDate)
				require.Equal(t, state, c.MessageState)
				require.Equal(t, code, c.ErrorCode)
				require.Equal(t, len(final) > 16, Validate(p) != nil)
			}
		}
	}
}

func TestSubmitMultiRespUnsuccessfulCountBoundaries(t *testing.T) {
	for _, count := range []int{0, 1, 254, 255, 256} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			p := NewSubmitMultiResp().(*SubmitMultiResp)
			for i := range count {
				entry := NewUnsuccessSMEWithTonNpi(1, 9, data.CommandStatusType(uint32(i)|0x80000000))
				require.NoError(t, entry.SetAddress("dst"))
				p.UnsuccessSMEs.Add(entry)
			}
			encoded := NewBuffer(nil)
			p.Marshal(encoded)
			if count > 255 {
				require.ErrorIs(t, encoded.Err(), ErrEncode)
				require.Empty(t, encoded.Bytes())
				return
			}
			require.NoError(t, encoded.Err())
			require.Equal(t, byte(count), encoded.Bytes()[17])
			parsed, err := Parse(bytes.NewReader(encoded.Bytes()))
			require.NoError(t, err)
			entries := parsed.(*SubmitMultiResp).UnsuccessSMEs.Get()
			require.Len(t, entries, count)
			for i, entry := range entries {
				require.Equal(t, uint32(i)|0x80000000, uint32(entry.ErrorStatusCode()))
			}
			// A truncated four-octet error_status_code must not become a TLV.
			if count > 0 {
				truncated := append([]byte(nil), encoded.Bytes()[:encoded.Len()-1]...)
				binary.BigEndian.PutUint32(truncated, uint32(len(truncated)))
				_, err := Parse(bytes.NewReader(truncated))
				require.Error(t, err)
			}
		})
	}
}
