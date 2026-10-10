package pdu

import (
	"fmt"
	"strings"

	"github.com/gomaja/go-smpp/data"
)

// DistributionList represents group of contacts.
type DistributionList struct {
	name string
}

// NewDistributionList returns a new DistributionList.
func NewDistributionList(name string) (c DistributionList, err error) {
	err = c.SetName(name)
	return
}

// Unmarshal from buffer.
func (c *DistributionList) Unmarshal(b *ByteBuffer) (err error) {
	var name string
	name, err = b.ReadCString()
	if err == nil {
		c.name = name
	}
	return
}

// Marshal to buffer.
func (c *DistributionList) Marshal(b *ByteBuffer) {
	b.Grow(1 + len(c.name))

	_ = b.WriteCString(c.name)
}

// SetName sets DistributionList name.
func (c *DistributionList) SetName(name string) (err error) {
	// SMPP v3.4 §4.5.1.2 Table 4-15: dl_name maximum includes NUL.
	if strings.IndexByte(name, 0) >= 0 {
		return fmt.Errorf("distribution list contains an embedded terminator")
	}
	if len(name)+1 > data.SM_DL_NAME_LEN {
		err = fmt.Errorf("distribution list name exceed limit. (%d > %d)", len(name)+1, data.SM_DL_NAME_LEN)
	} else {
		c.name = name
	}
	return
}

// Name returns name of DistributionList
func (c DistributionList) Name() string {
	return c.name
}
