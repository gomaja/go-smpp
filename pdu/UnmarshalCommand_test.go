package pdu

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"reflect"
	"testing"

	"github.com/gomaja/go-smpp/data"
	smpperrors "github.com/gomaja/go-smpp/errors"
	"github.com/stretchr/testify/require"
)

func TestBindRequestReuseDerivesBindingTypeFromCommandID(t *testing.T) {
	cases := []struct {
		id       data.CommandIDType
		binding  BindingType
		response data.CommandIDType
	}{
		{data.BIND_RECEIVER, Receiver, data.BIND_RECEIVER_RESP},
		{data.BIND_TRANSMITTER, Transmitter, data.BIND_TRANSMITTER_RESP},
		{data.BIND_TRANSCEIVER, Transceiver, data.BIND_TRANSCEIVER_RESP},
	}
	for _, previous := range cases {
		for _, next := range cases {
			t.Run(fmt.Sprintf("%x/%x", uint32(previous.id), uint32(next.id)), func(t *testing.T) {
				target := NewBindRequest(previous.binding)
				source := NewBindRequest(next.binding)
				wire := NewBuffer(nil)
				source.Marshal(wire)
				require.NoError(t, target.Unmarshal(NewBuffer(wire.Bytes())))
				require.Equal(t, next.binding, target.BindingType)
				require.Equal(t, next.response, target.GetResponse().GetHeader().CommandID)
				require.Equal(t, source.SequenceNumber, target.GetResponse().GetSequenceNumber())
			})
		}
	}
}

func TestUnmarshalRejectsCommandIDForDifferentConcreteType(t *testing.T) {
	for targetID := range pduMap {
		for wireID := range pduMap {
			target, err := CreatePDUFromCmdID(targetID)
			require.NoError(t, err)
			source, err := CreatePDUFromCmdID(wireID)
			require.NoError(t, err)
			// The three bind variants share their respective concrete Go types.
			sameType := reflect.TypeOf(target) == reflect.TypeOf(source)
			t.Run(fmt.Sprintf("%x/%x", uint32(targetID), uint32(wireID)), func(t *testing.T) {
				wire := NewBuffer(nil)
				source.Marshal(wire)
				require.NoError(t, wire.Err())
				following := decodeTestFrame(data.ENQUIRE_LINK, nil)
				input := NewBuffer(append(append([]byte(nil), wire.Bytes()...), following...))
				err := target.Unmarshal(input)
				if sameType {
					require.NoError(t, err)
					require.Equal(t, source.GetHeader(), target.GetHeader())
					require.Equal(t, wire.Bytes(), marshalCommandTestPDU(t, target))
				} else {
					var decoded *DecodeError
					require.ErrorAs(t, err, &decoded)
					require.Equal(t, data.ESME_RINVCMDID, decoded.Status)
					require.Equal(t, source.GetHeader(), decoded.Header)
					require.ErrorIs(t, err, smpperrors.ErrUnknownCommandID)
				}
				require.Equal(t, following, input.Bytes(), "command rejection crossed the frame boundary")
				fresh, parseErr := Parse(bytes.NewReader(wire.Bytes()))
				require.NoError(t, parseErr, "factory-selected Parse must stay compatible")
				require.IsType(t, source, fresh)
				require.Equal(t, wire.Bytes(), marshalCommandTestPDU(t, fresh))
			})
		}
	}
}

func TestUnmarshalZeroValueAcceptsItsOwnCommandType(t *testing.T) {
	for id := range pduMap {
		source, err := CreatePDUFromCmdID(id)
		require.NoError(t, err)
		target := reflect.New(reflect.TypeOf(source).Elem()).Interface().(PDU)
		wire := NewBuffer(nil)
		source.Marshal(wire)
		require.NoError(t, target.Unmarshal(NewBuffer(wire.Bytes())))
		require.Equal(t, source.GetHeader(), target.GetHeader())
		require.Equal(t, wire.Bytes(), marshalCommandTestPDU(t, target))
	}
}

func TestUnmarshalRejectsTruncatedDeclaredBody(t *testing.T) {
	for id := range pduMap {
		p, err := CreatePDUFromCmdID(id)
		require.NoError(t, err)
		wire := marshalCommandTestPDU(t, p)
		binary.BigEndian.PutUint32(wire, uint32(len(wire)+1))
		require.ErrorIs(t, p.Unmarshal(NewBuffer(wire)), ErrBufferNotEnoughByteToRead)
	}
}

func TestUnmarshalRejectsSingleFrameCommandIDAndConsumesIt(t *testing.T) {
	for _, wire := range [][]byte{
		decodeTestFrame(data.ENQUIRE_LINK, nil),
		decodeTestFrame(data.OUTBIND, []byte("system\x00password\x00")),
	} {
		input := NewBuffer(wire)
		var decoded *DecodeError
		err := NewSubmitSMResp().Unmarshal(input)
		require.ErrorAs(t, err, &decoded)
		require.Equal(t, data.ESME_RINVCMDID, decoded.Status)
		require.Equal(t, ParseHeader([16]byte(wire[:16])), decoded.Header)
		require.Empty(t, input.Bytes())
	}
}

func TestUnmarshalConsumesEntireSingleFrame(t *testing.T) {
	wire := benchmarkSubmitSM()
	target := NewSubmitSM()
	input := NewBuffer(wire)
	require.NoError(t, target.Unmarshal(input))
	require.Empty(t, input.Bytes(), "TLVs must be consumed on the single-frame fast path")
	for _, malformed := range []struct {
		id   data.CommandIDType
		body []byte
	}{
		{data.OUTBIND, []byte("unterminated")},
		// A three-octet error_status_code fails its four-octet read while
		// leaving bytes to drain after the mandatory-field error.
		{data.SUBMIT_MULTI_RESP, []byte("mid\x00\x01\x01\x01a\x00\x01\x02\x03")},
	} {
		input = NewBuffer(decodeTestFrame(malformed.id, malformed.body))
		target, err := CreatePDUFromCmdID(malformed.id)
		require.NoError(t, err)
		require.Error(t, target.Unmarshal(input))
		require.Empty(t, input.Bytes(), "a complete malformed frame must be consumed")
	}
}

func TestUnmarshalReusesSingleFrameAndBoundsWithoutCopying(t *testing.T) {
	for _, following := range [][]byte{nil, decodeTestFrame(data.ENQUIRE_LINK, nil)} {
		wire := decodeTestFrame(data.OUTBIND, []byte{1, 2, 3})
		input := NewBuffer(append(wire, following...))
		body := input.Bytes()[data.PDU_HEADER_SIZE:]
		var decoded base
		require.NoError(t, decoded.unmarshal(input, func(frame *ByteBuffer) error {
			require.Equal(t, 3, frame.Len())
			require.True(t, &body[0] == &frame.Bytes()[0], "frame isolation copied the body")
			if len(following) == 0 {
				require.Same(t, input, frame, "an already isolated frame allocated another buffer")
			}
			frame.Next(frame.Len())
			return nil
		}))
		require.True(t, bytes.Equal(following, input.Bytes()))
	}
}

func TestUnmarshalRetainedFieldsDoNotAliasInput(t *testing.T) {
	for _, tc := range benchmarkParseFrames() {
		t.Run(tc.name, func(t *testing.T) {
			wire := append([]byte(nil), tc.wire...)
			target, err := CreatePDUFromCmdID(ParseHeader([16]byte(wire[:16])).CommandID)
			require.NoError(t, err)
			require.NoError(t, target.Unmarshal(NewBuffer(wire)))
			for i := range wire {
				wire[i] = 0xff
			}
			out := NewBuffer(nil)
			target.Marshal(out)
			require.NoError(t, out.Err())
			require.Equal(t, tc.wire, out.Bytes(), "decoded fields retained the source buffer")
		})
	}
}

func marshalCommandTestPDU(t *testing.T, p PDU) []byte {
	t.Helper()
	wire := NewBuffer(nil)
	p.Marshal(wire)
	require.NoError(t, wire.Err())
	return wire.Bytes()
}
