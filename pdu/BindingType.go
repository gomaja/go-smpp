package pdu

import "strconv"

// String returns a readable bind mode name.
func (t BindingType) String() string {
	switch t {
	case Receiver:
		return "receiver"
	case Transmitter:
		return "transmitter"
	case Transceiver:
		return "transceiver"
	default:
		return "BindingType(" + strconv.FormatUint(uint64(t), 10) + ")"
	}
}
