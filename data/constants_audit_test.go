package data

import "testing"

func TestSMPPVersion33WireValue(t *testing.T) {
	// SMPP v3.4 §5.2.4: 0x00–0x33 advertise versions through v3.3.
	v := SMPP_V33
	if got := byte(v); got != 0x33 {
		t.Fatalf("SMPP_V33 wire value = 0x%02x, want 0x33", got)
	}
}

func TestRegisteredDeliveryIntermediateNotificationWirePattern(t *testing.T) {
	// SMPP v3.4 §5.2.17's xxx1xxxx wire pattern uses bit 4 despite the
	// inconsistent "bit 5" label. SMPP v5 §4.7.21 Table 4-52 confirms bit 4.
	for _, tc := range []struct {
		name string
		got  byte
	}{
		{"mask", SM_NOTIF_MASK},
		{"requested", SM_NOTIF_REQUESTED},
	} {
		if tc.got != 0x10 {
			t.Errorf("intermediate notification %s = 0x%02x, want 0x10", tc.name, tc.got)
		}
	}
}

func TestQoSTimeToLiveLengthConstants(t *testing.T) {
	// SMPP v3.4 §5.3.2.9 fixes the QoS value at four octets.
	for _, tc := range []struct {
		name      string
		got, want int
	}{
		{"qos_min", OPT_PAR_QOS_TIME_TO_LIVE_MIN, 4},
		{"qos_max", OPT_PAR_QOS_TIME_TO_LIVE_MAX, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("%s = %d, want %d", tc.name, tc.got, tc.want)
			}
		})
	}
}

func TestMessageWaitFacilitiesTagConstant(t *testing.T) {
	// SMPP v3.4 §5.3, Table 5-7; value format in §5.3.2.13.
	if OPT_PAR_MSG_WAIT != 0x0030 {
		t.Fatalf("ms_msg_wait_facilities tag = 0x%04x, want 0x0030", OPT_PAR_MSG_WAIT)
	}
}
