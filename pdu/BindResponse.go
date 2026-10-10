package pdu

import (
	"errors"
	"io"

	"github.com/gomaja/go-smpp/data"
)

// BindResp PDU.
type BindResp struct {
	base
	SystemID string
}

// NewBindResp returns BindResp.
func NewBindResp(req BindRequest) (c *BindResp) {
	c = &BindResp{
		base: newBase(),
	}
	c.SequenceNumber = req.SequenceNumber

	switch req.BindingType {
	case Transceiver:
		c.CommandID = data.BIND_TRANSCEIVER_RESP

	case Receiver:
		c.CommandID = data.BIND_RECEIVER_RESP

	case Transmitter:
		c.CommandID = data.BIND_TRANSMITTER_RESP
	}

	return
}

// NewBindTransmitterResp returns new bind transmitter resp.
func NewBindTransmitterResp() PDU {
	c := &BindResp{
		base: newBase(),
	}
	c.CommandID = data.BIND_TRANSMITTER_RESP
	return c
}

// NewBindTransceiverResp returns new bind transceiver resp.
func NewBindTransceiverResp() PDU {
	c := &BindResp{
		base: newBase(),
	}
	c.CommandID = data.BIND_TRANSCEIVER_RESP
	return c
}

// NewBindReceiverResp returns new bind receiver resp.
func NewBindReceiverResp() PDU {
	c := &BindResp{
		base: newBase(),
	}
	c.CommandID = data.BIND_RECEIVER_RESP
	return c
}

// CanResponse implements PDU interface.
func (c *BindResp) CanResponse() bool {
	return false
}

// GetResponse implements PDU interface.
func (c *BindResp) GetResponse() PDU {
	return nil
}

// Marshal implements PDU interface.
func (c *BindResp) Marshal(b *ByteBuffer) {
	// SMPP v3.4 §§4.1.2, 4.1.4, 4.1.6: failed responses omit the body.
	// Retain mandatory-field framing when explicitly registered error TLVs
	// need to remain parseable (SMPP v5 §4.8.4.1).
	if c.CommandStatus != data.ESME_ROK && len(c.OptionalParameters) == 0 {
		c.base.marshal(b, nil)
		return
	}
	c.base.marshal(b, func(w *ByteBuffer) {
		w.Grow(len(c.SystemID) + 1)

		_ = w.WriteCString(c.SystemID)
	})
}

// Unmarshal implements PDU interface.
//
// Accept both header-only errors and peers that include the system_id field.
// An absent field is tolerated so that responses omitting system_id still parse,
// matching how SubmitSMResp handles its message_id.
func (c *BindResp) Unmarshal(b *ByteBuffer) error {
	return c.base.unmarshal(b, func(w *ByteBuffer) (err error) {
		c.SystemID, err = w.ReadCString()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		// SMPP v3.4 Issue 1.2 Sections 4.1.2 and 4.1.4 say failed
		// bind_transmitter_resp and bind_receiver_resp PDUs omit the body.
		// Accept all-zero padding from SMSC simulators without treating it as TLVs.
		if c.isZeroPaddedFailedBindBody(w) {
			_, _ = w.ReadN(w.Len())
		}
		return nil
	})
}

func (c *BindResp) isZeroPaddedFailedBindBody(w *ByteBuffer) bool {
	if c.CommandStatus == data.ESME_ROK || c.SystemID != "" || w.Len() == 0 {
		return false
	}
	if c.CommandID != data.BIND_TRANSMITTER_RESP && c.CommandID != data.BIND_RECEIVER_RESP {
		return false
	}
	for _, b := range w.Bytes() {
		if b != 0 {
			return false
		}
	}
	return true
}
