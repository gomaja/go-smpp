package data

import (
	"bytes"
	"testing"
)

func TestEncodedSplitCompatibility(t *testing.T) {
	nli, err := NewGSM7(GSM7Options{LockingShift: 1, SingleShift: 2})
	if err != nil {
		t.Fatal(err)
	}
	for _, enc := range []Encoding{GSM7BIT, nli, UCS2, UCS2Strict, ASCII, LATIN1, CYRILLIC, HEBREW} {
		splitter := enc.(Splitter)
		for _, text := range []string{"", "A", string([]byte{0xff})} {
			parts, err := splitter.EncodeSplit(text, 0)
			if parts != nil || err == nil || err.Error() != "split limit must be positive" {
				t.Fatalf("%T zero limit: parts=%v err=%v", enc, parts, err)
			}
		}
		for _, limit := range []uint{1<<31 - 1, 1 << 31, ^uint(0) >> 1, ^uint(0)} {
			parts, err := splitter.EncodeSplit("AB", limit)
			encoded, encodeErr := enc.Encode("AB")
			if encodeErr != nil || err != nil || len(parts) != 1 || !bytes.Equal(parts[0], encoded) {
				t.Fatalf("%T limit %d: parts=%x err=%v", enc, limit, parts, err)
			}
		}
		parts, err := splitter.EncodeSplit("", 1)
		wantNonNil := enc == GSM7BIT || enc == nli
		if err != nil || len(parts) != 0 || (parts != nil) != wantNonNil {
			t.Fatalf("%T empty result: %#v, %v", enc, parts, err)
		}
	}
	for _, tc := range []struct {
		enc   Encoding
		text  string
		limit uint
		want  string
	}{
		{GSM7BIT, "A€B", 1, "split limit cannot fit a GSM character"},
		{UCS2, "A😀B", 3, "split limit cannot fit a UTF-16 character"},
		{UCS2Strict, "AB", 1, "split limit cannot fit a UTF-16 character"},
	} {
		parts, err := tc.enc.(Splitter).EncodeSplit(tc.text, tc.limit)
		if parts != nil || err == nil || err.Error() != tc.want {
			t.Fatalf("%T: parts=%x err=%v want=%q", tc.enc, parts, err, tc.want)
		}
	}
}
