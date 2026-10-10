package pdu

import (
	"errors"
	"io"

	"github.com/gomaja/go-smpp/data"
)

// SubmitSMResp PDU.
type SubmitSMResp struct {
	base
	MessageID string
}

// NewSubmitSMResp returns new SubmitSMResp.
func NewSubmitSMResp() PDU {
	c := &SubmitSMResp{
		base:      newBase(),
		MessageID: data.DFLT_MSGID,
	}
	c.CommandID = data.SUBMIT_SM_RESP
	return c
}

// NewSubmitSMRespFromReq returns new SubmitSMResp.
func NewSubmitSMRespFromReq(req *SubmitSM) PDU {
	c := NewSubmitSMResp().(*SubmitSMResp)
	if req != nil {
		c.SequenceNumber = req.SequenceNumber
	}
	return c
}

// CanResponse implements PDU interface.
func (c *SubmitSMResp) CanResponse() bool {
	return false
}

// GetResponse implements PDU interface.
func (c *SubmitSMResp) GetResponse() PDU {
	return nil
}

// Marshal implements PDU interface.
func (c *SubmitSMResp) Marshal(b *ByteBuffer) {
	// SMPP v3.4 §4.4.2: failed responses omit the body.
	// Retain mandatory-field framing when explicitly registered error TLVs
	// need to remain parseable (SMPP v5 §4.8.4.1).
	if c.CommandStatus != data.ESME_ROK && len(c.OptionalParameters) == 0 {
		c.base.marshal(b, nil)
		return
	}
	c.base.marshal(b, func(b *ByteBuffer) {
		b.Grow(len(c.MessageID) + 1)

		_ = b.WriteCString(c.MessageID)
	})
}

// Unmarshal implements PDU interface. Missing bodies and final unterminated
// message_id strings are tolerated to avoid retrying accepted submissions.
func (c *SubmitSMResp) Unmarshal(b *ByteBuffer) error {
	c.MessageID = ""
	return c.base.unmarshal(b, func(b *ByteBuffer) (err error) {
		c.MessageID, err = b.ReadCString()
		if errors.Is(err, io.EOF) {
			return nil
		}
		return
	}, data.SUBMIT_SM_RESP)
}
