package data

import (
	"fmt"

	"github.com/gomaja/go-sms/encoding/gsm7"
	"github.com/gomaja/go-sms/encoding/gsm7/charset"
)

// GSM7Options selects explicit GSM7 tables. LockingShift and SingleShift are
// national language identifiers from TS 23.038 section 6.2.1.2.4 and Annex A;
// zero selects the default table. Spanish (2) has only a single shift table.
// Packed uses the same CR padding convention as GSM7BITPACKED.
type GSM7Options struct {
	Packed       bool
	LockingShift int
	SingleShift  int
}

// NewGSM7 creates a GSM7 codec with explicitly selected tables. It rejects
// reserved or unknown identifiers and Spanish locking shift (Annex A.3.2 is
// void); it does not select a language or fall back to a different alphabet.
// Its result implements Splitter and exposes GSM7Options() GSM7Options so PDU
// encoding can describe the selected tables in the user data header. Packed
// EncodeSplit uses one fill bit for a six-octet concatenation header, matching
// GSM7BITPACKED; PDU construction handles other complete header alignments.
func NewGSM7(opts GSM7Options) (Encoding, error) {
	if opts.LockingShift < 0 || opts.LockingShift >= charset.End || opts.LockingShift == charset.Spanish {
		return nil, fmt.Errorf("invalid GSM7 locking shift identifier %d", opts.LockingShift)
	}
	if opts.SingleShift < 0 || opts.SingleShift >= charset.End {
		return nil, fmt.Errorf("invalid GSM7 single shift identifier %d", opts.SingleShift)
	}
	return &configuredGSM7{
		options: opts,
		encoder: gsm7.NewEncoder(gsm7.WithCharset(opts.LockingShift), gsm7.WithExtCharset(opts.SingleShift)),
		decoder: gsm7.NewDecoder(gsm7.WithCharset(opts.LockingShift), gsm7.WithExtCharset(opts.SingleShift)),
	}, nil
}

type configuredGSM7 struct {
	options GSM7Options
	encoder gsm7.Encoder
	decoder gsm7.Decoder
}

// GSM7Options returns a copy of the codec's explicit table and packing options.
func (c *configuredGSM7) GSM7Options() GSM7Options { return c.options }

func (*configuredGSM7) DataCoding() byte { return GSM7BITCoding }

func (c *configuredGSM7) Encode(text string) ([]byte, error) {
	septets, err := c.encoder.Encode([]byte(text))
	if err != nil {
		return nil, err
	}
	if len(septets) == 0 {
		return []byte{}, nil
	}
	if c.options.Packed {
		return gsm7.Pack7BitCR(septets, 0)
	}
	return septets, nil
}

func (c *configuredGSM7) Decode(src []byte) (string, error) {
	if c.options.Packed {
		septets, err := unpackGSM7(src, 0, -1)
		if err != nil {
			return "", err
		}
		src = septets
	}
	text, err := c.decoder.Decode(src)
	return string(text), err
}

func (c *configuredGSM7) ShouldSplit(text string, octetLimit uint) bool {
	encoded, err := c.Encode(text)
	return err == nil && uint(len(encoded)) > octetLimit
}

func (c *configuredGSM7) EncodeSplit(text string, octetLimit uint) ([][]byte, error) {
	if octetLimit == 0 {
		return nil, fmt.Errorf("split limit must be positive")
	}
	septets, err := c.encoder.Encode([]byte(text))
	if err != nil {
		return nil, err
	}
	parts := [][]byte{}
	limit := int(min(octetLimit, uint(len(septets))))
	if c.options.Packed {
		// As with GSM7BITPACKED.EncodeSplit, one fill bit follows a six-octet
		// concatenation UDH (TS 23.040 section 9.2.3.24).
		limit = int((min(octetLimit, uint(len(septets)+1))*8 - 1) / 7)
	}
	for start := 0; start < len(septets); {
		end := min(start+limit, len(septets))
		// TS 23.040 section 9.2.3.24.1 forbids splitting an escape sequence.
		if end > start && septets[end-1] == escapeSequence {
			end--
		}
		if end == start {
			return nil, fmt.Errorf("split limit cannot fit a GSM character")
		}
		segment := septets[start:end:end]
		if c.options.Packed {
			segment, err = gsm7.Pack7BitCR(segment, 1)
			if err != nil {
				return nil, err
			}
			if uint(len(segment)) > octetLimit {
				end--
				if end > start && septets[end-1] == escapeSequence {
					end--
				}
				if end == start {
					return nil, fmt.Errorf("split limit cannot fit a padded GSM character")
				}
				segment, err = gsm7.Pack7BitCR(septets[start:end], 1)
				if err != nil {
					return nil, err
				}
			}
		}
		parts = append(parts, segment)
		start = end
	}
	return parts, nil
}
