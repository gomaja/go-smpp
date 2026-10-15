package data

import (
	"unicode"

	"github.com/gomaja/go-sms/encoding/gsm7"
)

// FindEncoding returns GSM7BIT when s can be encoded using the GSM 7-bit
// default alphabet and default extension table (TS 23.038 §§6.2.1–6.2.1.1),
// and UCS2 otherwise, including invalid UTF-8. Extension characters take two
// septets. National language tables are not considered: this helper returns
// only an Encoding and cannot supply the required language IEs.
func FindEncoding(s string) Encoding {
	if _, err := gsm7.Encode([]byte(s)); err == nil {
		return GSM7BIT
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
