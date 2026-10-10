package pdu

import (
	"fmt"

	"github.com/gomaja/go-smpp/data"
)

// DestinationAddress represents Address or Distribution List based on destination flag.
type DestinationAddress struct {
	destFlag byte
	address  Address
	dl       DistributionList
}

// NewDestinationAddress returns new DestinationAddress.
func NewDestinationAddress() (c DestinationAddress) {
	c.destFlag = data.DFLT_DEST_FLAG
	return
}

// Unmarshal from buffer.
func (c *DestinationAddress) Unmarshal(b *ByteBuffer) (err error) {
	defer func() { err = withDecodeStatus(err, data.ESME_RINVDSTADR) }()
	if c.destFlag, err = b.ReadByte(); err == nil {
		switch c.destFlag {

		case data.SM_DEST_SME_ADDRESS:
			err = c.address.unmarshal(b, data.ESME_RINVDSTADR)

		case data.SM_DEST_DL_NAME:
			// SMPP v3.4 §5.1.3 distinguishes list names from SME addresses.
			err = withDecodeStatus(c.dl.Unmarshal(b), data.ESME_RINVDLNAME)

		default:
			// SMPP v3.4 §§5.1.3, 5.2.25: invalid destination flag.
			err = withDecodeStatus(fmt.Errorf("unrecognize dest_flag %d", c.destFlag), data.ESME_RINVDESTFLAG)

		}
	}
	return
}

// Marshal to buffer.
func (c *DestinationAddress) Marshal(b *ByteBuffer) {
	switch c.destFlag {
	case data.SM_DEST_DL_NAME:
		_ = b.WriteByte(data.SM_DEST_DL_NAME)
		c.dl.Marshal(b)

	default:
		_ = b.WriteByte(data.SM_DEST_SME_ADDRESS)
		c.address.Marshal(b)
	}
}

// Address returns underlying Address.
func (c *DestinationAddress) Address() Address {
	return c.address
}

// DistributionList returns underlying DistributionList.
func (c *DestinationAddress) DistributionList() DistributionList {
	return c.dl
}

// SetAddress marks DistributionAddress as a SME Address and assign.
func (c *DestinationAddress) SetAddress(addr Address) {
	c.destFlag = data.SM_DEST_SME_ADDRESS
	c.address = addr
}

// SetDistributionList marks DistributionAddress as a DistributionList and assign.
func (c *DestinationAddress) SetDistributionList(list DistributionList) {
	c.destFlag = data.SM_DEST_DL_NAME
	c.dl = list
}

// HasValue returns true if underlying DistributionList/Address is assigned.
func (c *DestinationAddress) HasValue() bool {
	return c.destFlag != data.DFLT_DEST_FLAG
}

// IsAddress returns true if DestinationAddress is a SME Address.
func (c *DestinationAddress) IsAddress() bool {
	return c.destFlag == data.SM_DEST_SME_ADDRESS
}

// IsDistributionList returns true if DestinationAddress is a DistributionList.
func (c *DestinationAddress) IsDistributionList() bool {
	return c.destFlag == byte(data.SM_DEST_DL_NAME)
}

// DestinationAddresses represents list of DestinationAddress.
type DestinationAddresses struct {
	l []DestinationAddress
}

// NewDestinationAddresses returns list of DestinationAddress.
func NewDestinationAddresses() (u DestinationAddresses) {
	u.l = make([]DestinationAddress, 0, 8)
	return
}

// Add to list.
func (c *DestinationAddresses) Add(addresses ...DestinationAddress) error {
	// SMPP v3.4 §5.2.24 limits number_of_dests to 254.
	if len(c.l)+len(addresses) > 254 {
		return fmt.Errorf("destination count exceeds maximum of 254")
	}
	c.l = append(c.l, addresses...)
	return nil
}

// Get list.
func (c *DestinationAddresses) Get() []DestinationAddress {
	return c.l
}

// Unmarshal from buffer.
func (c *DestinationAddresses) Unmarshal(b *ByteBuffer) (err error) {
	// SMPP v3.4 §3.3: receive all one-octet counts when entries fit.
	defer func() { err = withDecodeStatus(err, data.ESME_RINVNUMDESTS) }()
	var n byte
	if n, err = b.ReadByte(); err == nil {
		c.l = make([]DestinationAddress, n)

		var i byte
		for ; i < n; i++ {
			if err = c.l[i].Unmarshal(b); err != nil {
				return
			}
		}
	}
	return
}

// Marshal to buffer.
func (c *DestinationAddresses) Marshal(b *ByteBuffer) error {
	if len(c.l) > 255 {
		err := fmt.Errorf("destination count exceeds one-octet maximum of 255")
		b.setError(err)
		return err
	}
	n := byte(len(c.l))
	_ = b.WriteByte(n)

	var i byte
	for ; i < n; i++ {
		c.l[i].Marshal(b)
	}
	return nil
}
