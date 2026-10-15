package data

import (
	"fmt"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/gomaja/go-sms/encoding/gsm7"
	"github.com/gomaja/go-sms/encoding/tpdu"
	smsucs2 "github.com/gomaja/go-sms/encoding/ucs2"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
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
	return err == nil && uint(len(encoded)) > octetLimit
}

// EncodeSplit delegates text segmentation to the underlying encoding.
// Encodings without a splitter return ErrNotImplSplitterInterface.
func (c *CustomEncoding) EncodeSplit(text string, octetLimit uint) ([][]byte, error) {
	if splitter, ok := c.encDec.(Splitter); ok {
		return splitter.EncodeSplit(text, octetLimit)
	}
	return nil, ErrNotImplSplitterInterface
}

// Alphabet conversion is owned by go-sms (TS 23.038 §§6.2.1–6.2.1.1).
type gsm7bit struct{}

func (*gsm7bit) Encode(text string) ([]byte, error) {
	return encodeGSM7(text)
}

func (*gsm7bit) Decode(src []byte) (string, error) {
	return decodeGSM7(src)
}

func encodeGSM7(text string) ([]byte, error) {
	encoded, err := gsm7.Encode([]byte(text))
	if err == nil && encoded == nil {
		encoded = []byte{}
	}
	return encoded, err
}

func decodeGSM7(src []byte) (string, error) {
	text, err := gsm7.Decode(src)
	return string(text), err
}

func (*gsm7bit) DataCoding() byte { return GSM7BITCoding }

func (c *gsm7bit) ShouldSplit(text string, octetLimit uint) bool {
	septets, err := c.Encode(text)
	return err == nil && uint(len(septets)) > octetLimit
}

func (c *gsm7bit) EncodeSplit(text string, octetLimit uint) ([][]byte, error) {
	if octetLimit == 0 {
		return nil, fmt.Errorf("split limit must be positive")
	}
	septets, err := c.Encode(text)
	if err != nil {
		return nil, err
	}
	parts := [][]byte{}
	for start := 0; start < len(septets); {
		end := start + int(min(octetLimit, uint(len(septets)-start)))
		// TS 23.040 §9.2.3.24.1: keep an escape and its character together.
		if septets[end-1] == escapeSequence {
			end--
		}
		if end == start {
			return nil, fmt.Errorf("split limit cannot fit a GSM character")
		}
		parts = append(parts, septets[start:end:end])
		start = end
	}
	return parts, nil
}

type gsm7bitPacked struct{}

func (*gsm7bitPacked) Encode(text string) ([]byte, error) {
	septets, err := encodeGSM7(text)
	if err != nil {
		return nil, err
	}
	if len(septets) == 0 {
		return []byte{}, nil
	}
	return packGSM7WithFill(septets, 0), nil
}

func (*gsm7bitPacked) Decode(src []byte) (string, error) {
	return decodeGSM7Packed(src, 0, -1, true)
}

func (*gsm7bitPacked) DataCoding() byte { return GSM7BITCoding }

func (c *gsm7bitPacked) ShouldSplit(text string, octetLimit uint) bool {
	encoded, err := c.Encode(text)
	return err == nil && uint(len(encoded)) > octetLimit
}

func (*gsm7bitPacked) EncodeSplit(text string, octetLimit uint) ([][]byte, error) {
	if octetLimit == 0 {
		return nil, fmt.Errorf("split limit must be positive")
	}
	septets, err := encodeGSM7(text)
	if err != nil {
		return nil, err
	}
	parts := [][]byte{}
	// One fill bit follows a six-octet UDH (TS 23.040 §9.2.3.24).
	limit := int((min(octetLimit, uint(len(septets)+1))*8 - 1) / 7)
	for start := 0; start < len(septets); {
		end := min(start+limit, len(septets))
		if end > start && septets[end-1] == escapeSequence {
			end--
		}
		if end == start {
			return nil, fmt.Errorf("split limit cannot fit a GSM character")
		}
		segment := packGSM7WithFill(septets[start:end], 1)
		if uint(len(segment)) > octetLimit {
			end--
			if end > start && septets[end-1] == escapeSequence {
				end--
			}
			if end == start {
				return nil, fmt.Errorf("split limit cannot fit a padded GSM character")
			}
			segment = packGSM7WithFill(septets[start:end], 1)
		}
		parts = append(parts, segment)
		start = end
	}
	return parts, nil
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
	return isASCII(text) && uint(len(text)) > octetLimit
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

func (c *iso88591) ShouldSplit(text string, octetLimit uint) bool {
	encoded, err := c.Encode(text)
	return err == nil && uint(len(encoded)) > octetLimit
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

func (c *iso88595) ShouldSplit(text string, octetLimit uint) bool {
	encoded, err := c.Encode(text)
	return err == nil && uint(len(encoded)) > octetLimit
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

func (c *iso88598) ShouldSplit(text string, octetLimit uint) bool {
	encoded, err := c.Encode(text)
	return err == nil && uint(len(encoded)) > octetLimit
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

type ucs2 struct{ strict bool }

func (c *ucs2) Encode(text string) ([]byte, error) {
	// Validate before rune conversion can replace malformed UTF-8 (RFC 3629 §3).
	if !utf8.ValidString(text) {
		return nil, fmt.Errorf("text is not valid UTF-8")
	}
	if text == "" {
		return []byte{}, nil
	}
	if c.strict {
		return smsucs2.Encode([]rune(text))
	}
	return smsucs2.EncodeUTF16([]rune(text))
}

func (*ucs2) Decode(src []byte) (string, error) {
	text, err := smsucs2.Decode(src)
	return string(text), err
}

func (c *ucs2) ShouldSplit(text string, octetLimit uint) (shouldSplit bool) {
	if !utf8.ValidString(text) {
		return false
	}
	if c.strict {
		encoded, err := c.Encode(text)
		return err == nil && uint(len(encoded)) > octetLimit
	}
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
	if _, err := c.Encode(text); err != nil {
		return nil, err
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
	GSM7BIT Encoding = &gsm7bit{}

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

	// UCS2 is UTF-16BE without a BOM, including surrogate pairs for scalars
	// above U+FFFF. This interoperability extension carries SMPP data_coding
	// 0x08 (SMPP v3.4 §5.2.19); it is not strict UCS2. Use UCS2Strict for the
	// 16-bit alphabet of TS 23.038 §6.2.3. Both decoders accept UTF-16 pairs
	// and return go-sms errors and any decoded prefix for malformed input.
	UCS2 Encoding = &ucs2{}

	// UCS2Strict encodes the strict UCS2 alphabet of TS 23.038 §6.2.3,
	// rejecting scalars above U+FFFF. It carries the same SMPP coding 0x08
	// and has the same tolerant UTF-16BE decoder as UCS2.
	UCS2Strict Encoding = &ucs2{strict: true}
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
	// Preserve SMPP's direct reserved values 0x0b, 0x0c and 0x0f exactly.
	if code < 0x10 {
		return NewCustomEncoding(code, underlying)
	}
	// TS 23.038 §4 interpretation beyond SMPP v3.4 §5.2.19's direct
	// values is a lenient interoperability extension. Delegate reserved-bit
	// interpretation too: E4-E7/EC-EF select UCS2; FC-FF select binary.
	dcs := tpdu.DCS(code)
	if dcs.Compressed() {
		// TS 23.038 §4 selects TS 23.042 compression, which is unsupported.
		underlying = BINARY8BIT2
	} else {
		switch dcs.Alphabet() {
		case tpdu.Alpha8Bit:
			underlying = BINARY8BIT2
		case tpdu.AlphaUCS2:
			underlying = UCS2
		}
	}
	return NewCustomEncoding(code, underlying)
}

// Splitter extend encoding object by defining a split function
// that split a string into multiple segments
// Each segment string, when encoded, must be within a certain octet limit
type Splitter interface {
	// ShouldSplit reports whether the encoded text exceeds octetLimit.
	// It returns false when text cannot be encoded, so callers use Encode
	// to obtain the error with its offset in the complete message.
	ShouldSplit(text string, octetLimit uint) (should bool)
	EncodeSplit(text string, octetLimit uint) ([][]byte, error)
}
