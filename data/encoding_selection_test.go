package data_test

import (
	"fmt"
	"testing"

	"github.com/gomaja/go-smpp/data"
	"github.com/stretchr/testify/require"
)

// TS 23.038 §§6.2.1–6.2.1.1: the literal alphabet in adoption_test.go is
// ordered by septet value. ESC is an introducer, not an encodable character.
func TestFindEncodingEntireDefaultRepertoire(t *testing.T) {
	table := []rune(adoptionDefaultAlphabet)
	require.Len(t, table, 128)
	accepted := make(map[rune]bool)
	check := func(r rune, wire []byte) {
		t.Helper()
		accepted[r] = true
		t.Run(fmt.Sprintf("%U", r), func(t *testing.T) {
			text := string(r)
			enc := data.FindEncoding(text)
			require.Equal(t, data.GSM7BIT, enc)
			got, err := enc.Encode(text)
			require.NoError(t, err)
			require.Equal(t, wire, got)

			// Check the same cost and wire value within ordinary text.
			mixed := "A" + text + "Z"
			enc = data.FindEncoding(mixed)
			require.Equal(t, data.GSM7BIT, enc)
			got, err = enc.Encode(mixed)
			require.NoError(t, err)
			want := append([]byte{0x41}, wire...)
			want = append(want, 0x5a)
			require.Equal(t, want, got)
		})
	}
	for code, r := range table {
		if code != 0x1b {
			check(r, []byte{byte(code)})
		}
	}
	for _, extension := range []struct {
		r    rune
		code byte
	}{{'\f', 0x0a}, {'^', 0x14}, {'{', 0x28}, {'}', 0x29}, {'\\', 0x2f}, {'[', 0x3c}, {'~', 0x3d}, {']', 0x3e}, {'|', 0x40}, {'€', 0x65}} {
		check(extension.r, []byte{0x1b, extension.code})
	}
	require.Len(t, accepted, 137)
	// Every ASCII character outside the literal tables must select UCS2.
	for r := rune(0); r <= 0x7f; r++ {
		if !accepted[r] {
			t.Run(fmt.Sprintf("UCS2_%U", r), func(t *testing.T) {
				require.Equal(t, data.UCS2, data.FindEncoding(string(r)))
			})
		}
	}
	for _, text := range []string{"漢字", "😀", "ı", "Ğ", "\ufffd", "\xff", "\xc3", "A\xff", "\xc0\xaf", "\xed\xa0\x80", "\xf4\x90\x80\x80"} {
		t.Run(fmt.Sprintf("UCS2_%q", text), func(t *testing.T) {
			require.Equal(t, data.UCS2, data.FindEncoding(text))
		})
	}
	require.Equal(t, data.GSM7BIT, data.FindEncoding(""))
}
