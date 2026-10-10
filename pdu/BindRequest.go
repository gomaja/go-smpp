package pdu

import (
	"github.com/gomaja/go-smpp/data"
)

// BindingType indicates type of binding.
type BindingType byte

const (
	// Receiver indicates Receiver binding.
	Receiver BindingType = iota
	// Transceiver indicates Transceiver binding.
	Transceiver
	// Transmitter indicate Transmitter binding.
	Transmitter
)

// BindRequest represents a bind request.
type BindRequest struct {
	base
	SystemID         string
	Password         string
	SystemType       string
	InterfaceVersion byte
	AddressRange     AddressRange
	BindingType      BindingType
}

// NewBindRequest returns new bind request.
func NewBindRequest(t BindingType) (b *BindRequest) {
	b = &BindRequest{
		base:             newBase(),
		BindingType:      t,
		SystemID:         data.DFLT_SYSID,
		Password:         data.DFLT_PASS,
		SystemType:       data.DFLT_SYSTYPE,
		AddressRange:     AddressRange{},
		InterfaceVersion: data.SMPP_V34,
	}

	switch t {
	case Transceiver:
		b.CommandID = data.BIND_TRANSCEIVER

	case Receiver:
		b.CommandID = data.BIND_RECEIVER

	case Transmitter:
		b.CommandID = data.BIND_TRANSMITTER
	}

	return
}

// NewBindTransmitter returns new bind transmitter pdu.
func NewBindTransmitter() PDU {
	return NewBindRequest(Transmitter)
}

// NewBindTransceiver returns new bind transceiver pdu.
func NewBindTransceiver() PDU {
	return NewBindRequest(Transceiver)
}

// NewBindReceiver returns new bind receiver pdu.
func NewBindReceiver() PDU {
	return NewBindRequest(Receiver)
}

// CanResponse implements PDU interface.
func (b *BindRequest) CanResponse() bool {
	return true
}

// GetResponse implements PDU interface.
func (b *BindRequest) GetResponse() PDU {
	return NewBindResp(*b)
}

// Marshal implements PDU interface.
// SMPP v3.4 §3.3: support longer peer-issued credentials for interoperability.
func (b *BindRequest) Marshal(w *ByteBuffer) {
	b.base.marshal(w, func(w *ByteBuffer) {
		w.Grow(len(b.SystemID) + len(b.Password) + len(b.SystemType) + 4)

		_ = w.WriteCString(b.SystemID)
		_ = w.WriteCString(b.Password)
		_ = w.WriteCString(b.SystemType)
		_ = w.WriteByte(b.InterfaceVersion)
		b.AddressRange.Marshal(w)
	})
}

// Unmarshal implements PDU interface.
func (b *BindRequest) Unmarshal(w *ByteBuffer) error {
	return b.base.unmarshal(w, func(w *ByteBuffer) (err error) {
		// SMPP v3.4 §5.1.2 Table 5-1: a reused bind object follows
		// the incoming command, so GetResponse selects its matching ID.
		switch b.CommandID {
		case data.BIND_RECEIVER:
			b.BindingType = Receiver
		case data.BIND_TRANSMITTER:
			b.BindingType = Transmitter
		case data.BIND_TRANSCEIVER:
			b.BindingType = Transceiver
		}
		if b.SystemID, err = w.ReadCString(); err == nil {
			if b.Password, err = w.ReadCString(); err == nil {
				if b.SystemType, err = w.ReadCString(); err == nil {
					if b.InterfaceVersion, err = w.ReadByte(); err == nil {
						err = b.AddressRange.Unmarshal(w)
					}
				}
			}
		}
		return
	}, data.BIND_RECEIVER, data.BIND_TRANSMITTER, data.BIND_TRANSCEIVER)
}
