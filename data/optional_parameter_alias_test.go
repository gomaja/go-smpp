package data_test

import (
	"testing"

	"github.com/gomaja/go-smpp/data"
	"github.com/gomaja/go-smpp/pdu"
)

// SMPP v3.4 Table 5-7 and §§5.3.2.1–44: both public tag surfaces
// must name the same wire parameter. The independent PDU table tests its code.
func TestSMPP34OptionalParameterAliasesMatchPDUTags(t *testing.T) {
	rows := []struct {
		name  string
		alias uint16
		tag   pdu.Tag
	}{
		{"OPT_PAR_DST_ADDR_SUBUNIT", data.OPT_PAR_DST_ADDR_SUBUNIT, pdu.TagDestAddrSubunit},
		{"OPT_PAR_SRC_ADDR_SUBUNIT", data.OPT_PAR_SRC_ADDR_SUBUNIT, pdu.TagSourceAddrSubunit},
		{"OPT_PAR_DST_NW_TYPE", data.OPT_PAR_DST_NW_TYPE, pdu.TagDestNetworkType},
		{"OPT_PAR_SRC_NW_TYPE", data.OPT_PAR_SRC_NW_TYPE, pdu.TagSourceNetworkType},
		{"OPT_PAR_DST_BEAR_TYPE", data.OPT_PAR_DST_BEAR_TYPE, pdu.TagDestBearerType},
		{"OPT_PAR_SRC_BEAR_TYPE", data.OPT_PAR_SRC_BEAR_TYPE, pdu.TagSourceBearerType},
		{"OPT_PAR_DST_TELE_ID", data.OPT_PAR_DST_TELE_ID, pdu.TagDestTelematicsID},
		{"OPT_PAR_SRC_TELE_ID", data.OPT_PAR_SRC_TELE_ID, pdu.TagSourceTelematicsID},
		{"OPT_PAR_QOS_TIME_TO_LIVE", data.OPT_PAR_QOS_TIME_TO_LIVE, pdu.TagQosTimeToLive},
		{"OPT_PAR_PAYLOAD_TYPE", data.OPT_PAR_PAYLOAD_TYPE, pdu.TagPayloadType},
		{"OPT_PAR_ADD_STAT_INFO", data.OPT_PAR_ADD_STAT_INFO, pdu.TagAdditionalStatusInfoText},
		{"OPT_PAR_RECP_MSG_ID", data.OPT_PAR_RECP_MSG_ID, pdu.TagReceiptedMessageID},
		{"OPT_PAR_MSG_WAIT", data.OPT_PAR_MSG_WAIT, pdu.TagMsMsgWaitFacilities},
		{"OPT_PAR_PRIV_IND", data.OPT_PAR_PRIV_IND, pdu.TagPrivacyIndicator},
		{"OPT_PAR_SRC_SUBADDR", data.OPT_PAR_SRC_SUBADDR, pdu.TagSourceSubaddress},
		{"OPT_PAR_DEST_SUBADDR", data.OPT_PAR_DEST_SUBADDR, pdu.TagDestSubaddress},
		{"OPT_PAR_USER_MSG_REF", data.OPT_PAR_USER_MSG_REF, pdu.TagUserMessageReference},
		{"OPT_PAR_USER_RESP_CODE", data.OPT_PAR_USER_RESP_CODE, pdu.TagUserResponseCode},
		{"OPT_PAR_LANG_IND", data.OPT_PAR_LANG_IND, pdu.TagLanguageIndicator},
		{"OPT_PAR_SRC_PORT", data.OPT_PAR_SRC_PORT, pdu.TagSourcePort},
		{"OPT_PAR_DST_PORT", data.OPT_PAR_DST_PORT, pdu.TagDestinationPort},
		{"OPT_PAR_SAR_MSG_REF_NUM", data.OPT_PAR_SAR_MSG_REF_NUM, pdu.TagSarMsgRefNum},
		{"OPT_PAR_SAR_TOT_SEG", data.OPT_PAR_SAR_TOT_SEG, pdu.TagSarTotalSegments},
		{"OPT_PAR_SAR_SEG_SNUM", data.OPT_PAR_SAR_SEG_SNUM, pdu.TagSarSegmentSeqnum},
		{"OPT_PAR_SC_IF_VER", data.OPT_PAR_SC_IF_VER, pdu.TagScInterfaceVersion},
		{"OPT_PAR_DISPLAY_TIME", data.OPT_PAR_DISPLAY_TIME, pdu.TagDisplayTime},
		{"OPT_PAR_MS_VALIDITY", data.OPT_PAR_MS_VALIDITY, pdu.TagMsValidity},
		{"OPT_PAR_DPF_RES", data.OPT_PAR_DPF_RES, pdu.TagDpfResult},
		{"OPT_PAR_SET_DPF", data.OPT_PAR_SET_DPF, pdu.TagSetDpf},
		{"OPT_PAR_MS_AVAIL_STAT", data.OPT_PAR_MS_AVAIL_STAT, pdu.TagMsAvailabilityStatus},
		{"OPT_PAR_NW_ERR_CODE", data.OPT_PAR_NW_ERR_CODE, pdu.TagNetworkErrorCode},
		{"OPT_PAR_MSG_PAYLOAD", data.OPT_PAR_MSG_PAYLOAD, pdu.TagMessagePayload},
		{"OPT_PAR_DEL_FAIL_RSN", data.OPT_PAR_DEL_FAIL_RSN, pdu.TagDeliveryFailureReason},
		{"OPT_PAR_MORE_MSGS", data.OPT_PAR_MORE_MSGS, pdu.TagMoreMessagesToSend},
		{"OPT_PAR_MSG_STATE", data.OPT_PAR_MSG_STATE, pdu.TagMessageStateOption},
		{"OPT_PAR_CALLBACK_NUM", data.OPT_PAR_CALLBACK_NUM, pdu.TagCallbackNum},
		{"OPT_PAR_CALLBACK_NUM_PRES_IND", data.OPT_PAR_CALLBACK_NUM_PRES_IND, pdu.TagCallbackNumPresInd},
		{"OPT_PAR_CALLBACK_NUM_ATAG", data.OPT_PAR_CALLBACK_NUM_ATAG, pdu.TagCallbackNumAtag},
		{"OPT_PAR_NUM_MSGS", data.OPT_PAR_NUM_MSGS, pdu.TagNumberOfMessages},
		{"OPT_PAR_SMS_SIGNAL", data.OPT_PAR_SMS_SIGNAL, pdu.TagSmsSignal},
		{"OPT_PAR_ALERT_ON_MSG_DELIVERY", data.OPT_PAR_ALERT_ON_MSG_DELIVERY, pdu.TagAlertOnMessageDelivery},
		{"OPT_PAR_ITS_REPLY_TYPE", data.OPT_PAR_ITS_REPLY_TYPE, pdu.TagItsReplyType},
		{"OPT_PAR_ITS_SESSION_INFO", data.OPT_PAR_ITS_SESSION_INFO, pdu.TagItsSessionInfo},
		{"OPT_PAR_USSD_SER_OP", data.OPT_PAR_USSD_SER_OP, pdu.TagUssdServiceOp},
	}
	if len(rows) != 44 {
		t.Fatal("expected all 44 SMPP v3.4 optional parameters")
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			if row.alias != uint16(row.tag) {
				t.Errorf("data alias = 0x%04X, pdu tag = 0x%04X", row.alias, uint16(row.tag))
			}
		})
	}
}

func TestQoSTimeToLiveConstantsMatchTypedWireWidth(t *testing.T) {
	// SMPP v3.4 §5.3.2.9: QoS is a four-octet integer, including high bits.
	field := pdu.NewFieldUint32(pdu.TagQosTimeToLive, 0x12345678)
	if len(field.Data) != 4 {
		t.Fatalf("QoS encoded width = %d, want 4", len(field.Data))
	}
	for _, bound := range []int{data.OPT_PAR_QOS_TIME_TO_LIVE_MIN, data.OPT_PAR_QOS_TIME_TO_LIVE_MAX} {
		if bound != len(field.Data) {
			t.Fatalf("QoS bound %d disagrees with typed wire width %d", bound, len(field.Data))
		}
	}
}
