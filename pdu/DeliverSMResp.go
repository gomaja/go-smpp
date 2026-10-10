package pdu

import (
	"errors"
	"io"

	"github.com/gomaja/go-smpp/data"
)

// DeliverSMResp PDU.
type DeliverSMResp struct {
	base
	MessageID string
}

// NewDeliverSMResp returns new DeliverSMResp.
func NewDeliverSMResp() PDU {
	c := &DeliverSMResp{
		base:      newBase(),
		MessageID: data.DFLT_MSGID,
	}
	c.CommandID = data.DELIVER_SM_RESP
	return c
}

// NewDeliverSMRespFromReq returns new DeliverSMResp.
func NewDeliverSMRespFromReq(req *DeliverSM) PDU {
	c := NewDeliverSMResp().(*DeliverSMResp)
	if req != nil {
		c.SequenceNumber = req.SequenceNumber
	}
	return c
}

// CanResponse implements PDU interface.
func (c *DeliverSMResp) CanResponse() bool {
	return false
}

// GetResponse implements PDU interface.
func (c *DeliverSMResp) GetResponse() PDU {
	return nil
}

// Marshal implements PDU interface.
// SMPP v3.4 §4.6.2 Table 4-19; nonempty IDs follow the
// documented SMPP v5 §4.3.1.2 Table 4-23 interoperability tolerance.
func (c *DeliverSMResp) Marshal(b *ByteBuffer) {
	c.base.marshal(b, func(b *ByteBuffer) {
		b.Grow(len(c.MessageID) + 1)

		_ = b.WriteCString(c.MessageID)
	})
}

// Unmarshal implements PDU interface.
func (c *DeliverSMResp) Unmarshal(b *ByteBuffer) error {
	c.MessageID = ""
	return c.base.unmarshal(b, func(b *ByteBuffer) (err error) {
		c.MessageID, err = b.ReadCString()
		// Match the final message_id tolerance used by other submission
		// responses (SMPP v3.4 §4.6.2; v5 §4.3.1.2).
		if errors.Is(err, io.EOF) {
			return nil
		}
		return
	}, data.DELIVER_SM_RESP)
}
