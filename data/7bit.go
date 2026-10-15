package data

import (
	"fmt"

	"github.com/gomaja/go-sms/encoding/gsm7"
)

const escapeSequence = 0x1b

// DecodeGSM7Packed decodes packed GSM7 after fillBits alignment bits
// (TS 23.040 section 9.2.3.24). Fill bits must be in 0..6. A nonnegative
// septetCount preserves exactly that many septets via gsm7.Unpack7Bit. With
// count -1, gsm7.Unpack7BitCR applies TS 23.038 section 6.1.2.3.1's CR
// convention: zero septets and doubled CRs are retained; a final CR on a
// complete septet boundary is removed. The mutable GSM7BIT selects the alphabet;
// any packing option on that codec is ignored while its tables are retained.
func DecodeGSM7Packed(src []byte, fillBits uint8, septetCount int) (string, error) {
	septets, err := unpackGSM7(src, fillBits, septetCount)
	if err != nil || len(src) == 0 {
		return "", err
	}
	codec, err := UnpackedGSM7(GSM7BIT)
	if err != nil {
		return "", err
	}
	return codec.Decode(septets)
}

// Validate public input before the dependency's fill-bit panic boundary.
func unpackGSM7(src []byte, fillBits uint8, septetCount int) ([]byte, error) {
	if fillBits > 6 || septetCount < -1 {
		return nil, fmt.Errorf("invalid packed GSM7 fill bits or septet count")
	}
	if len(src) == 0 {
		if septetCount > 0 {
			return nil, fmt.Errorf("invalid packed GSM7 fill bits or septet count")
		}
		return []byte{}, nil
	}
	availableSeptets := (len(src)*8 - int(fillBits)) / 7
	if septetCount > availableSeptets {
		return nil, fmt.Errorf("invalid packed GSM7 fill bits or septet count")
	}
	if septetCount == -1 {
		return gsm7.Unpack7BitCR(src, int(fillBits)), nil
	}
	return gsm7.Unpack7Bit(src, int(fillBits))[:septetCount], nil
}
