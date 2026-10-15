package data

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFindEncoding(t *testing.T) {
	require.Equal(t, GSM7BIT, FindEncoding("abc30hb3bk2lopzSD=2-^"))
	require.Equal(t, UCS2, FindEncoding("Trần Lập và ban nhạc Bức tường huyền thoại"))
	require.Equal(t, UCS2, FindEncoding("Đừng buồn thế dù ngoài kia vẫn mưa nghiễng rợi tý tỵ"))
}

// TS 23.038 §§6.2.1–6.2.1.1: selection uses only the default tables.
func TestFindEncodingDefaultAlphabet(t *testing.T) {
	for _, text := range []string{"£", "é", "Δ", "€", "[", "]", "{", "}", "~", "^", "|", "\\", "\f", "\r", "\n", "plain ASCII 123", ""} {
		t.Run(fmt.Sprintf("GSM7_%q", text), func(t *testing.T) {
			require.Equal(t, GSM7BIT, FindEncoding(text))
			encoded, err := FindEncoding(text).Encode(text)
			require.NoError(t, err)
			wantSeptets := len([]rune(text))
			if strings.ContainsAny(text, "€[]{}~^|\\\f") {
				wantSeptets = 2
			}
			require.Len(t, encoded, wantSeptets)
		})
	}
	for _, text := range []string{"`", "\x1b", "漢字", "😀", "ı", "Ğ", "\xff", "\xc3", "A\xff", "\xc0\xaf", "\xed\xa0\x80"} {
		t.Run(fmt.Sprintf("UCS2_%q", text), func(t *testing.T) {
			require.Equal(t, UCS2, FindEncoding(text))
		})
	}
	for control := rune(0); control <= 0x7f; control++ {
		if control >= 0x20 && control != 0x7f || control == '\n' || control == '\r' || control == '\f' {
			continue
		}
		t.Run(fmt.Sprintf("control_%02x", control), func(t *testing.T) {
			require.Equal(t, UCS2, FindEncoding(string(control)))
		})
	}
}
