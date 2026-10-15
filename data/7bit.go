package data

import "fmt"

const escapeSequence = 0x1b

// Packing and inferred padding remain local until
// https://github.com/gomaja/go-sms/issues/19 supplies aligned CR packing.
func packGSM7WithFill(septets []byte, fillBits uint8) []byte {
	if len(septets) == 0 {
		return nil
	}
	bits := int(fillBits) + len(septets)*7
	endsCR := septets[len(septets)-1] == 0x0d
	packed := make([]byte, packedGSM7Octets(len(septets), fillBits, endsCR))
	for i, septet := range septets {
		bit := int(fillBits) + i*7
		offset, shift := bit/8, uint(bit%8)
		value := uint16(septet) << shift
		packed[offset] |= byte(value)
		if offset+1 < len(packed) {
			packed[offset+1] |= byte(value >> 8)
		}
	}
	// Apply the CR-padding convention after alignment.
	if bits%8 == 1 {
		packed[len(packed)-1] |= 0x0d << 1
	}
	// TS 23.038 V19.0.0 §6.1.2.3.1 also requires a second CR followed
	// by one zero bit when a wanted terminal CR ends on an octet boundary.
	if bits%8 == 0 && endsCR {
		packed[len(packed)-1] = 0x0d
	}
	return packed
}

func packedGSM7Octets(septetCount int, fillBits uint8, endsCR bool) int {
	bits := int(fillBits) + septetCount*7
	octets := (bits + 7) / 8
	if septetCount > 0 && bits%8 == 0 && endsCR {
		octets++
	}
	return octets
}

// DecodeGSM7Packed decodes packed GSM 7-bit data after fillBits alignment bits.
// fillBits must be in 0..6, as used to align a UDH (TS 23.040 §9.2.3.24).
// A nonnegative septetCount selects exactly that many septets, preserving
// literal final @ and CR characters. A count of -1 infers the count and accepts
// zero or CR padding. Without a count, a terminal @ or CR occupying a complete
// padding septet is ambiguous. A duplicated CR after an octet boundary is also
// indistinguishable from two literal CRs straddling that boundary.
func DecodeGSM7Packed(src []byte, fillBits uint8, septetCount int) (string, error) {
	return decodeGSM7Packed(src, fillBits, septetCount, false)
}

func decodeGSM7Packed(src []byte, fillBits uint8, septetCount int, standalone bool) (string, error) {
	if fillBits > 6 || septetCount < -1 {
		return "", fmt.Errorf("invalid packed GSM7 fill bits or septet count")
	}
	if len(src) == 0 {
		if septetCount > 0 {
			return "", fmt.Errorf("invalid packed GSM7 fill bits or septet count")
		}
		return "", nil
	}
	// TS 23.040 V19.0.0 §9.2.3.24 inserts fill bits for alignment, not
	// as user text. Skip their values rather than rejecting received data.
	availableBits := len(src)*8 - int(fillBits)
	availableSeptets := availableBits / 7
	count := septetCount
	if count == -1 {
		count = availableSeptets
	}
	if count > availableSeptets {
		return "", fmt.Errorf("invalid packed GSM7 fill bits or septet count")
	}
	septets := make([]byte, count)
	// TS 23.038 §6.1.2.1.1 packs the least significant bit first.
	for i := range septets {
		bit := int(fillBits) + i*7
		offset, shift := bit/8, uint(bit%8)
		value := uint16(src[offset]) >> shift
		if offset+1 < len(src) {
			value |= uint16(src[offset+1]) << (8 - shift)
		}
		septets[i] = byte(value & 0x7f)
	}
	if septetCount == -1 && count > 0 {
		escaped := false
		for _, b := range septets[:count-1] {
			escaped = !escaped && b == escapeSequence
		}
		last := septets[count-1]
		// TS 23.038 §6.2.1.1: an extension's fallback septet remains
		// content even when its value would otherwise denote padding.
		fullPadding := availableBits%7 == 0 && (last == 0 || last == 0x0d)
		// The standalone decoder's existing padding check required the entire
		// final octet to equal CR, including its unused high bit. The explicit
		// fill/count helper has always inferred from septets alone.
		duplicateCR := count > 1 && last == 0x0d && septets[count-2] == 0x0d &&
			(int(fillBits)+(count-1)*7)%8 == 0 && availableBits%7 == 1 &&
			(!standalone || src[len(src)-1] == 0x0d)
		if !escaped && (fullPadding || duplicateCR) {
			septets = septets[:count-1]
		}
	}
	if standalone {
		return decodeGSM7(septets)
	}
	return GSM7BIT.Decode(septets)
}
