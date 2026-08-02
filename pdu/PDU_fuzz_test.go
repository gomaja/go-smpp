package pdu

import (
	"bytes"
	"encoding/hex"
	"testing"
)

func FuzzParse(f *testing.F) {
	for _, seed := range []string{
		"",
		"0000000f800000060000000000000001",
		"00000010000000150000000000000001",
		"00000010800000150000000000000001",
		"00000010800000010000000d00000004",
		"00000010800000040000005800000001",
		"000000100000ffff0000000000000001",
	} {
		raw, err := hex.DecodeString(seed)
		if err != nil {
			f.Fatalf("invalid fuzz seed %q: %v", seed, err)
		}
		f.Add(raw)
	}

	f.Fuzz(func(t *testing.T, raw []byte) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("Parse panicked for %x: %v", raw, r)
			}
		}()

		parsed, err := Parse(bytes.NewReader(raw))
		if err != nil || parsed == nil {
			return
		}

		buf := NewBuffer(nil)
		parsed.Marshal(buf)
		reparsed, err := Parse(bytes.NewReader(buf.Bytes()))
		if err != nil {
			t.Fatalf("reparse of marshaled %T failed: %v", parsed, err)
		}
		if reparsed == nil {
			t.Fatalf("reparse of marshaled %T returned nil PDU", parsed)
		}
	})
}
