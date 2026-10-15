package pdu

import (
	"github.com/gomaja/go-smpp/data"
)

// SubmitSM PDU is used by an ESME to submit a short message to the SMSC for onward
// transmission to a specified short message entity (SME). The submit_sm PDU does
// not support the transaction message mode.
type SubmitSM struct {
	base
	ServiceType          string
	SourceAddr           Address
	DestAddr             Address
	EsmClass             byte
	ProtocolID           byte
	PriorityFlag         byte
	ScheduleDeliveryTime string // not used
	ValidityPeriod       string // not used
	RegisteredDelivery   byte
	ReplaceIfPresentFlag byte // not used
	Message              ShortMessage
}

// NewSubmitSM returns SubmitSM PDU.
func NewSubmitSM() PDU {
	message, _ := NewShortMessage("")
	c := &SubmitSM{
		base:                 newBase(),
		ServiceType:          data.DFLT_SRVTYPE,
		SourceAddr:           NewAddress(),
		DestAddr:             NewAddress(),
		EsmClass:             data.DFLT_ESM_CLASS,
		ProtocolID:           data.DFLT_PROTOCOLID,
		PriorityFlag:         data.DFLT_PRIORITY_FLAG,
		ScheduleDeliveryTime: data.DFLT_SCHEDULE,
		ValidityPeriod:       data.DFLT_VALIDITY,
		RegisteredDelivery:   data.DFLT_REG_DELIVERY,
		ReplaceIfPresentFlag: data.DFTL_REPLACE_IFP,
		Message:              message,
	}
	c.CommandID = data.SUBMIT_SM
	return c
}

// ShouldSplit check if this the user data of submitSM PDU
func (c *SubmitSM) ShouldSplit() bool {
	limit := c.Message.payloadLimit()
	return limit < 0 || len(c.Message.messageData) > limit
}

// CanResponse implements PDU interface.
func (c *SubmitSM) CanResponse() bool {
	return true
}

// GetResponse implements PDU interface.
func (c *SubmitSM) GetResponse() PDU {
	return NewSubmitSMRespFromReq(c)
}

// Split split a single long text message into multiple SubmitSM PDU,
// Each have the TPUD within the GSM's User Data limit of 140 octet
// If the message is short enough and doesn't need splitting,
// Split() returns an array of length 1
func (c *SubmitSM) Split() (multiSubSM []*SubmitSM, err error) {
	multiSubSM = []*SubmitSM{}

	multiMsg, err := c.Message.split()
	if err != nil {
		return
	}
	if len(multiMsg) == 1 {
		return []*SubmitSM{c}, nil
	}

	esmClass := c.EsmClass | data.SM_UDH_GSM // must set to indicate UDH

	for _, msg := range multiMsg {
		multiSubSM = append(multiSubSM, &SubmitSM{
			base:                 c.baseForSplitPart(),
			ServiceType:          c.ServiceType,
			SourceAddr:           c.SourceAddr,
			DestAddr:             c.DestAddr,
			EsmClass:             esmClass,
			ProtocolID:           c.ProtocolID,
			PriorityFlag:         c.PriorityFlag,
			ScheduleDeliveryTime: c.ScheduleDeliveryTime,
			ValidityPeriod:       c.ValidityPeriod,
			RegisteredDelivery:   c.RegisteredDelivery,
			ReplaceIfPresentFlag: c.ReplaceIfPresentFlag,
			Message:              *msg,
		})
	}
	return
}

func (c *SubmitSM) baseForSplitPart() base {
	b := newBase()
	b.CommandID = c.CommandID
	b.CommandStatus = c.CommandStatus
	b.OptionalParameters = make([]Field, 0, len(c.OptionalParameters))
	for _, field := range c.OptionalParameters {
		// UDH carries the new concatenation metadata; do not retain stale SAR.
		if field.Tag == TagSarMsgRefNum || field.Tag == TagSarTotalSegments || field.Tag == TagSarSegmentSeqnum {
			continue
		}
		field.Data = append([]byte(nil), field.Data...)
		b.OptionalParameters = append(b.OptionalParameters, field)
	}
	return b
}

// Marshal implements PDU interface.
// SMPP v3.4 §4.4.1 Table 4-10: C-Octet maxima include NUL.
func (c *SubmitSM) Marshal(b *ByteBuffer) {
	c.base.marshal(b, func(b *ByteBuffer) {
		b.Grow(len(c.ServiceType) + len(c.ScheduleDeliveryTime) + len(c.ValidityPeriod) + 10)

		_ = b.WriteCString(c.ServiceType)
		c.SourceAddr.Marshal(b)
		c.DestAddr.Marshal(b)
		if c.Message.udHeader.hasNationalLanguage() {
			c.EsmClass |= data.SM_UDH_GSM
		}
		_ = b.WriteByte(c.EsmClass)
		_ = b.WriteByte(c.ProtocolID)
		_ = b.WriteByte(c.PriorityFlag)
		_ = b.WriteCString(c.ScheduleDeliveryTime)
		_ = b.WriteCString(c.ValidityPeriod)
		_ = b.WriteByte(c.RegisteredDelivery)
		_ = b.WriteByte(c.ReplaceIfPresentFlag)
		if err := c.Message.validateUDHI(c.EsmClass&data.SM_UDH_GSM != 0); err != nil {
			b.setError(err)
			return
		}
		_ = c.Message.Marshal(b)
	})
}

// Unmarshal implements PDU interface.
func (c *SubmitSM) Unmarshal(b *ByteBuffer) error {
	return c.base.unmarshal(b, func(b *ByteBuffer) (err error) {
		if c.ServiceType, err = b.ReadCString(); err == nil {
			if err = c.SourceAddr.unmarshal(b, data.ESME_RINVSRCADR); err == nil {
				if err = c.DestAddr.unmarshal(b, data.ESME_RINVDSTADR); err == nil {
					if c.EsmClass, err = b.ReadByte(); err == nil {
						if c.ProtocolID, err = b.ReadByte(); err == nil {
							if c.PriorityFlag, err = b.ReadByte(); err == nil {
								if c.ScheduleDeliveryTime, err = b.ReadCString(); err == nil {
									if c.ValidityPeriod, err = b.ReadCString(); err == nil {
										if c.RegisteredDelivery, err = b.ReadByte(); err == nil {
											if c.ReplaceIfPresentFlag, err = b.ReadByte(); err == nil {
												err = c.Message.Unmarshal(b, (c.EsmClass&data.SM_UDH_GSM) > 0)
											}
										}
									}
								}
							}
						}
					}
				}
			}
		}
		return
	}, data.SUBMIT_SM)
}
