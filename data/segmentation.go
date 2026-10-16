package data

import (
	"errors"
	"fmt"

	"github.com/gomaja/go-sms/encoding/tpdu"
)

// Encode with the selected codec before splitting, preserving full-input
// conversion errors and strict UCS2 validation. Alpha8Bit only selects octet
// boundaries for the single-octet text alphabets; it does not select a codec.
func encodeSplit(codec EncDec, text string, limit uint, alpha tpdu.Alphabet) ([][]byte, error) {
	if limit == 0 {
		return nil, fmt.Errorf("split limit must be positive")
	}
	encoded, err := codec.Encode(text)
	if err != nil || len(encoded) == 0 {
		return nil, err
	}
	// TS 23.040 §§9.2.3.24.1, 9.2.3.24.8; TS 23.038 §§6.2.1.1, 6.2.3:
	// go-sms keeps escape sequences and UTF-16 pairs within a chunk.
	// Cap before converting so even MaxUint cannot wrap to a negative int.
	parts, err := tpdu.SplitUserData(encoded, alpha, int(min(limit, uint(len(encoded)))))
	if errors.Is(err, tpdu.ErrOverlength) {
		switch alpha {
		case tpdu.Alpha7Bit:
			err = fmt.Errorf("split limit cannot fit a GSM character")
		case tpdu.AlphaUCS2:
			err = fmt.Errorf("split limit cannot fit a UTF-16 character")
		}
	}
	return parts, err
}
