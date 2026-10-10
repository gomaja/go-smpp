package pdu

import (
	"fmt"
	"strings"

	"github.com/gomaja/go-smpp/data"
)

// Address smpp address of src and dst.
type Address struct {
	ton     byte
	npi     byte
	address string
}

// NewAddress returns new address with default max length.
func NewAddress() Address {
	return Address{ton: data.GetDefaultTon(), npi: data.GetDefaultNpi()}
}

// NewAddressWithAddr returns new address.
func NewAddressWithAddr(addr string) (a Address, err error) {
	a = NewAddress()
	err = a.SetAddress(addr)
	return
}

// NewAddressWithTonNpi returns new address with ton, npi.
func NewAddressWithTonNpi(ton, npi byte) Address {
	return Address{ton: ton, npi: npi}
}

// NewAddressWithTonNpiAddr returns new address with ton, npi, addr string.
func NewAddressWithTonNpiAddr(ton, npi byte, addr string) (a Address, err error) {
	a = NewAddressWithTonNpi(ton, npi)
	err = a.SetAddress(addr)
	return
}

// Unmarshal from buffer.
func (c *Address) Unmarshal(b *ByteBuffer) (err error) {
	return c.unmarshal(b, data.ESME_RINVPARLEN)
}

func (c *Address) unmarshal(b *ByteBuffer, status data.CommandStatusType) (err error) {
	// SMPP v3.4 §§2.8, 5.1.3: the containing field determines whether
	// an invalid address is a source or destination address error.
	defer func() { err = withDecodeStatus(err, status) }()
	if c.ton, err = b.ReadByte(); err == nil {
		if c.npi, err = b.ReadByte(); err == nil {
			var addr string
			if addr, err = b.ReadCString(); err == nil {
				// SMPP v3.4 §3.3: receive lengths are limited by the frame.
				c.address = addr
			}
		}
	}
	return
}

// Marshal to buffer.
func (c *Address) Marshal(b *ByteBuffer) {
	b.Grow(3 + len(c.address))
	_ = b.WriteByte(c.ton)
	_ = b.WriteByte(c.npi)
	_ = b.WriteCString(c.address)
}

// SetTon sets ton.
func (c *Address) SetTon(ton byte) {
	c.ton = ton
}

// SetNpi sets npi.
func (c *Address) SetNpi(npi byte) {
	c.npi = npi
}

// SetAddress accepts up to 64 content octets, the widest SMPP address field.
// Address does not know its containing PDU; call pdu.Validate before sending
// to enforce the per-PDU 21/65-octet limits, including the NUL terminator.
func (c *Address) SetAddress(addr string) (err error) {
	if strings.IndexByte(addr, 0) >= 0 {
		return fmt.Errorf("address contains an embedded terminator")
	}
	// SMPP v3.4 §3.1.1, Tables 4-20 and 4-30: widest maximum includes NUL.
	if len(addr)+1 > data.SM_DATA_ADDR_LEN {
		err = fmt.Errorf("Address len exceed limit. (%d > %d)", len(addr)+1, data.SM_DATA_ADDR_LEN)
	} else {
		c.address = addr
	}
	return
}

// Ton returns assigned ton.
func (c Address) Ton() byte {
	return c.ton
}

// Npi returns assigned npi.
func (c Address) Npi() byte {
	return c.npi
}

// Address returns assigned address (in string).
func (c Address) Address() string {
	return c.address
}

// String implement stringer interface
func (c Address) String() string {
	return c.address
}
