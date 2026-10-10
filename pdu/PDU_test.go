package pdu

import (
	"bytes"
	"encoding/binary"
	stderrors "errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/gomaja/go-smpp/data"
	"github.com/gomaja/go-smpp/errors"

	"github.com/stretchr/testify/require"
)

func decodeTestFrame(command data.CommandIDType, body []byte) []byte {
	b := NewBuffer(nil)
	h := Header{CommandLength: int32(16 + len(body)), CommandID: command, SequenceNumber: 0x1020304}
	h.Marshal(b)
	_, _ = b.Write(body)
	return b.Bytes()
}

func TestParseDecodeErrors(t *testing.T) {
	marshalBody := func(p PDU) []byte {
		b := NewBuffer(nil)
		p.Marshal(b)
		require.NoError(t, b.Err())
		return append([]byte(nil), b.Bytes()[16:]...)
	}
	baseBody := marshalBody(NewSubmitSM())
	messageBody := func(length byte, payload []byte, udhi bool) []byte {
		body := append([]byte(nil), baseBody...)
		body[16] = length
		if udhi {
			body[7] = data.SM_UDH_GSM
		}
		return append(body, payload...)
	}
	multiBody := marshalBody(NewSubmitMulti())
	invalidFlag := append([]byte(nil), multiBody[:4]...)
	invalidFlag = append(invalidFlag, 1, 0xff)
	invalidFlag = append(invalidFlag, multiBody[5:]...)
	dataBody := marshalBody(NewDataSM())
	cases := []struct {
		name    string
		command data.CommandIDType
		body    []byte
		status  data.CommandStatusType
		cause   error
	}{
		{"source_address_underflow", data.SUBMIT_SM, []byte{0}, data.ESME_RINVCMDLEN, io.EOF},
		{"message_truncated", data.SUBMIT_SM, messageBody(4, []byte{'A'}, false), data.ESME_RINVMSGLEN, ErrBufferNotEnoughByteToRead},
		{"message_255_truncated", data.SUBMIT_SM, messageBody(255, make([]byte, 254), false), data.ESME_RINVMSGLEN, ErrBufferNotEnoughByteToRead},
		{"message_udh", data.SUBMIT_SM, messageBody(3, []byte{5, 0, 3}, true), data.ESME_RINVMSGLEN, nil},
		{"destination_flag", data.SUBMIT_MULTI, invalidFlag, data.ESME_RINVDESTFLAG, nil},
		{"distribution_list", data.SUBMIT_MULTI, []byte{0, 0, 0, 0, 1, 2, 'A'}, data.ESME_RINVCMDLEN, io.EOF},
		{"tlv_header", data.DATA_SM, append(append([]byte(nil), dataBody...), 4, 0x24, 0), data.ESME_RINVOPTPARSTREAM, ErrBufferNotEnoughByteToRead},
		{"tlv_value", data.DATA_SM, append(append([]byte(nil), dataBody...), 4, 0x24, 0, 3, 1), data.ESME_RINVOPTPARSTREAM, ErrBufferNotEnoughByteToRead},
		{"mandatory_parameter", data.SUBMIT_SM, []byte{'A'}, data.ESME_RINVCMDLEN, io.EOF},
		{"unknown_command", data.CommandIDType(0x7777), []byte{1, 2, 3}, data.ESME_RINVCMDID, errors.ErrUnknownCommandID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wire := decodeTestFrame(tc.command, tc.body)
			binary.BigEndian.PutUint32(wire[8:], 0x58)
			following := decodeTestFrame(data.ENQUIRE_LINK, nil)
			r := bytes.NewReader(append(append([]byte(nil), wire...), following...))
			p, err := Parse(r)
			require.Nil(t, p, "a failed decode must not expose a partially valid PDU")
			var decoded *DecodeError
			require.ErrorAs(t, err, &decoded)
			var rawHeader [16]byte
			copy(rawHeader[:], wire)
			require.Equal(t, ParseHeader(rawHeader), decoded.Header)
			require.Equal(t, tc.status, decoded.Status)
			require.NotNil(t, decoded.Unwrap())
			require.Contains(t, decoded.Error(), decoded.Err.Error())
			if tc.cause != nil {
				require.ErrorIs(t, err, tc.cause)
			}
			require.False(t, stderrors.Is(err, ErrInvalidCommandLength))
			require.False(t, stderrors.Is(err, ErrIncompletePDU))
			next, err := Parse(r)
			require.NoError(t, err)
			require.Equal(t, data.ENQUIRE_LINK, next.GetHeader().CommandID)
			require.Zero(t, r.Len())
		})
	}
}

func TestParseFramingErrors(t *testing.T) {
	for _, tc := range []struct {
		name  string
		wire  []byte
		want  error
		cause error
	}{
		{"empty_header", nil, ErrIncompletePDU, io.EOF},
		{"short_header", []byte{0}, ErrIncompletePDU, io.ErrUnexpectedEOF},
		{"short_body", decodeTestFrame(data.SUBMIT_SM, []byte{0})[:16], ErrIncompletePDU, io.EOF},
		{"partial_body", decodeTestFrame(data.SUBMIT_SM, []byte{0, 1})[:17], ErrIncompletePDU, io.ErrUnexpectedEOF},
		{"small_length", fromHex("0000000f000000040000000000000001"), ErrInvalidCommandLength, errors.ErrInvalidPDU},
		{"large_length", fromHex("7fffffff000000040000000000000001"), ErrInvalidCommandLength, errors.ErrInvalidPDU},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Parse(bytes.NewReader(tc.wire))
			require.Nil(t, p)
			require.ErrorIs(t, err, tc.want)
			require.ErrorIs(t, err, tc.cause)
			var decoded *DecodeError
			require.False(t, stderrors.As(err, &decoded), "framing failures have no complete PDU")
		})
	}
}

func TestParseOverlongAddresses(t *testing.T) {
	multi := NewSubmitMulti().(*SubmitMulti)
	dest := NewDestinationAddress()
	dest.SetAddress(NewAddress())
	require.NoError(t, multi.DestAddrs.Add(dest))
	multiResp := NewSubmitMultiResp().(*SubmitMultiResp)
	multiResp.UnsuccessSMEs.Add(NewUnsuccessSME())
	for _, tc := range []struct {
		name   string
		p      PDU
		offset int
	}{
		{"submit_source", NewSubmitSM(), 3},
		{"submit_destination", NewSubmitSM(), 6},
		{"deliver_source", NewDeliverSM(), 3},
		{"deliver_destination", NewDeliverSM(), 6},
		{"data_source", NewDataSM(), 3},
		{"data_destination", NewDataSM(), 6},
		{"multi_source", multi, 3},
		{"multi_destination", multi, 8},
		{"replace_source", NewReplaceSM(), 3},
		{"query_source", NewQuerySM(), 3},
		{"cancel_source", NewCancelSM(), 4},
		{"cancel_destination", NewCancelSM(), 7},
		{"alert_source", NewAlertNotification(), 2},
		{"alert_esme", NewAlertNotification(), 5},
		{"unsuccessful_destination", multiResp, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := NewBuffer(nil)
			tc.p.Marshal(b)
			require.NoError(t, b.Err())
			body := b.Bytes()[16:]
			require.Zero(t, body[tc.offset])
			limit := 21
			if tc.p.GetHeader().CommandID == data.DATA_SM || tc.p.GetHeader().CommandID == data.ALERT_NOTIFICATION {
				limit = 65
			}
			for _, n := range []int{limit - 1, limit, 512} {
				invalid := append([]byte(nil), body[:tc.offset]...)
				invalid = append(invalid, []byte(strings.Repeat("A", n))...)
				invalid = append(invalid, body[tc.offset:]...)
				wire := decodeTestFrame(tc.p.GetHeader().CommandID, invalid)
				got, err := Parse(bytes.NewReader(wire))
				require.NoError(t, err)
				value := reflect.ValueOf(got).Elem()
				var address Address
				switch tc.name {
				case "multi_destination":
					address = got.(*SubmitMulti).DestAddrs.Get()[0].Address()
				case "unsuccessful_destination":
					address = got.(*SubmitMultiResp).UnsuccessSMEs.Get()[0].Address
				default:
					field := "SourceAddr"
					if strings.Contains(tc.name, "destination") {
						field = "DestAddr"
					}
					if tc.name == "alert_esme" {
						field = "EsmeAddr"
					}
					address = value.FieldByName(field).Interface().(Address)
				}
				require.Equal(t, strings.Repeat("A", n), address.Address())
				require.Equal(t, n+1 > limit, Validate(got) != nil)

				var header [16]byte
				copy(header[:], wire)
				require.Equal(t, ParseHeader(header), got.GetHeader())
				round := NewBuffer(nil)
				got.Marshal(round)
				require.NoError(t, round.Err())
				require.Equal(t, wire, round.Bytes())
			}

		})
	}
}

func TestParseCStringOctetsRoundTrip(t *testing.T) {
	for _, text := range []string{"é", "A\x80\xff", "\xfe\xc3"} {
		p := NewSubmitSM().(*SubmitSM)
		p.ServiceType = text
		var err error
		p.SourceAddr, err = NewAddressWithAddr(text)
		require.NoError(t, err)
		p.DestAddr = p.SourceAddr
		b := NewBuffer(nil)
		p.Marshal(b)
		require.NoError(t, b.Err())
		parsed, err := Parse(bytes.NewReader(b.Bytes()))
		require.NoError(t, err)
		require.Equal(t, text, parsed.(*SubmitSM).ServiceType)
		require.Equal(t, text, parsed.(*SubmitSM).SourceAddr.Address())
		require.Equal(t, text, parsed.(*SubmitSM).DestAddr.Address())
		roundTrip := NewBuffer(nil)
		parsed.Marshal(roundTrip)
		require.NoError(t, roundTrip.Err())
		require.Equal(t, b.Bytes(), roundTrip.Bytes())
	}
}

func TestOptionalParameterRegistrationAndLookup(t *testing.T) {
	var p base
	first := NewField(TagCallbackNum, []byte{1, 1, 1, '1'})
	second := NewField(TagCallbackNum, []byte{1, 1, 1, '2'})
	other := NewFieldUint8(TagCallbackNumPresInd, 0)
	p.AppendOptionalParam(first)
	p.AppendOptionalParam(other)
	p.AppendOptionalParam(second)
	replacement := NewField(TagCallbackNum, []byte{1, 1, 1, '3'})
	p.RegisterOptionalParam(replacement)
	require.Equal(t, []Field{replacement, other, second}, p.OptionalParameters)
	got, ok := p.GetOptionalParam(TagCallbackNum)
	require.True(t, ok)
	require.Equal(t, replacement, got)
	require.Equal(t, []Field{replacement, second}, p.GetOptionalParams(TagCallbackNum))
	_, ok = p.GetOptionalParam(Tag(0xffff))
	require.False(t, ok)
	require.Empty(t, p.GetOptionalParams(Tag(0xffff)))
	p.RegisterOptionalParam(NewField(TagAlertOnMessageDelivery, nil))
	require.Len(t, p.OptionalParameters, 4)
}

func TestOptionalParametersPreserveRepeatedWireOrder(t *testing.T) {
	b := NewBuffer(nil)
	NewDataSM().Marshal(b)
	fields := []Field{
		NewField(TagCallbackNum, []byte{1, 1, 1, '1'}),
		NewFieldUint8(TagCallbackNumPresInd, 0),
		NewField(TagCallbackNumAtag, []byte{1, 'A'}),
		NewField(TagCallbackNum, []byte{1, 1, 1, '2'}),
		NewFieldUint8(TagCallbackNumPresInd, 1),
		NewField(TagCallbackNumAtag, []byte{1, 'B'}),
	}
	for i := range fields {
		fields[i].Marshal(b)
	}
	wire := append([]byte(nil), b.Bytes()...)
	binary.BigEndian.PutUint32(wire, uint32(len(wire)))
	p, err := Parse(bytes.NewReader(wire))
	require.NoError(t, err)
	d := p.(*DataSM)
	require.Len(t, d.OptionalParameters, 6, "repeated callbacks and their associations")
	for i, f := range d.OptionalParameters {
		require.Equal(t, fields[i], f)
	}
	b = NewBuffer(nil)
	p.Marshal(b)
	require.Equal(t, wire, b.Bytes())
}

func TestOptionalParametersLargeAndEmptyRoundTrip(t *testing.T) {
	p := NewDataSM()
	p.RegisterOptionalParam(NewField(TagMessagePayload, make([]byte, 32768)))
	p.RegisterOptionalParam(NewField(TagAlertOnMessageDelivery, nil))
	b := NewBuffer(nil)
	p.Marshal(b)
	parsed, err := Parse(bytes.NewReader(b.Bytes()))
	require.NoError(t, err)
	payload, ok := parsed.(*DataSM).GetOptionalMessagePayload()
	require.True(t, ok)
	require.Len(t, payload, 32768)
	require.Len(t, parsed.(*DataSM).OptionalParameters, 2)
}

func TestPDURejectsInvalidEncodedBody(t *testing.T) {
	s := NewSubmitSM().(*SubmitSM)
	s.Message.messageData = make([]byte, 256)
	b := NewBuffer(nil)
	s.Marshal(b)
	require.Error(t, b.Err())
	require.Zero(t, b.Len())
	b.Reset()
	s.Message.messageData = nil
	s.RegisterOptionalParam(NewField(TagMessagePayload, make([]byte, 65536)))
	s.Marshal(b)
	require.Error(t, b.Err())
	require.Zero(t, b.Len())
}

func TestParsePDU(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		buf := NewBuffer(fromHex("00000010800000060000000000000001"))
		_, err := Parse(buf)
		require.Nil(t, err)
	})

	t.Run("submit_sm_resp with command_status != 0 ", func(t *testing.T) {
		buf := NewBuffer(fromHex("00000010800000040000005800000001"))
		_, err := Parse(buf)
		require.Nil(t, err)
	})

	t.Run("eof", func(t *testing.T) {
		buf := NewBuffer(nil)
		_, err := Parse(buf)
		require.NotNil(t, err)
	})

	t.Run("invalidCmdLength", func(t *testing.T) {
		buf := NewBuffer(fromHex("0000000f800000060000000000000001"))
		_, err := Parse(buf)
		require.ErrorIs(t, err, errors.ErrInvalidPDU)

		buf = NewBuffer(fromHex("3800000f800000060000000000000001"))
		_, err = Parse(buf)
		require.ErrorIs(t, err, errors.ErrInvalidPDU)
	})

	t.Run("invalidBody", func(t *testing.T) {
		buf := NewBuffer(fromHex("0000001e00000003000000000000000161776179001c1d416c69636572"))
		_, err := Parse(buf)
		require.NotNil(t, err)
	})

	t.Run("invalidPayload", func(t *testing.T) {
		buf := NewBuffer(fromHex("000000118000000400000000000000010012"))
		var b base
		require.NotNil(t, b.unmarshal(buf, func(buf *ByteBuffer) error {
			return nil
		}))

		buf = NewBuffer(fromHex("000000118000000400000000000000010012333333333333333333"))
		require.NotNil(t, b.unmarshal(buf, func(buf *ByteBuffer) error {
			_, _ = buf.ReadN(8)
			return nil
		}))
	})
}

func TestGetOptionalMessagePayload(t *testing.T) {
	t.Run("present", func(t *testing.T) {
		v := newBase()
		v.RegisterOptionalParam(Field{Tag: TagMessagePayload, Data: []byte("hello")})

		payload, found := v.GetOptionalMessagePayload()
		require.True(t, found)
		require.Equal(t, []byte("hello"), payload)
	})

	t.Run("absent", func(t *testing.T) {
		v := newBase()

		payload, found := v.GetOptionalMessagePayload()
		require.False(t, found)
		require.Nil(t, payload)
	})

	t.Run("presentButEmpty", func(t *testing.T) {
		v := newBase()
		v.RegisterOptionalParam(Field{Tag: TagMessagePayload, Data: []byte{}})

		payload, found := v.GetOptionalMessagePayload()
		require.False(t, found)
		require.Nil(t, payload)
	})

	t.Run("otherTagsIgnored", func(t *testing.T) {
		v := newBase()
		v.RegisterOptionalParam(Field{Tag: TagDestBearerType, Data: []byte{95}})

		payload, found := v.GetOptionalMessagePayload()
		require.False(t, found)
		require.Nil(t, payload)
	})

	t.Run("returnedSliceAliasesPDU", func(t *testing.T) {
		v := newBase()
		v.RegisterOptionalParam(Field{Tag: TagMessagePayload, Data: []byte("hello")})

		payload, found := v.GetOptionalMessagePayload()
		require.True(t, found)

		payload[0] = 'j'

		again, found := v.GetOptionalMessagePayload()
		require.True(t, found)
		require.Equal(t, []byte("jello"), again)
	})

	t.Run("fromUnmarshalledPDU", func(t *testing.T) {
		v := newBase()
		require.Nil(t, v.unmarshalOptionalParam(fromHex("042400056869746865")))

		payload, found := v.GetOptionalMessagePayload()
		require.True(t, found)
		require.Equal(t, []byte("hithe"), payload)
	})
}

func TestBaseRegisterOptionalParamOnZeroValue(t *testing.T) {
	var v base

	require.NotPanics(t, func() {
		v.RegisterOptionalParam(NewFieldUint16(TagUserMessageReference, 0x1234))
	})
	require.Equal(t, []byte{0x12, 0x34}, v.OptionalParameters[0].Data)
}

func TestAddressLimitsByPDU(t *testing.T) {
	for _, cmd := range []data.CommandIDType{data.SUBMIT_SM, data.DELIVER_SM, data.DATA_SM, data.ALERT_NOTIFICATION} {
		t.Run(fmt.Sprintf("%x", uint32(cmd)), func(t *testing.T) {
			for _, n := range []int{20, 21, 63, 64, 65} {
				a, err := NewAddressWithAddr(strings.Repeat("A", n))
				if n >= 65 {
					require.Error(t, err)
					continue
				}
				require.NoError(t, err)
				p, err := CreatePDUFromCmdID(cmd)
				require.NoError(t, err)
				switch p := p.(type) {
				case *SubmitSM:
					p.SourceAddr = a
					p.DestAddr = a
				case *DeliverSM:
					p.SourceAddr = a
					p.DestAddr = a
				case *DataSM:
					p.SourceAddr = a
					p.DestAddr = a
				case *AlertNotification:
					p.SourceAddr = a
					p.EsmeAddr = a
				}
				b := NewBuffer(nil)
				p.Marshal(b)

				require.NoError(t, b.Err())
				got, err := Parse(bytes.NewReader(b.Bytes()))
				require.NoError(t, err)
				round := NewBuffer(nil)
				got.Marshal(round)
				require.NoError(t, round.Err())
				require.Equal(t, b.Bytes(), round.Bytes())
			}
		})
	}
}
func TestErrorResponsesWithoutBody(t *testing.T) {
	for _, id := range []data.CommandIDType{data.BIND_RECEIVER_RESP, data.BIND_TRANSMITTER_RESP, data.BIND_TRANSCEIVER_RESP, data.SUBMIT_SM_RESP, data.SUBMIT_MULTI_RESP, data.DELIVER_SM_RESP, data.DATA_SM_RESP, data.QUERY_SM_RESP, data.CANCEL_SM_RESP, data.REPLACE_SM_RESP, data.UNBIND_RESP, data.ENQUIRE_LINK_RESP, data.GENERIC_NACK} {
		t.Run(fmt.Sprintf("%x", uint32(id)), func(t *testing.T) {
			wire := decodeTestFrame(id, nil)
			binary.BigEndian.PutUint32(wire[8:], uint32(data.ESME_RSYSERR))
			got, err := Parse(bytes.NewReader(wire))
			require.NoError(t, err)
			require.Equal(t, data.ESME_RSYSERR, got.GetHeader().CommandStatus)
			b := NewBuffer(nil)
			got.Marshal(b)
			require.NoError(t, b.Err())
			_, err = Parse(bytes.NewReader(b.Bytes()))
			require.NoError(t, err)
		})
	}
}
func TestShortMandatoryBodyStatus(t *testing.T) {
	for _, cmd := range []data.CommandIDType{data.SUBMIT_SM, data.SUBMIT_MULTI, data.DELIVER_SM, data.DATA_SM, data.ALERT_NOTIFICATION, data.QUERY_SM, data.CANCEL_SM, data.REPLACE_SM, data.OUTBIND, data.BIND_TRANSCEIVER, data.QUERY_SM_RESP, data.SUBMIT_MULTI_RESP, data.DATA_SM_RESP, data.DELIVER_SM_RESP} {
		p, err := CreatePDUFromCmdID(cmd)
		require.NoError(t, err)
		b := NewBuffer(nil)
		p.Marshal(b)
		require.NoError(t, b.Err())
		body := b.Bytes()[16:]
		for n := 0; n < len(body); n++ {
			if n == 0 && uint32(cmd)&0x80000000 != 0 {
				continue
			}
			wire := decodeTestFrame(cmd, body[:n])
			_, err := Parse(bytes.NewReader(wire))
			var de *DecodeError
			require.ErrorAs(t, err, &de, "%v body length %d", cmd, n)
			require.Equal(t, data.ESME_RINVCMDLEN, de.Status, "%v body length %d", cmd, n)
			require.Equal(t, cmd, de.Header.CommandID)
			require.EqualValues(t, 0x1020304, de.Header.SequenceNumber)
		}
	}
}
func TestMarshalMaximumPDULength(t *testing.T) {
	for _, n := range []int{65505, 65506, 65535} {
		p := NewDataSM()
		p.AppendOptionalParam(NewField(TagMessagePayload, make([]byte, n)))
		b := NewBuffer(nil)
		p.Marshal(b)
		if n > 65506 {
			require.ErrorIs(t, b.Err(), ErrEncode)
			require.Empty(t, b.Bytes())
			continue
		}
		require.NoError(t, b.Err())
		require.LessOrEqual(t, b.Len(), data.MAX_PDU_LEN)
		_, err := Parse(bytes.NewReader(b.Bytes()))
		require.NoError(t, err)
	}
}
func TestOptionalGettersUseFirstInstance(t *testing.T) {
	p := NewSubmitSM().(*SubmitSM)
	for _, fields := range [][]Field{
		{NewField(TagMessagePayload, []byte("A")), NewFieldUint16(TagSarMsgRefNum, 11), NewFieldUint8(TagSarTotalSegments, 2), NewFieldUint8(TagSarSegmentSeqnum, 1)},
		{NewField(TagMessagePayload, []byte("B")), NewFieldUint16(TagSarMsgRefNum, 22), NewFieldUint8(TagSarTotalSegments, 3), NewFieldUint8(TagSarSegmentSeqnum, 2)},
	} {
		for _, field := range fields {
			p.AppendOptionalParam(field)
		}
	}
	payload, ok := p.GetOptionalMessagePayload()
	require.True(t, ok)
	require.Equal(t, []byte("A"), payload)
	total, num, ref, ok := p.GetSARConcatInfo()
	require.True(t, ok)
	require.EqualValues(t, 2, total)
	require.EqualValues(t, 1, num)
	require.EqualValues(t, 11, ref)
	p.OptionalParameters[1].Data = nil
	_, _, _, ok = p.GetSARConcatInfo()
	require.False(t, ok, "a malformed first occurrence must not select a later one")
}

func TestCStringFieldLimits(t *testing.T) {
	// SMPP v3.4 §§4.1–4.12: maxima include the C-Octet terminator.
	// Nonempty deliver_sm timing/message IDs use the documented v5 tolerance.
	cases := []struct {
		cmd           data.CommandIDType
		field         string
		offset, limit int
	}{
		{data.BIND_RECEIVER, "SystemID", 0, 16}, {data.BIND_TRANSMITTER, "SystemID", 0, 16}, {data.BIND_TRANSCEIVER, "SystemID", 0, 16},
		{data.BIND_TRANSCEIVER, "Password", 1, 9}, {data.BIND_TRANSCEIVER, "SystemType", 2, 13}, {data.BIND_TRANSCEIVER, "AddressRange.AddressRange", 6, 41},
		{data.BIND_RECEIVER_RESP, "SystemID", 0, 16}, {data.BIND_TRANSMITTER_RESP, "SystemID", 0, 16}, {data.BIND_TRANSCEIVER_RESP, "SystemID", 0, 16},
		{data.OUTBIND, "SystemID", 0, 16}, {data.OUTBIND, "Password", 1, 9},
		{data.SUBMIT_SM, "ServiceType", 0, 6}, {data.DELIVER_SM, "ServiceType", 0, 6}, {data.DATA_SM, "ServiceType", 0, 6}, {data.SUBMIT_MULTI, "ServiceType", 0, 6}, {data.CANCEL_SM, "ServiceType", 0, 6},
		{data.SUBMIT_SM, "ScheduleDeliveryTime", 10, 17}, {data.SUBMIT_SM, "ValidityPeriod", 11, 17}, {data.DELIVER_SM, "ScheduleDeliveryTime", 10, 17}, {data.DELIVER_SM, "ValidityPeriod", 11, 17}, {data.SUBMIT_MULTI, "ScheduleDeliveryTime", 8, 17}, {data.SUBMIT_MULTI, "ValidityPeriod", 9, 17},
		{data.REPLACE_SM, "ScheduleDeliveryTime", 4, 17}, {data.REPLACE_SM, "ValidityPeriod", 5, 17}, {data.QUERY_SM_RESP, "FinalDate", 1, 17},
		{data.SUBMIT_SM_RESP, "MessageID", 0, 65}, {data.SUBMIT_MULTI_RESP, "MessageID", 0, 65}, {data.DATA_SM_RESP, "MessageID", 0, 65}, {data.DELIVER_SM_RESP, "MessageID", 0, 65}, {data.QUERY_SM_RESP, "MessageID", 0, 65},
		{data.QUERY_SM, "MessageID", 0, 65}, {data.REPLACE_SM, "MessageID", 0, 65}, {data.CANCEL_SM, "MessageID", 1, 65},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%x/%s", uint32(tc.cmd), tc.field), func(t *testing.T) {
			p, err := CreatePDUFromCmdID(tc.cmd)
			require.NoError(t, err)
			b := NewBuffer(nil)
			p.Marshal(b)
			require.NoError(t, b.Err())
			body := append([]byte(nil), b.Bytes()[16:]...)
			for _, n := range []int{tc.limit - 1, tc.limit, 512} {
				field := reflect.ValueOf(p).Elem()
				for _, name := range strings.Split(tc.field, ".") {
					field = field.FieldByName(name)
				}
				field.SetString(strings.Repeat("A", n))
				encoded := NewBuffer(nil)
				p.Marshal(encoded)
				wireBody := append([]byte(nil), body[:tc.offset]...)
				wireBody = append(wireBody, []byte(strings.Repeat("A", n))...)
				wireBody = append(wireBody, body[tc.offset:]...)
				wire := decodeTestFrame(tc.cmd, wireBody)
				got, err := Parse(bytes.NewReader(wire))
				require.NoError(t, encoded.Err())
				require.NoError(t, err)
				gotField := reflect.ValueOf(got).Elem()
				for _, name := range strings.Split(tc.field, ".") {
					gotField = gotField.FieldByName(name)
				}
				require.Equal(t, strings.Repeat("A", n), gotField.String())
				maximum := tc.limit
				if tc.cmd == data.DELIVER_SM && (tc.field == "ScheduleDeliveryTime" || tc.field == "ValidityPeriod") || tc.cmd == data.DELIVER_SM_RESP {
					maximum = 1
				}
				validation := Validate(got)
				require.Equal(t, n >= maximum, validation != nil)
				if validation != nil {
					var violation *ValidationError
					require.ErrorAs(t, validation, &violation)
					want := map[string]data.CommandStatusType{
						"SystemID": data.ESME_RINVSYSID, "Password": data.ESME_RINVPASWD,
						"ServiceType": data.ESME_RINVSERTYP, "MessageID": data.ESME_RINVMSGID,
						"ScheduleDeliveryTime": data.ESME_RINVSCHED, "ValidityPeriod": data.ESME_RINVEXPIRY,
						"SystemType": data.ESME_RINVPARLEN, "AddressRange.AddressRange": data.ESME_RINVPARLEN,
						"FinalDate": data.ESME_RINVPARLEN,
					}
					require.Equal(t, want[tc.field], violation.Status)
				}
				round := NewBuffer(nil)
				got.Marshal(round)
				require.NoError(t, round.Err())
				require.Equal(t, wire, round.Bytes())

			}
		})
	}
}

func TestEncodeErrorIdentity(t *testing.T) {
	p := NewSubmitSM()
	err := &EncodeError{PDU: p, Err: io.ErrUnexpectedEOF}
	require.ErrorIs(t, err, ErrEncode)
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	require.Same(t, p, err.PDU)
	require.Same(t, io.ErrUnexpectedEOF, err.Unwrap())
	require.Contains(t, err.Error(), "SubmitSM")
}

func TestResponseTrailingCStringTolerance(t *testing.T) {
	for _, cmd := range []data.CommandIDType{data.BIND_RECEIVER_RESP, data.BIND_TRANSMITTER_RESP, data.BIND_TRANSCEIVER_RESP, data.SUBMIT_SM_RESP} {
		for _, text := range []string{"", "not-terminated", "A\x80\xff"} {
			wire := decodeTestFrame(cmd, []byte(text))
			next := decodeTestFrame(data.ENQUIRE_LINK, nil)
			reader := bytes.NewReader(append(append([]byte(nil), wire...), next...))
			got, err := Parse(reader)
			require.NoError(t, err)
			switch p := got.(type) {
			case *BindResp:
				require.Equal(t, text, p.SystemID)
			case *SubmitSMResp:
				require.Equal(t, text, p.MessageID)
			}
			require.Empty(t, reflect.ValueOf(got).Elem().FieldByName("OptionalParameters").Interface())
			following, err := Parse(reader)
			require.NoError(t, err)
			require.Equal(t, data.ENQUIRE_LINK, following.GetHeader().CommandID)
			b := NewBuffer(nil)
			got.Marshal(b)
			require.NoError(t, b.Err())
			parsed, err := Parse(bytes.NewReader(b.Bytes()))
			require.NoError(t, err)
			require.Equal(t, got, parsed)
		}
		// A terminated field leaves the remaining bytes to strict TLV parsing.
		_, err := Parse(bytes.NewReader(decodeTestFrame(cmd, []byte{'A', 0, 4, 0})))
		var de *DecodeError
		require.ErrorAs(t, err, &de)
		require.Equal(t, data.ESME_RINVOPTPARSTREAM, de.Status)
	}
}

func TestParseLongCStringBoundedByFrame(t *testing.T) {
	base := NewBuffer(nil)
	NewSubmitSM().Marshal(base)
	body := base.Bytes()[16:]
	text := string(bytes.Repeat([]byte{0xff}, data.MAX_PDU_LEN-len(body)-16))
	peer := append([]byte(text), body...)
	wire := decodeTestFrame(data.SUBMIT_SM, peer)
	require.Len(t, wire, data.MAX_PDU_LEN)
	next := decodeTestFrame(data.ENQUIRE_LINK, nil)
	reader := bytes.NewReader(append(append([]byte(nil), wire...), next...))
	parsed, err := Parse(reader)
	require.NoError(t, err)
	require.Equal(t, text, parsed.(*SubmitSM).ServiceType)
	following, err := Parse(reader)
	require.NoError(t, err)
	require.Equal(t, data.ENQUIRE_LINK, following.GetHeader().CommandID)
	reader = bytes.NewReader(append(decodeTestFrame(data.SUBMIT_SM, []byte(text)), next...))
	_, err = Parse(reader)
	var de *DecodeError
	require.ErrorAs(t, err, &de)
	require.Equal(t, data.ESME_RINVCMDLEN, de.Status)
	require.ErrorIs(t, err, io.EOF)
	following, err = Parse(reader)
	require.NoError(t, err)
	require.Equal(t, data.ENQUIRE_LINK, following.GetHeader().CommandID)
}

func TestResponseTrailingCStringAtFrameLimit(t *testing.T) {
	text := string(bytes.Repeat([]byte{'I'}, data.MAX_PDU_LEN-16))
	wire := decodeTestFrame(data.BIND_TRANSCEIVER_RESP, []byte(text))
	parsed, err := Parse(bytes.NewReader(wire))
	require.NoError(t, err)
	require.Equal(t, text, parsed.(*BindResp).SystemID)
	b := NewBuffer(nil)
	parsed.Marshal(b)
	require.ErrorIs(t, b.Err(), ErrEncode)
	require.Empty(t, b.Bytes())
}

func TestClearOptionalParametersDetachesHeldSlice(t *testing.T) {
	p := NewSubmitSM()
	original := NewField(TagUserMessageReference, []byte{0x12, 0x34})
	p.AppendOptionalParam(original)
	held := p.GetOptionalParameters()
	p.ClearOptionalParameters()
	require.Empty(t, p.GetOptionalParameters())
	p.AppendOptionalParam(NewField(TagMessagePayload, []byte("new")))
	require.Equal(t, original, held[0])
}
func TestAllResponsesAcceptMissingBody(t *testing.T) {
	for _, id := range []data.CommandIDType{data.BIND_RECEIVER_RESP, data.BIND_TRANSMITTER_RESP, data.BIND_TRANSCEIVER_RESP, data.SUBMIT_SM_RESP, data.SUBMIT_MULTI_RESP, data.DELIVER_SM_RESP, data.DATA_SM_RESP, data.QUERY_SM_RESP, data.CANCEL_SM_RESP, data.REPLACE_SM_RESP, data.UNBIND_RESP, data.ENQUIRE_LINK_RESP, data.GENERIC_NACK} {
		for _, status := range []data.CommandStatusType{data.ESME_ROK, data.ESME_RSYSERR} {
			wire := decodeTestFrame(id, nil)
			binary.BigEndian.PutUint32(wire[8:], uint32(status))
			p, err := Parse(bytes.NewReader(wire))
			require.NoError(t, err, "cmd %x status %x", uint32(id), uint32(status))
			require.Equal(t, status, p.GetHeader().CommandStatus)
			b := NewBuffer(nil)
			p.Marshal(b)
			require.NoError(t, b.Err())
			q, err := Parse(b)
			require.NoError(t, err)
			require.Equal(t, p, q)
		}
	}
}
