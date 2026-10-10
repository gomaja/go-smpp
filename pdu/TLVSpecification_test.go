package pdu

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"testing"

	"github.com/gomaja/go-smpp/data"
	"github.com/stretchr/testify/require"
)

// The numeric tags and value widths come from SMPP v3.4 Issue 1.2
// Table 5-7 and §§5.3.2.1–44. Field remains a lossless raw codec;
// these widths guide typed callers, without restricting peer-provided TLVs.
func TestSMPP34OptionalParameterTagsAndValueWidths(t *testing.T) {
	// Link the independent standard rows to every exported normative width.
	// message_payload's legacy 1/1500 limits are policy suggestions; §5.3.2.32
	// permits 0..65535 on the wire. Most fixed-width tags have no MIN/MAX aliases.
	widths := map[Tag]struct{ minimum, maximum int }{
		TagQosTimeToLive:            {data.OPT_PAR_QOS_TIME_TO_LIVE_MIN, data.OPT_PAR_QOS_TIME_TO_LIVE_MAX},
		TagAdditionalStatusInfoText: {data.OPT_PAR_ADD_STAT_INFO_MIN, data.OPT_PAR_ADD_STAT_INFO_MAX},
		TagReceiptedMessageID:       {data.OPT_PAR_RECP_MSG_ID_MIN, data.OPT_PAR_RECP_MSG_ID_MAX},
		TagSourceSubaddress:         {data.OPT_PAR_SRC_SUBADDR_MIN, data.OPT_PAR_SRC_SUBADDR_MAX},
		TagDestSubaddress:           {data.OPT_PAR_DEST_SUBADDR_MIN, data.OPT_PAR_DEST_SUBADDR_MAX},
		TagNetworkErrorCode:         {data.OPT_PAR_NW_ERR_CODE_MIN, data.OPT_PAR_NW_ERR_CODE_MAX},
		TagCallbackNum:              {data.OPT_PAR_CALLBACK_NUM_MIN, data.OPT_PAR_CALLBACK_NUM_MAX},
		TagCallbackNumAtag:          {data.OPT_PAR_CALLBACK_NUM_ATAG_MIN, data.OPT_PAR_CALLBACK_NUM_ATAG_MAX},
	}

	for _, row := range []struct {
		section string
		tag     Tag
		wireTag uint16
		minimum int
		maximum int
		integer bool
	}{
		{"1", TagDestAddrSubunit, 0x0005, 1, 1, true},
		{"2", TagSourceAddrSubunit, 0x000d, 1, 1, true},
		{"3", TagDestNetworkType, 0x0006, 1, 1, true},
		{"4", TagSourceNetworkType, 0x000e, 1, 1, true},
		{"5", TagDestBearerType, 0x0007, 1, 1, true},
		{"6", TagSourceBearerType, 0x000f, 1, 1, true},
		{"7", TagDestTelematicsID, 0x0008, 2, 2, true},
		{"8", TagSourceTelematicsID, 0x0010, 1, 1, true},
		{"9", TagQosTimeToLive, 0x0017, 4, 4, true},
		{"10", TagPayloadType, 0x0019, 1, 1, true},
		{"11", TagAdditionalStatusInfoText, 0x001d, 1, 256, false},
		{"12", TagReceiptedMessageID, 0x001e, 1, 65, false},
		{"13", TagMsMsgWaitFacilities, 0x0030, 1, 1, false},
		{"14", TagPrivacyIndicator, 0x0201, 1, 1, true},
		{"15", TagSourceSubaddress, 0x0202, 2, 23, false},
		{"16", TagDestSubaddress, 0x0203, 2, 23, false},
		{"17", TagUserMessageReference, 0x0204, 2, 2, true},
		{"18", TagUserResponseCode, 0x0205, 1, 1, true},
		{"19", TagLanguageIndicator, 0x020d, 1, 1, true},
		{"20", TagSourcePort, 0x020a, 2, 2, true},
		{"21", TagDestinationPort, 0x020b, 2, 2, true},
		{"22", TagSarMsgRefNum, 0x020c, 2, 2, true},
		{"23", TagSarTotalSegments, 0x020e, 1, 1, true},
		{"24", TagSarSegmentSeqnum, 0x020f, 1, 1, true},
		{"25", TagScInterfaceVersion, 0x0210, 1, 1, true},
		{"26", TagDisplayTime, 0x1201, 1, 1, true},
		{"27", TagMsValidity, 0x1204, 1, 1, true},
		{"28", TagDpfResult, 0x0420, 1, 1, true},
		{"29", TagSetDpf, 0x0421, 1, 1, true},
		{"30", TagMsAvailabilityStatus, 0x0422, 1, 1, true},
		{"31", TagNetworkErrorCode, 0x0423, 3, 3, false},
		{"32", TagMessagePayload, 0x0424, 0, 65535, false},
		{"33", TagDeliveryFailureReason, 0x0425, 1, 1, true},
		{"34", TagMoreMessagesToSend, 0x0426, 1, 1, true},
		{"35", TagMessageStateOption, 0x0427, 1, 1, true},
		{"36", TagCallbackNum, 0x0381, 4, 19, false},
		{"37", TagCallbackNumPresInd, 0x0302, 1, 1, false},
		{"38", TagCallbackNumAtag, 0x0303, 1, 65, false},
		{"39", TagNumberOfMessages, 0x0304, 1, 1, true},
		{"40", TagSmsSignal, 0x1203, 2, 2, true},
		{"41", TagAlertOnMessageDelivery, 0x130c, 0, 0, false},
		{"42", TagItsReplyType, 0x1380, 1, 1, true},
		{"43", TagItsSessionInfo, 0x1383, 2, 2, false},
		{"44", TagUssdServiceOp, 0x0501, 1, 1, false},
	} {
		t.Run("5.3.2."+row.section, func(t *testing.T) {
			require.Equal(t, row.wireTag, uint16(row.tag))
			if limits, exists := widths[row.tag]; exists {
				require.Equal(t, row.minimum, limits.minimum, "exported minimum differs from standard")
				require.Equal(t, row.maximum, limits.maximum, "exported maximum differs from standard")
			}
			for _, length := range []int{row.minimum, row.maximum} {
				value := bytes.Repeat([]byte{0xa5}, length)
				field := NewField(row.tag, value)
				encoded := NewBuffer(nil)
				field.Marshal(encoded)
				require.NoError(t, encoded.Err())
				require.Equal(t, row.wireTag, binary.BigEndian.Uint16(encoded.Bytes()))
				require.EqualValues(t, length, binary.BigEndian.Uint16(encoded.Bytes()[2:]))
				var decoded Field
				require.NoError(t, decoded.Unmarshal(encoded))
				require.Equal(t, field, decoded)
				require.Empty(t, encoded.Bytes())
			}
			if row.integer {
				switch row.minimum {
				case 1:
					field := NewFieldUint8(row.tag, 0xa5)
					v, err := field.Uint8()
					require.NoError(t, err)
					require.Equal(t, uint8(0xa5), v)
				case 2:
					field := NewFieldUint16(row.tag, 0x1234)
					require.Equal(t, []byte{0x12, 0x34}, field.Data)
					v, err := field.Uint16()
					require.NoError(t, err)
					require.Equal(t, uint16(0x1234), v)
				case 4:
					field := NewFieldUint32(row.tag, 0x12345678)
					require.Equal(t, []byte{0x12, 0x34, 0x56, 0x78}, field.Data)
					v, err := field.Uint32()
					require.NoError(t, err)
					require.Equal(t, uint32(0x12345678), v)
				default:
					t.Fatalf("integer width %d has no typed helper", row.minimum)
				}
			}
		})
	}
}

func TestTypedOptionalIntegerRejectsEveryOtherWidth(t *testing.T) {
	for _, width := range []int{1, 2, 4} {
		for length := range 6 {
			t.Run(fmt.Sprintf("%d/%d", width, length), func(t *testing.T) {
				field := NewField(Tag(0x7777), make([]byte, length))
				var err error
				switch width {
				case 1:
					_, err = field.Uint8()
				case 2:
					_, err = field.Uint16()
				case 4:
					_, err = field.Uint32()
				}
				require.Equal(t, length != width, err != nil)
			})
		}
	}
}
