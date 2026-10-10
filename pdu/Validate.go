package pdu

import (
	"errors"
	"fmt"

	"github.com/gomaja/go-smpp/data"
)

// ValidationError identifies a field exceeding its SMPP v3.4 maximum.
// Status suggests the command_status from SMPP v3.4 §5.1.3.
type ValidationError struct {
	Field  string
	Status data.CommandStatusType
	Err    error
}

// Error describes the field and the violated maximum.
func (e *ValidationError) Error() string {
	return fmt.Sprintf("%s (status 0x%08x): %v", e.Field, uint32(e.Status), e.Err)
}

// Unwrap returns the validation cause.
func (e *ValidationError) Unwrap() error { return e.Err }

// Validate reports all mandatory-field maximum violations under SMPP v3.4.
// The returned error joins ValidationErrors. Marshal and transports enforce
// wire representability; call Validate explicitly to enforce specification maxima.
// Validation examines supplied body fields even when a response omits its body.
func Validate(p PDU) error {
	var v maximumValidator
	// SMPP v3.4 §3.1.1 and Tables 4-1–4-30: C-Octet maxima include NUL.
	switch c := p.(type) {
	case *BindRequest:
		v.credentials(c.SystemID, c.Password)
		v.text("system_type", c.SystemType, 13)
		v.text("address_range", c.AddressRange.AddressRange, 41)
	case *BindResp:
		v.text("system_id", c.SystemID, 16)
	case *Outbind:
		v.credentials(c.SystemID, c.Password)
	case *SubmitSM:
		v.service(c.ServiceType)
		v.addresses(c.SourceAddr, c.DestAddr, 21)
		v.times(c.ScheduleDeliveryTime, c.ValidityPeriod, 17)
		v.message(c.Message)
	case *DeliverSM:
		v.service(c.ServiceType)
		v.addresses(c.SourceAddr, c.DestAddr, 21)
		v.times(c.ScheduleDeliveryTime, c.ValidityPeriod, 1)
		v.message(c.Message)
	case *DataSM:
		v.service(c.ServiceType)
		v.addresses(c.SourceAddr, c.DestAddr, 65)
	case *AlertNotification:
		v.address("source_addr", c.SourceAddr, 65, data.ESME_RINVSRCADR)
		v.address("esme_addr", c.EsmeAddr, 65, data.ESME_RINVDSTADR)
	case *SubmitMulti:
		v.service(c.ServiceType)
		v.address("source_addr", c.SourceAddr, 21, data.ESME_RINVSRCADR)
		v.times(c.ScheduleDeliveryTime, c.ValidityPeriod, 17)
		v.message(c.Message)
		v.length("number_of_dests", len(c.DestAddrs.l), 254, data.ESME_RINVNUMDESTS)
		for i, d := range c.DestAddrs.l {
			if d.IsDistributionList() {
				v.length(fmt.Sprintf("dest_addr[%d].dl_name", i), len(d.dl.name)+1, 21, data.ESME_RINVDLNAME)
			} else {
				v.address(fmt.Sprintf("dest_addr[%d].destination_addr", i), d.address, 21, data.ESME_RINVDSTADR)
			}
		}
	case *SubmitMultiResp:
		v.length("no_unsuccess", len(c.UnsuccessSMEs.Get()), 255, data.ESME_RINVNUMDESTS)
		v.messageID(c.MessageID, 65)
		for i, d := range c.UnsuccessSMEs.Get() {
			v.address(fmt.Sprintf("unsuccess_sme[%d].destination_addr", i), d.Address, 21, data.ESME_RINVDSTADR)
		}
	case *SubmitSMResp:
		v.messageID(c.MessageID, 65)
	case *DataSMResp:
		v.messageID(c.MessageID, 65)
	case *DeliverSMResp:
		v.messageID(c.MessageID, 1)
	case *QuerySM:
		v.messageID(c.MessageID, 65)
		v.address("source_addr", c.SourceAddr, 21, data.ESME_RINVSRCADR)
	case *QuerySMResp:
		v.messageID(c.MessageID, 65)
		v.text("final_date", c.FinalDate, 17)
	case *CancelSM:
		v.service(c.ServiceType)
		v.messageID(c.MessageID, 65)
		v.addresses(c.SourceAddr, c.DestAddr, 21)
	case *ReplaceSM:
		v.messageID(c.MessageID, 65)
		v.address("source_addr", c.SourceAddr, 21, data.ESME_RINVSRCADR)
		v.times(c.ScheduleDeliveryTime, c.ValidityPeriod, 17)
		v.message(c.Message)
	}
	return errors.Join(v.errors...)
}

type maximumValidator struct{ errors []error }

func (v *maximumValidator) length(field string, length, maximum int, status data.CommandStatusType) {
	if length > maximum {
		v.errors = append(v.errors, &ValidationError{Field: field, Status: status, Err: fmt.Errorf("length %d exceeds maximum %d", length, maximum)})
	}
}
func (v *maximumValidator) text(field, text string, maximum int) {
	// SMPP v3.4 §5.1.3, Table 5-2: suggest the field-specific error code.
	status := data.ESME_RINVPARLEN
	switch field {
	case "system_id":
		status = data.ESME_RINVSYSID
	case "password":
		status = data.ESME_RINVPASWD
	case "service_type":
		status = data.ESME_RINVSERTYP
	case "schedule_delivery_time":
		status = data.ESME_RINVSCHED
	case "validity_period":
		status = data.ESME_RINVEXPIRY
	case "message_id":
		status = data.ESME_RINVMSGID
	}
	v.length(field, len(text)+1, maximum, status)
}
func (v *maximumValidator) credentials(id, password string) {
	v.text("system_id", id, 16)
	v.text("password", password, 9)
}
func (v *maximumValidator) service(text string)                { v.text("service_type", text, 6) }
func (v *maximumValidator) messageID(text string, maximum int) { v.text("message_id", text, maximum) }
func (v *maximumValidator) address(field string, a Address, maximum int, status data.CommandStatusType) {
	v.length(field, len(a.address)+1, maximum, status)
}
func (v *maximumValidator) addresses(src, dst Address, maximum int) {
	v.address("source_addr", src, maximum, data.ESME_RINVSRCADR)
	v.address("destination_addr", dst, maximum, data.ESME_RINVDSTADR)
}
func (v *maximumValidator) times(schedule, validity string, maximum int) {
	v.text("schedule_delivery_time", schedule, maximum)
	v.text("validity_period", validity, maximum)
}
func (v *maximumValidator) message(m ShortMessage) {
	v.length("short_message", m.headerLength()+len(m.messageData), 254, data.ESME_RINVMSGLEN)
}
