package data

import (
	"fmt"
	"strings"
	"unicode/utf16"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/unicode"
)

const (
	// SMSCDefaultCoding is the SMPP v3.4 Issue 1.2 section 5.2.19 SMSC
	// Default Alphabet value.
	SMSCDefaultCoding byte = 0x00
	// GSM7BITCoding is a backward-compatible alias for SMSCDefaultCoding. This
	// package maps the SMSC default alphabet to GSM 7-bit unless callers provide
	// a custom encoding for provider-specific defaults.
	GSM7BITCoding byte = SMSCDefaultCoding
	// ASCIICoding is ascii coding
	ASCIICoding byte = 0x01
	// BINARY8BIT1Coding is 8-bit binary coding
	BINARY8BIT1Coding byte = 0x02
	// LATIN1Coding is iso-8859-1 coding
	LATIN1Coding byte = 0x03
	// BINARY8BIT2Coding is 8-bit binary coding
	BINARY8BIT2Coding byte = 0x04
	// CYRILLICCoding is iso-8859-5 coding
	CYRILLICCoding byte = 0x06
	// HEBREWCoding is iso-8859-8 coding
	HEBREWCoding byte = 0x07
	// UCS2Coding is UCS2 coding
	UCS2Coding byte = 0x08
)

// EncDec wraps encoder and decoder interface.
type EncDec interface {
	Encode(str string) ([]byte, error)
	Decode([]byte) (string, error)
}

// Encoding interface.
type Encoding interface {
	EncDec
	DataCoding() byte
}

func encode(str string, encoder *encoding.Encoder) ([]byte, error) {
	return encoder.Bytes([]byte(str))
}

func decode(data []byte, decoder *encoding.Decoder) (st string, err error) {
	tmp, err := decoder.Bytes(data)
	if err == nil {
		st = string(tmp)
	}
	return
}

// CustomEncoding is wrapper for user-defined data encoding.
type CustomEncoding struct {
	encDec EncDec
	coding byte
}

// NewCustomEncoding creates new custom encoding.
func NewCustomEncoding(coding byte, encDec EncDec) Encoding {
	return &CustomEncoding{
		coding: coding,
		encDec: encDec,
	}
}

// Encode string.
func (c *CustomEncoding) Encode(str string) ([]byte, error) {
	return c.encDec.Encode(str)
}

// Decode data to string.
func (c *CustomEncoding) Decode(data []byte) (string, error) {
	return c.encDec.Decode(data)
}

// DataCoding flag.
func (c *CustomEncoding) DataCoding() byte {
	return c.coding
}

// BaseEncoding returns the underlying codec selected for this custom encoding.
func (c *CustomEncoding) BaseEncoding() EncDec {
	return c.encDec
}

// ShouldSplit delegates to the underlying splitter, or measures the complete
// encoded field when that encoding does not support text segmentation.
func (c *CustomEncoding) ShouldSplit(text string, octetLimit uint) bool {
	if splitter, ok := c.encDec.(Splitter); ok {
		return splitter.ShouldSplit(text, octetLimit)
	}
	encoded, err := c.Encode(text)
	return err != nil || uint(len(encoded)) > octetLimit
}

// EncodeSplit delegates text segmentation to the underlying encoding.
// Encodings without a splitter return ErrNotImplSplitterInterface.
func (c *CustomEncoding) EncodeSplit(text string, octetLimit uint) ([][]byte, error) {
	if splitter, ok := c.encDec.(Splitter); ok {
		return splitter.EncodeSplit(text, octetLimit)
	}
	return nil, ErrNotImplSplitterInterface
}

type gsm7bit struct {
	packed bool
}

func (c *gsm7bit) Encode(str string) ([]byte, error) {
	return encode(str, GSM7(c.packed).NewEncoder())
}

func (c *gsm7bit) Decode(data []byte) (string, error) {
	return decode(data, GSM7(c.packed).NewDecoder())
}

func (c *gsm7bit) DataCoding() byte { return GSM7BITCoding }

// getSeptetCount returns the number of septets the given runes occupy once
// encoded. Escape characters (^ { } \ [ ~ ] | €) are not in the 3GPP TS 23.038
// section 6.2.1 default alphabet and are transmitted as two-septet escape
// sequences through the section 6.2.1.1 extension table.
func (c *gsm7bit) getSeptetCount(runeSlice []rune) int {
	escCharsLen := len(GetEscapeChars(runeSlice))
	return len(runeSlice) - escCharsLen + escCharsLen*2
}

func (c *gsm7bit) ShouldSplit(text string, octetLimit uint) (shouldSplit bool) {
	// Count septets rather than bytes: a rune is not one septet (escape
	// characters cost two) and is not one byte either (they are multi-byte
	// in UTF-8), so len(text) measures neither budget correctly.
	nSeptet := c.getSeptetCount([]rune(text))
	if c.packed {
		return uint(packedGSM7Octets(nSeptet, 0, strings.HasSuffix(text, "\r"))) > octetLimit
	}
	return uint(nSeptet) > octetLimit
}

func (c *gsm7bit) EncodeSplit(text string, octetLimit uint) (allSeg [][]byte, err error) {
	if octetLimit == 0 {
		return nil, fmt.Errorf("split limit must be positive")
	}

	allSeg = [][]byte{}
	runeSlice := []rune(text)

	// In the unpacked form every septet is carried in its own octet, so the
	// septet budget per segment equals the octet limit.
	lim := int(min(octetLimit, uint(len(runeSlice)*2)))

	fr := 0
	for fr < len(runeSlice) {
		to := min(fr+lim, len(runeSlice))

		// Walk by septet cost so escape characters are billed as two, and so
		// an escape sequence is never split across segments (3GPP TS 23.040
		// 9.2.3.24.1: "A character represented by an escape-sequence shall not
		// be split in the middle.").
		to = determineTo(fr, to, lim, runeSlice)
		if to == fr {
			return nil, fmt.Errorf("split limit cannot fit a GSM character")
		}

		seg, err := c.Encode(string(runeSlice[fr:to]))
		if err != nil {
			return nil, err
		}
		allSeg = append(allSeg, seg)
		fr = to
	}

	return
}

type gsm7bitPacked struct {
}

func (c *gsm7bitPacked) Encode(str string) ([]byte, error) {
	return encode(str, GSM7(true).NewEncoder())
}

func (c *gsm7bitPacked) Decode(data []byte) (string, error) {
	return decode(data, GSM7(true).NewDecoder())
}

func (c *gsm7bitPacked) DataCoding() byte { return GSM7BITCoding }

func (c *gsm7bitPacked) ShouldSplit(text string, octetLimit uint) (shouldSplit bool) {
	runeSlice := []rune(text)
	tLen := len(runeSlice)
	escCharsLen := len(GetEscapeChars(runeSlice))
	regCharsLen := tLen - escCharsLen
	// Escape characters occupy two septets; ETSI TS 23.038 V19.0.0
	// §6.2.1.1, unchanged in 3GPP V20.0.0 (2026-06).
	return uint(packedGSM7Octets(regCharsLen+escCharsLen*2, 0, strings.HasSuffix(text, "\r"))) > octetLimit
}

func (c *gsm7bitPacked) GetSeptetCount(runeSlice []rune) int {
	tLen := len(runeSlice)
	escCharsLen := len(GetEscapeChars(runeSlice))
	regCharsLen := tLen - escCharsLen
	return escCharsLen*2 + regCharsLen
}

func (c *gsm7bitPacked) EncodeSplit(text string, octetLimit uint) (allSeg [][]byte, err error) {
	if octetLimit == 0 {
		return nil, fmt.Errorf("split limit must be positive")
	}

	allSeg = [][]byte{}
	runeSlice := []rune(text)
	// One fill bit follows a six-octet UDH (TS 23.040 §9.2.3.24).
	lim := int((min(octetLimit, uint(len(runeSlice)*2+1))*8 - 1) / 7)

	fr, to := 0, lim
	for fr < len(runeSlice) {
		if to > len(runeSlice) {
			to = len(runeSlice)
		}

		to = determineTo(fr, to, lim, runeSlice)
		if to == fr {
			return nil, fmt.Errorf("split limit cannot fit a GSM character")
		}

		septets, err := GSM7BIT.Encode(string(runeSlice[fr:to]))
		if err != nil {
			return nil, err
		}
		// TS 23.040 §9.2.3.24: a six-octet UDH needs one fill bit.
		// Pack from septets so standalone padding is not shifted into data.
		seg := packGSM7WithFill(septets, 1)
		// A boundary CR may need one extra octet under the package's
		// TS 23.038 §6.1.2.3.1 convention. Keep that padding in the budget.
		if uint(len(seg)) > octetLimit {
			to--
			if to == fr {
				return nil, fmt.Errorf("split limit cannot fit a padded GSM character")
			}
			septets, err = GSM7BIT.Encode(string(runeSlice[fr:to]))
			if err != nil {
				return nil, err
			}
			seg = packGSM7WithFill(septets, 1)
		}

		allSeg = append(allSeg, seg)
		fr, to = to, to+lim
	}

	return
}

func determineTo(from int, to int, lim int, runeSlice []rune) int {
	used := 0
	for from < to {
		cost := 1
		if IsEscapeChar(runeSlice[from]) {
			cost = 2
		}
		if used+cost > lim {
			break
		}
		used += cost
		from++
	}
	return from
}

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
	// Apply the same CR-padding convention as GSM7(true), after alignment.
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

type ascii struct{}

func (*ascii) Encode(str string) ([]byte, error) {
	// SMPP v3.4 §5.2.19: data_coding 1 is IA5 (ASCII), not UTF-8.
	if !isASCII(str) {
		return nil, fmt.Errorf("text cannot be represented in ASCII")
	}
	return []byte(str), nil
}

func (*ascii) Decode(data []byte) (string, error) {
	// Preserve received octets for peers using a different alphabet. Caller-
	// selected ASCII text is still checked by Encode before transmission.
	return string(data), nil
}

func (*ascii) DataCoding() byte { return ASCIICoding }

func (*ascii) ShouldSplit(text string, octetLimit uint) bool {
	return uint(len(text)) > octetLimit
}

func (c *ascii) EncodeSplit(text string, octetLimit uint) ([][]byte, error) {
	if octetLimit == 0 {
		return nil, fmt.Errorf("split limit must be positive")
	}
	var segments [][]byte
	limit := int(min(octetLimit, uint(len(text))))

	for i := 0; i < len(text); {
		end := min(i+limit, len(text))

		segment, err := c.Encode(text[i:end])
		if err != nil {
			return nil, err
		}

		segments = append(segments, segment)
		i = end
	}

	return segments, nil
}

type iso88591 struct{}

func (*iso88591) Encode(str string) ([]byte, error) {
	return encode(str, charmap.ISO8859_1.NewEncoder())
}

func (*iso88591) Decode(data []byte) (string, error) {
	return decode(data, charmap.ISO8859_1.NewDecoder())
}

func (*iso88591) DataCoding() byte { return LATIN1Coding }

func (*iso88591) ShouldSplit(text string, octetLimit uint) bool {
	return uint(len([]rune(text))) > octetLimit
}

func (c *iso88591) EncodeSplit(text string, octetLimit uint) ([][]byte, error) {
	if octetLimit == 0 {
		return nil, fmt.Errorf("split limit must be positive")
	}
	var segments [][]byte
	runeSlice := []rune(text)
	limit := int(min(octetLimit, uint(len(runeSlice))))

	for i := 0; i < len(runeSlice); {
		end := min(i+limit, len(runeSlice))

		segment, err := c.Encode(string(runeSlice[i:end]))
		if err != nil {
			return nil, err
		}

		// If encoded segment exceeds limit, reduce character count
		for len(segment) > limit && end > i+1 {
			end--
			segment, err = c.Encode(string(runeSlice[i:end]))
			if err != nil {
				return nil, err
			}
		}

		segments = append(segments, segment)
		i = end
	}

	return segments, nil
}

type binary8bit1 struct{}

func (*binary8bit1) Encode(_ string) ([]byte, error) {
	return []byte{}, ErrNotImplEncode
}

func (*binary8bit1) Decode(_ []byte) (string, error) {
	return "", ErrNotImplDecode
}

func (*binary8bit1) DataCoding() byte { return BINARY8BIT1Coding }

type binary8bit2 struct{}

func (*binary8bit2) Encode(_ string) ([]byte, error) {
	return []byte{}, ErrNotImplEncode
}

func (*binary8bit2) Decode(_ []byte) (string, error) {
	return "", ErrNotImplDecode
}

func (*binary8bit2) DataCoding() byte { return BINARY8BIT2Coding }

// opaque8bit preserves an SMPP alphabet for which no text codec is available.
// Its character boundaries are unknown, so it must not advertise Splitter.
type opaque8bit struct {
	coding byte
}

func (*opaque8bit) Encode(string) ([]byte, error) { return nil, ErrNotImplEncode }

func (*opaque8bit) Decode([]byte) (string, error) { return "", ErrNotImplDecode }

func (c *opaque8bit) DataCoding() byte { return c.coding }

type iso88595 struct{}

func (*iso88595) Encode(str string) ([]byte, error) {
	return encode(str, charmap.ISO8859_5.NewEncoder())
}

func (*iso88595) Decode(data []byte) (string, error) {
	return decode(data, charmap.ISO8859_5.NewDecoder())
}

func (*iso88595) DataCoding() byte { return CYRILLICCoding }

func (*iso88595) ShouldSplit(text string, octetLimit uint) bool {
	return uint(len([]rune(text))) > octetLimit
}

func (c *iso88595) EncodeSplit(text string, octetLimit uint) ([][]byte, error) {
	if octetLimit == 0 {
		return nil, fmt.Errorf("split limit must be positive")
	}
	var segments [][]byte
	runeSlice := []rune(text)
	limit := int(min(octetLimit, uint(len(runeSlice))))

	for i := 0; i < len(runeSlice); {
		end := min(i+limit, len(runeSlice))

		segment, err := c.Encode(string(runeSlice[i:end]))
		if err != nil {
			return nil, err
		}

		// If encoded segment exceeds limit, reduce character count
		for len(segment) > limit && end > i+1 {
			end--
			segment, err = c.Encode(string(runeSlice[i:end]))
			if err != nil {
				return nil, err
			}
		}

		segments = append(segments, segment)
		i = end
	}

	return segments, nil
}

type iso88598 struct{}

func (*iso88598) Encode(str string) ([]byte, error) {
	return encode(str, charmap.ISO8859_8.NewEncoder())
}

func (*iso88598) Decode(data []byte) (string, error) {
	return decode(data, charmap.ISO8859_8.NewDecoder())
}

func (*iso88598) DataCoding() byte { return HEBREWCoding }

func (*iso88598) ShouldSplit(text string, octetLimit uint) bool {
	return uint(len([]rune(text))) > octetLimit
}

func (c *iso88598) EncodeSplit(text string, octetLimit uint) ([][]byte, error) {
	if octetLimit == 0 {
		return nil, fmt.Errorf("split limit must be positive")
	}
	var segments [][]byte
	runeSlice := []rune(text)
	limit := int(min(octetLimit, uint(len(runeSlice))))

	for i := 0; i < len(runeSlice); {
		end := min(i+limit, len(runeSlice))

		segment, err := c.Encode(string(runeSlice[i:end]))
		if err != nil {
			return nil, err
		}

		// If encoded segment exceeds limit, reduce character count
		for len(segment) > limit && end > i+1 {
			end--
			segment, err = c.Encode(string(runeSlice[i:end]))
			if err != nil {
				return nil, err
			}
		}

		segments = append(segments, segment)
		i = end
	}

	return segments, nil
}

type ucs2 struct{}

func (*ucs2) Encode(str string) ([]byte, error) {
	tmp := unicode.UTF16(unicode.BigEndian, unicode.IgnoreBOM)
	return encode(str, tmp.NewEncoder())
}

func (*ucs2) Decode(data []byte) (string, error) {
	tmp := unicode.UTF16(unicode.BigEndian, unicode.IgnoreBOM)
	return decode(data, tmp.NewDecoder())
}

func (*ucs2) ShouldSplit(text string, octetLimit uint) (shouldSplit bool) {
	octets := 0
	for _, r := range text {
		octets += 2
		if utf16.RuneLen(r) == 2 {
			octets += 2
		}
	}
	return uint(octets) > octetLimit
}

func (c *ucs2) EncodeSplit(text string, octetLimit uint) (allSeg [][]byte, err error) {
	if octetLimit == 0 {
		return nil, fmt.Errorf("split limit must be positive")
	}
	runes := []rune(text)
	for from := 0; from < len(runes); {
		to := from
		octets := uint(0)
		// TS 23.040 §9.2.3.24 budgets encoded octets. Keep both UTF-16
		// code units of a supplementary character in the same segment.
		for to < len(runes) {
			cost := uint(2)
			if utf16.RuneLen(runes[to]) == 2 {
				cost = 4
			}
			if octets+cost > octetLimit {
				break
			}
			octets += cost
			to++
		}
		if to == from {
			return nil, fmt.Errorf("split limit cannot fit a UTF-16 character")
		}
		segment, err := c.Encode(string(runes[from:to]))
		if err != nil {
			return nil, err
		}
		allSeg = append(allSeg, segment)
		from = to
	}
	return allSeg, nil
}

func (*ucs2) DataCoding() byte { return UCS2Coding }

var (
	// GSM7BIT is gsm-7bit encoding.
	GSM7BIT Encoding = &gsm7bit{packed: false}

	// GSM7BITPACKED is packed gsm-7bit encoding.
	// Most of SMSC(s) use unpack version.
	// Should be tested before using.
	GSM7BITPACKED Encoding = &gsm7bitPacked{}

	// ASCII is ascii encoding.
	ASCII Encoding = &ascii{}

	// BINARY8BIT1 is binary 8-bit encoding.
	BINARY8BIT1 Encoding = &binary8bit1{}

	// LATIN1 encoding.
	LATIN1 Encoding = &iso88591{}

	// BINARY8BIT2 is binary 8-bit encoding.
	BINARY8BIT2 Encoding = &binary8bit2{}

	// CYRILLIC encoding.
	CYRILLIC Encoding = &iso88595{}

	// HEBREW encoding.
	HEBREW Encoding = &iso88598{}

	// UCS2 encoding.
	UCS2 Encoding = &ucs2{}
)

var codingMap = map[byte]Encoding{
	GSM7BITCoding:     GSM7BIT,
	ASCIICoding:       ASCII,
	BINARY8BIT1Coding: BINARY8BIT1,
	LATIN1Coding:      LATIN1,
	BINARY8BIT2Coding: BINARY8BIT2,
	CYRILLICCoding:    CYRILLIC,
	HEBREWCoding:      HEBREW,
	UCS2Coding:        UCS2,
}

// FromDataCoding returns encoding from DataCoding value.
// SMPP v3.4 Issue 1.2 section 5.2.19 defines 0x00 as the SMSC Default
// Alphabet; this package maps it to GSM 7-bit for backward compatibility.
func FromDataCoding(code byte) (enc Encoding) {
	if enc, ok := codingMap[code]; ok {
		return enc
	}
	// SMPP v3.4 Issue 1.2 §5.2.19 assigns these values to JIS, pictogram,
	// ISO-2022-JP, extended Kanji and KS C 5601. Preserve their wire coding
	// without falsely encoding or segmenting them as the GSM alphabet.
	switch code {
	case 0x05, 0x09, 0x0a, 0x0d, 0x0e:
		return &opaque8bit{coding: code}
	}
	underlying := GSM7BIT
	// ETSI TS 23.038 V19.0.0 §4, unchanged in 3GPP V20.0.0 (2026-06),
	// defines GSM SMS DCS groups beyond SMPP's direct codes.
	// Reserved values retain the GSM default alphabet fallback.
	switch {
	case code >= 0x10 && code <= 0x7f:
		if code&0x0c == 0x0c {
			// TS 23.038 V19.0.0 §4, unchanged in 3GPP V20.0.0 (2026-06):
			// a reserved alphabet selects the
			// receiver's GSM default fallback, including when bit 5 is set.
			break
		}
		if code&0x20 != 0 {
			// TS 23.038 §4 selects TS 23.042 compression. This package does not
			// implement that codec, so preserve raw binary rather than emit text
			// with a data_coding value falsely claiming compression.
			underlying = BINARY8BIT2
		} else {
			switch code & 0x0c {
			case 0x04:
				underlying = BINARY8BIT2
			case 0x08:
				underlying = UCS2
			}
		}
	case code&0xf0 == 0xe0 && code&0x04 == 0:
		underlying = UCS2
	case code >= 0xf0 && code <= 0xf7 && code&0x04 != 0:
		underlying = BINARY8BIT2
	}
	return NewCustomEncoding(code, underlying)
}

// Splitter extend encoding object by defining a split function
// that split a string into multiple segments
// Each segment string, when encoded, must be within a certain octet limit
type Splitter interface {
	// ShouldSplit check if the encoded data of given text should be splitted under octetLimit
	ShouldSplit(text string, octetLimit uint) (should bool)
	EncodeSplit(text string, octetLimit uint) ([][]byte, error)
}
