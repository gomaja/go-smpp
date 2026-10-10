package pdu

import (
	"errors"
	"strings"
	"testing"

	"github.com/gomaja/go-smpp/data"
	"github.com/stretchr/testify/require"
)

func TestValidateAllMaximumViolations(t *testing.T) {
	p := NewSubmitSM().(*SubmitSM)
	p.ServiceType = strings.Repeat("S", 6)
	p.SourceAddr.address = strings.Repeat("A", 21)
	p.DestAddr.address = strings.Repeat("D", 21)
	p.ScheduleDeliveryTime = strings.Repeat("T", 17)
	p.ValidityPeriod = strings.Repeat("V", 17)
	p.Message.messageData = make([]byte, 255)
	err := Validate(p)
	require.Error(t, err)
	joined, ok := err.(interface{ Unwrap() []error })
	require.True(t, ok)
	want := map[string]data.CommandStatusType{"service_type": data.ESME_RINVSERTYP, "source_addr": data.ESME_RINVSRCADR, "destination_addr": data.ESME_RINVDSTADR, "schedule_delivery_time": data.ESME_RINVSCHED, "validity_period": data.ESME_RINVEXPIRY, "short_message": data.ESME_RINVMSGLEN}
	require.Len(t, joined.Unwrap(), len(want))
	for _, part := range joined.Unwrap() {
		var v *ValidationError
		require.ErrorAs(t, part, &v)
		require.Equal(t, want[v.Field], v.Status)
		require.NotNil(t, v.Unwrap())
		require.Contains(t, v.Error(), v.Field)
		delete(want, v.Field)
	}
	require.Empty(t, want)
	var first *ValidationError
	require.True(t, errors.As(err, &first))
}
func TestValidateDestinationLimits(t *testing.T) {
	for _, n := range []int{254, 255} {
		p := NewSubmitMulti().(*SubmitMulti)
		p.DestAddrs.l = make([]DestinationAddress, n)
		for i := range p.DestAddrs.l {
			p.DestAddrs.l[i].SetAddress(NewAddress())
		}
		require.Equal(t, n == 255, Validate(p) != nil)
	}
	p := NewSubmitMulti().(*SubmitMulti)
	d := NewDestinationAddress()
	d.SetDistributionList(DistributionList{name: strings.Repeat("L", 21)})
	p.DestAddrs.l = []DestinationAddress{d}
	var v *ValidationError
	require.ErrorAs(t, Validate(p), &v)
	require.Equal(t, "dest_addr[0].dl_name", v.Field)
	require.Equal(t, data.ESME_RINVDLNAME, v.Status)
}
func TestValidateAddressMaxima(t *testing.T) {
	for _, id := range []data.CommandIDType{data.SUBMIT_SM, data.DELIVER_SM, data.DATA_SM, data.ALERT_NOTIFICATION} {
		maximum := 21
		if id == data.DATA_SM || id == data.ALERT_NOTIFICATION {
			maximum = 65
		}
		for _, n := range []int{maximum - 1, maximum} {
			p, err := CreatePDUFromCmdID(id)
			require.NoError(t, err)
			a := Address{address: strings.Repeat("A", n)}
			switch v := p.(type) {
			case *SubmitSM:
				v.SourceAddr = a
			case *DeliverSM:
				v.SourceAddr = a
			case *DataSM:
				v.SourceAddr = a
			case *AlertNotification:
				v.SourceAddr = a
			}
			require.Equal(t, n == maximum, Validate(p) != nil)
		}
	}
}
