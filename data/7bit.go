package data

import (
	"errors"
	"unicode/utf8"

	"golang.org/x/text/encoding"
	"golang.org/x/text/transform"
)

// The GSM 7-bit alphabet tables follow 3GPP TS 23.038 sections 6.2.1 and
// 6.2.1.1, cross-checked against the Unicode GSM0338 mapping. Portions of this
// implementation are derived from fiorix/go-smpp; see
// LICENSES/fiorix-go-smpp-MIT.txt.

// ErrInvalidCharacter means a given character can not be represented in GSM 7-bit encoding.
//
// This can only happen during encoding.
var ErrInvalidCharacter = errors.New("invalid gsm7 character")

// ErrInvalidByte means that a given byte is outside of the GSM 7-bit encoding range.
//
// This can only happen during decoding.
var ErrInvalidByte = errors.New("invalid gsm7 byte")

const escapeSequence = 0x1B

var forwardLookup = map[rune]byte{
	'@': 0x00, '£': 0x01, '$': 0x02, '¥': 0x03, 'è': 0x04, 'é': 0x05, 'ù': 0x06, 'ì': 0x07,
	'ò': 0x08, 'Ç': 0x09, '\n': 0x0a, 'Ø': 0x0b, 'ø': 0x0c, '\r': 0x0d, 'Å': 0x0e, 'å': 0x0f,
	'Δ': 0x10, '_': 0x11, 'Φ': 0x12, 'Γ': 0x13, 'Λ': 0x14, 'Ω': 0x15, 'Π': 0x16, 'Ψ': 0x17,
	'Σ': 0x18, 'Θ': 0x19, 'Ξ': 0x1a /* 0x1B */, 'Æ': 0x1c, 'æ': 0x1d, 'ß': 0x1e, 'É': 0x1f,
	' ': 0x20, '!': 0x21, '"': 0x22, '#': 0x23, '¤': 0x24, '%': 0x25, '&': 0x26, '\'': 0x27,
	'(': 0x28, ')': 0x29, '*': 0x2a, '+': 0x2b, ',': 0x2c, '-': 0x2d, '.': 0x2e, '/': 0x2f,
	'0': 0x30, '1': 0x31, '2': 0x32, '3': 0x33, '4': 0x34, '5': 0x35, '6': 0x36, '7': 0x37,
	'8': 0x38, '9': 0x39, ':': 0x3a, ';': 0x3b, '<': 0x3c, '=': 0x3d, '>': 0x3e, '?': 0x3f,
	'¡': 0x40, 'A': 0x41, 'B': 0x42, 'C': 0x43, 'D': 0x44, 'E': 0x45, 'F': 0x46, 'G': 0x47,
	'H': 0x48, 'I': 0x49, 'J': 0x4a, 'K': 0x4b, 'L': 0x4c, 'M': 0x4d, 'N': 0x4e, 'O': 0x4f,
	'P': 0x50, 'Q': 0x51, 'R': 0x52, 'S': 0x53, 'T': 0x54, 'U': 0x55, 'V': 0x56, 'W': 0x57,
	'X': 0x58, 'Y': 0x59, 'Z': 0x5a, 'Ä': 0x5b, 'Ö': 0x5c, 'Ñ': 0x5d, 'Ü': 0x5e, '§': 0x5f,
	'¿': 0x60, 'a': 0x61, 'b': 0x62, 'c': 0x63, 'd': 0x64, 'e': 0x65, 'f': 0x66, 'g': 0x67,
	'h': 0x68, 'i': 0x69, 'j': 0x6a, 'k': 0x6b, 'l': 0x6c, 'm': 0x6d, 'n': 0x6e, 'o': 0x6f,
	'p': 0x70, 'q': 0x71, 'r': 0x72, 's': 0x73, 't': 0x74, 'u': 0x75, 'v': 0x76, 'w': 0x77,
	'x': 0x78, 'y': 0x79, 'z': 0x7a, 'ä': 0x7b, 'ö': 0x7c, 'ñ': 0x7d, 'ü': 0x7e, 'à': 0x7f,
}
var forwardEscape = map[rune]byte{
	'\f': 0x0A, '^': 0x14, '{': 0x28, '}': 0x29, '\\': 0x2F, '[': 0x3C, '~': 0x3D, ']': 0x3E, '|': 0x40, '€': 0x65,
}
var reverseLookup = map[byte]rune{
	0x00: '@', 0x01: '£', 0x02: '$', 0x03: '¥', 0x04: 'è', 0x05: 'é', 0x06: 'ù', 0x07: 'ì',
	0x08: 'ò', 0x09: 'Ç', 0x0a: '\n', 0x0b: 'Ø', 0x0c: 'ø', 0x0d: '\r', 0x0e: 'Å', 0x0f: 'å',
	0x10: 'Δ', 0x11: '_', 0x12: 'Φ', 0x13: 'Γ', 0x14: 'Λ', 0x15: 'Ω', 0x16: 'Π', 0x17: 'Ψ',
	0x18: 'Σ', 0x19: 'Θ', 0x1a: 'Ξ' /* 0x1B */, 0x1c: 'Æ', 0x1d: 'æ', 0x1e: 'ß', 0x1f: 'É',
	0x20: ' ', 0x21: '!', 0x22: '"', 0x23: '#', 0x24: '¤', 0x25: '%', 0x26: '&', 0x27: '\'',
	0x28: '(', 0x29: ')', 0x2a: '*', 0x2b: '+', 0x2c: ',', 0x2d: '-', 0x2e: '.', 0x2f: '/',
	0x30: '0', 0x31: '1', 0x32: '2', 0x33: '3', 0x34: '4', 0x35: '5', 0x36: '6', 0x37: '7',
	0x38: '8', 0x39: '9', 0x3a: ':', 0x3b: ';', 0x3c: '<', 0x3d: '=', 0x3e: '>', 0x3f: '?',
	0x40: '¡', 0x41: 'A', 0x42: 'B', 0x43: 'C', 0x44: 'D', 0x45: 'E', 0x46: 'F', 0x47: 'G',
	0x48: 'H', 0x49: 'I', 0x4a: 'J', 0x4b: 'K', 0x4c: 'L', 0x4d: 'M', 0x4e: 'N', 0x4f: 'O',
	0x50: 'P', 0x51: 'Q', 0x52: 'R', 0x53: 'S', 0x54: 'T', 0x55: 'U', 0x56: 'V', 0x57: 'W',
	0x58: 'X', 0x59: 'Y', 0x5a: 'Z', 0x5b: 'Ä', 0x5c: 'Ö', 0x5d: 'Ñ', 0x5e: 'Ü', 0x5f: '§',
	0x60: '¿', 0x61: 'a', 0x62: 'b', 0x63: 'c', 0x64: 'd', 0x65: 'e', 0x66: 'f', 0x67: 'g',
	0x68: 'h', 0x69: 'i', 0x6a: 'j', 0x6b: 'k', 0x6c: 'l', 0x6d: 'm', 0x6e: 'n', 0x6f: 'o',
	0x70: 'p', 0x71: 'q', 0x72: 'r', 0x73: 's', 0x74: 't', 0x75: 'u', 0x76: 'v', 0x77: 'w',
	0x78: 'x', 0x79: 'y', 0x7a: 'z', 0x7b: 'ä', 0x7c: 'ö', 0x7d: 'ñ', 0x7e: 'ü', 0x7f: 'à',
}
var reverseEscape = map[byte]rune{
	0x0A: '\f', 0x14: '^', 0x28: '{', 0x29: '}', 0x2F: '\\', 0x3C: '[', 0x3D: '~', 0x3E: ']', 0x40: '|', 0x65: '€',
}

// ValidateGSM7String returns the characters, in the given text, that can not be represented in GSM 7-bit encoding.
func ValidateGSM7String(text string) []rune {
	invalidChars := make([]rune, 0, 4)
	for _, r := range text {
		if _, ok := forwardLookup[r]; !ok {
			if _, ok := forwardEscape[r]; !ok {
				invalidChars = append(invalidChars, r)
			}
		}
	}
	return invalidChars
}

// ValidateGSM7Buffer returns the bytes, in the given buffer, that are outside of the GSM 7-bit encoding range.
func ValidateGSM7Buffer(buffer []byte) []byte {
	invalidBytes := make([]byte, 0, 4)
	count := 0
	for count < len(buffer) {
		b := buffer[count]
		if b == escapeSequence {
			count++
			if count >= len(buffer) {
				invalidBytes = append(invalidBytes, b)
				break
			}
			e := buffer[count]
			if _, ok := decodeGSM7Rune(e, true); !ok {
				invalidBytes = append(invalidBytes, b, e)
			}
		} else if _, ok := reverseLookup[b]; !ok {
			invalidBytes = append(invalidBytes, b)
		}
		count++
	}
	return invalidBytes
}

// GetEscapeChars returns the escape characters in the given text, that doesn't exist in GSM 7-bit DEFAULT alphabet table
func GetEscapeChars(runeText []rune) []rune {
	eChars := make([]rune, 0, 4)
	for _, r := range runeText {
		if _, ok := forwardEscape[r]; ok {
			eChars = append(eChars, r)
		}
	}
	return eChars
}

// IsEscapeChar checks if the given rune is an escape char
func IsEscapeChar(c rune) bool {
	_, exists := forwardEscape[c]
	return exists
}

// GSM7 returns a GSM 7-bit Bit Encoding.
//
// Set the packed flag to true if you wish to convert septets to octets,
// this should be false for most SMPP providers. The packed encoder uses CR in
// seven spare bits as an interoperability convention, matching the padding
// rule for USSD in TS 23.038 §6.1.2.3.1. SMS packing itself permits zero fill
// (TS 23.038 §6.1.2.1.1). The decoder accepts both conventions; use
// DecodeGSM7Packed with an explicit septet count when a literal final @ or CR
// could otherwise be mistaken for padding. A terminal CR at an octet boundary
// receives a second CR; without a count, this is indistinguishable from two
// literal CRs straddling that boundary.
func GSM7(packed bool) encoding.Encoding {
	return gsm7Encoding{packed: packed}
}

type gsm7Encoding struct {
	packed bool
}

func (g gsm7Encoding) NewDecoder() *encoding.Decoder {
	return &encoding.Decoder{Transformer: &gsm7Decoder{
		packed: g.packed,
	}}
}

func (g gsm7Encoding) NewEncoder() *encoding.Encoder {
	return &encoding.Encoder{Transformer: &gsm7Encoder{
		packed: g.packed,
	}}
}

func (g gsm7Encoding) String() string {
	if g.packed {
		return "GSM 7-bit (Packed)"
	}
	return "GSM 7-bit (Unpacked)"
}

type gsm7Decoder struct {
	packed     bool
	bits       uint16
	nBits      uint
	octets     uint
	last       byte
	lastSeptet byte
	escape     bool
}

func (g *gsm7Decoder) Reset() {
	*g = gsm7Decoder{packed: g.packed}
}

func (g *gsm7Decoder) Transform(dst, src []byte, atEOF bool) (nDst, nSrc int, err error) {
	if !g.packed {
		for nSrc < len(src) {
			b := src[nSrc]
			r, ok := decodeGSM7Rune(b, false)
			size := 1
			if b == escapeSequence {
				if nSrc+1 == len(src) {
					if !atEOF {
						return nDst, nSrc, transform.ErrShortSrc
					}
					return nDst, nSrc, ErrInvalidByte
				}
				r, ok = decodeGSM7Rune(src[nSrc+1], true)
				size = 2
			}
			if !ok {
				return nDst, nSrc, ErrInvalidByte
			}
			if len(dst)-nDst < utf8.RuneLen(r) {
				return nDst, nSrc, transform.ErrShortDst
			}
			nDst += utf8.EncodeRune(dst[nDst:], r)
			nSrc += size
		}
		return nDst, nSrc, nil
	}
	for {
		if g.nBits < 7 {
			if nSrc == len(src) {
				if g.escape {
					if !atEOF {
						return nDst, nSrc, transform.ErrShortSrc
					}
					return nDst, nSrc, ErrInvalidByte
				}
				if atEOF {
					g.bits = 0
					g.nBits = 0
					return nDst, nSrc, nil
				}
				if g.nBits != 0 {
					return nDst, nSrc, transform.ErrShortSrc
				}
				return nDst, nSrc, nil
			}
			g.last = src[nSrc]
			g.bits |= uint16(g.last) << g.nBits
			g.nBits += 8
			g.octets++
			nSrc++
		}
		// With no septet count, recognize legacy zero and CR padding only at
		// the end of a complete block. Bit zero still belongs to the seventh
		// septet (TS 23.038 §6.1.2.1.1); compare the seven upper bits.
		if g.nBits == 7 && g.octets%7 == 0 && nSrc == len(src) && !g.escape {
			if !atEOF {
				return nDst, nSrc, transform.ErrShortSrc
			}
			if g.last>>1 == 0 || g.last>>1 == 0x0d {
				g.bits = 0
				g.nBits = 0
				return nDst, nSrc, nil
			}
		}
		// TS 23.038 V19.0.0 §6.1.2.3.1 adds a second CR plus one zero
		// bit after a wanted CR ending on an octet boundary. As with other
		// inferred padding, two literal CRs at this boundary are ambiguous.
		if g.nBits == 8 && g.octets%7 == 1 && nSrc == len(src) &&
			!g.escape && g.lastSeptet == 0x0d && g.bits == 0x0d {
			if !atEOF {
				return nDst, nSrc, transform.ErrShortSrc
			}
			g.bits = 0
			g.nBits = 0
			return nDst, nSrc, nil
		}
		b := byte(g.bits & 0x7f)
		if b == escapeSequence && !g.escape {
			g.bits >>= 7
			g.nBits -= 7
			g.escape = true
			continue
		}
		r, ok := decodeGSM7Rune(b, g.escape)
		if !ok {
			return nDst, nSrc, ErrInvalidByte
		}
		if len(dst)-nDst < utf8.RuneLen(r) {
			return nDst, nSrc, transform.ErrShortDst
		}
		nDst += utf8.EncodeRune(dst[nDst:], r)
		g.bits >>= 7
		g.nBits -= 7
		g.lastSeptet = b
		g.escape = false
	}
}

type gsm7Encoder struct {
	packed     bool
	bits       uint16
	nBits      uint
	lastSeptet byte
}

func (g *gsm7Encoder) Reset() {
	*g = gsm7Encoder{packed: g.packed}
}

func (g *gsm7Encoder) Transform(dst, src []byte, atEOF bool) (nDst, nSrc int, err error) {
	for nSrc < len(src) {
		if !utf8.FullRune(src[nSrc:]) && !atEOF {
			return nDst, nSrc, transform.ErrShortSrc
		}
		r, size := utf8.DecodeRune(src[nSrc:])
		var septets [2]byte
		count := 1
		if v, ok := forwardLookup[r]; ok {
			septets[0] = v
		} else if v, ok := forwardEscape[r]; ok {
			septets[0] = escapeSequence
			septets[1] = v
			count = 2
		} else {
			return nDst, nSrc, ErrInvalidCharacter
		}
		needed := count
		if g.packed {
			needed = int((g.nBits + uint(count)*7) / 8)
		}
		if len(dst)-nDst < needed {
			return nDst, nSrc, transform.ErrShortDst
		}
		for _, b := range septets[:count] {
			if !g.packed {
				dst[nDst] = b
				nDst++
				continue
			}
			g.bits |= uint16(b) << g.nBits
			g.lastSeptet = b
			g.nBits += 7
			if g.nBits >= 8 {
				dst[nDst] = byte(g.bits)
				nDst++
				g.bits >>= 8
				g.nBits -= 8
			}
		}
		nSrc += size
	}
	if g.packed && atEOF && g.nBits == 0 && g.lastSeptet == 0x0d {
		if nDst == len(dst) {
			return nDst, nSrc, transform.ErrShortDst
		}
		// TS 23.038 V19.0.0 §6.1.2.3.1: preserve a wanted CR ending on
		// an octet boundary by appending another CR and one zero bit.
		dst[nDst] = 0x0d
		nDst++
		g.lastSeptet = 0
	}
	if g.packed && atEOF && g.nBits != 0 {
		if nDst == len(dst) {
			return nDst, nSrc, transform.ErrShortDst
		}
		// Preserve the package's CR-padding convention when seven spare
		// bits would otherwise be decoded as @. TS 23.038 §6.1.2.3.1
		// prescribes this convention for USSD; SMS §6.1.2.1.1 uses zeros.
		if g.nBits == 1 {
			g.bits |= 0x0d << 1
		}
		dst[nDst] = byte(g.bits)
		nDst++
		g.bits = 0
		g.nBits = 0
		g.lastSeptet = 0
	}
	return nDst, nSrc, nil
}

// DecodeGSM7Packed decodes packed GSM 7-bit data after fillBits alignment bits.
// fillBits must be in 0..6, as used to align a UDH (TS 23.040 §9.2.3.24).
// A nonnegative septetCount selects exactly that many septets, preserving
// literal final @ and CR characters. A count of -1 infers the count and accepts
// zero or CR padding. Without a count, a terminal @ or CR occupying a complete
// padding septet is ambiguous. A duplicated CR after an octet boundary is also
// indistinguishable from two literal CRs straddling that boundary.
func DecodeGSM7Packed(src []byte, fillBits uint8, septetCount int) (string, error) {
	if fillBits > 6 || septetCount < -1 {
		return "", ErrInvalidByte
	}
	if len(src) == 0 {
		if septetCount > 0 {
			return "", ErrInvalidByte
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
		return "", ErrInvalidByte
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
		duplicateCR := count > 1 && last == 0x0d && septets[count-2] == 0x0d &&
			(int(fillBits)+(count-1)*7)%8 == 0 && availableBits%7 == 1
		if !escaped && (fullPadding || duplicateCR) {
			septets = septets[:count-1]
		}
	}
	return GSM7BIT.Decode(septets)
}

func decodeGSM7Rune(b byte, escaped bool) (rune, bool) {
	if escaped {
		if r, ok := reverseEscape[b]; ok {
			return r, true
		}
		// TS 23.038 §6.2.1.1: unassigned extensions use the main table;
		// its reserved escape-to-another-extension entry displays a space.
		if b == escapeSequence {
			return ' ', true
		}
	}
	r, ok := reverseLookup[b]
	return r, ok
}
