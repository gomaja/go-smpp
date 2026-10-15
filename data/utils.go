package data

import (
	"unicode"

	"github.com/gomaja/go-sms/encoding/gsm7"
)

// FindEncoding returns GSM7BIT when s can be encoded using the GSM 7-bit
// default alphabet and default extension table (TS 23.038 §§6.2.1–6.2.1.1),
// and UCS2 otherwise, including invalid UTF-8. Extension characters take two
// septets. National language tables are not considered: this helper returns
// only an Encoding and cannot supply the required language IEs. If GSM7BIT
// has been replaced, that codec must also accept s; otherwise UCS2 is returned.
func FindEncoding(s string) Encoding {
	if _, err := gsm7.Encode([]byte(s)); err == nil {
		candidate := GSM7BIT
		if _, err := candidate.Encode(s); err == nil {
			return candidate
		}
	}
	return UCS2
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] > unicode.MaxASCII {
			return false
		}
	}
	return true
}
