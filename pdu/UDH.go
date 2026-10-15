package pdu

import (
	"fmt"

	"github.com/gomaja/go-smpp/data"
	"github.com/gomaja/go-sms/encoding/tpdu"
)

// This package supports message uses of UDH for message concatenation.
// Both 8-bit and 16-bit reference numbers are supported for concatenated
// messages, as defined by 3GPP TS 23.040 sections 9.2.3.24.1 and 9.2.3.24.8.

// UDH represent User Data Header
// as defined in 3GPP TS 23.040 Section 9.2.3.24.
type UDH []InfoElement

// UDHL returns length (octets) of encoded UDH, including the UDHL byte.
//
// If there is no InfoElement (IE), returns 0. If the body exceeds 255 octets,
// returns -1; a 255-octet body has an encoded length of 256.
func (u UDH) UDHL() (l int) {
	if len(u) == 0 {
		return
	}

	for i := range u {
		if len(u[i].Data) > 255 {
			return -1
		}

		// to account for id and type bytes
		if l += 2 + len(u[i].Data); l > 255 {
			return -1
		}
	}

	// include the udhlength byte itself
	l++

	return
}

// MarshalBinary marshal UDH into bytes array
// The first byte is UDHL
// MarshalBinary preserve InformationElement order as they appears in the UDH
func (u UDH) MarshalBinary() (b []byte, err error) {
	reservedLength := u.UDHL()
	if reservedLength == -1 {
		err = fmt.Errorf("header limit (255 in marshal size) exceeds")
		return
	}
	if reservedLength == 0 {
		return
	}

	// TS 23.040 §9.2.3.24: go-sms owns complete-header serialization.
	return u.smsHeader().MarshalBinary()
}

// UnmarshalBinary reads the InformationElements from raw binary UDH.
// Unmarshal preserve InfoElement order as they appears in the raw data
// The src contains the complete UDH, including the UDHL and all IEs.
// Returns the number of bytes read from src, and the first error
// detected while unmarshalling.
//
// Since UDHL can only represented in 1 byte, UnmarshalBinary
// will only read up to a maximum of 256 byte regardless of src length
// Parsed IE data is copied by go-sms and does not alias src. Empty or ignored
// headers return an error without changing the receiver.
func (u *UDH) UnmarshalBinary(src []byte) (int, error) {
	// TS 23.040 §9.2.3.24: parse within UDHL and ignore the whole header
	// on a malformed final IE. This public facade requires a nonempty header.
	var header tpdu.UserDataHeader
	n, err := header.UnmarshalBinary(src)
	if err != nil {
		return 0, err
	}
	if len(header) == 0 {
		return 0, fmt.Errorf("invalid or ignored UDH")
	}
	*u = udhFromSMS(header)
	return n, nil
}

func (u UDH) smsHeader() tpdu.UserDataHeader {
	if len(u) == 0 {
		return nil
	}
	header := make(tpdu.UserDataHeader, len(u))
	for i, ie := range u {
		header[i] = tpdu.InformationElement(ie)
	}
	return header
}

func udhFromSMS(header tpdu.UserDataHeader) UDH {
	u := make(UDH, len(header))
	for i, ie := range header {
		u[i] = InfoElement(ie)
	}
	return u
}

// FindInfoElement find the first occurrence of the Information Element with id
func (u UDH) FindInfoElement(id byte) (ie *InfoElement, found bool) {
	for i := range u {
		if u[i].ID == id {
			return &u[i], true
		}
	}
	return nil, false
}

// GetConcatInfo returns the last applicable concatenation IE (TS 23.040
// §9.2.3.24). IEs with the wrong length, zero total or sequence, or sequence
// greater than total are ignored (§§9.2.3.24.1, 9.2.3.24.8).
// For an 8-bit reference, mref is that octet (0–255); a 16-bit reference
// occupies the full uint16 range (0–65535).
func (u UDH) GetConcatInfo() (totalParts, partNum byte, mref uint16, found bool) {
	info, found := u.smsHeader().ConcatInfo()
	return byte(info.Total), byte(info.Seqno), uint16(info.Ref), found
}

// InfoElement represent a 3 parts Information-Element
// as defined in 3GPP TS 23.040 Section 9.2.3.24
// Each InfoElement is comprised of it's identifier and data
type InfoElement struct {
	ID   byte
	Data []byte
}

// NewIEConcatMessage creates a new IE element for 8-bit concat message info
// IE.Data is populated at time of object creation
func NewIEConcatMessage(totalParts, partNum, mref byte) InfoElement {
	return InfoElement{
		ID:   data.UDH_CONCAT_MSG_8_BIT_REF,
		Data: []byte{byte(mref), byte(totalParts), byte(partNum)},
	}
}

// NewIEConcatMessage16Bit creates a new IE element for 16-bit concat message info
// IE.Data is populated at time of object creation
// mref is a uint16 value (0-65535)
func NewIEConcatMessage16Bit(totalParts, partNum byte, mref uint16) InfoElement {
	return InfoElement{
		ID:   data.UDH_CONCAT_MSG_16_BIT_REF,
		Data: []byte{byte(mref >> 8), byte(mref), byte(totalParts), byte(partNum)},
	}
}

// UnmarshalBinary unmarshal IE from binary in src, only read a single IE,
// expect src at least of length 2 with correct IE format:
//
//	[ ID_1, LENGTH_1, DATA_N ]
func (ie *InfoElement) UnmarshalBinary(src []byte) (int, error) {
	// Standalone IE decoding stays local pending https://github.com/gomaja/go-sms/issues/26.
	if len(src) < 2 {
		return 0, fmt.Errorf("decode error InfoElement underflow, len = %d", len(src))
	}

	// second byte is len
	ieLen := int(src[1])

	// check length, excluding first 2 bytes
	if len(src)-2 < ieLen {
		return 0, fmt.Errorf("decode error InfoElement underflow, expect length %d, got %d", ieLen, len(src))
	}

	ie.ID = src[0]               // first byte is ID
	ie.Data = src[2:(ieLen + 2)] // 3rd byte onward is data

	return 2 + ieLen, nil
}
