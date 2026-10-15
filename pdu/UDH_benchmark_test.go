package pdu

import "testing"

func BenchmarkUDHUnmarshalBinary(b *testing.B) {
	raw := []byte{5, 0, 3, 7, 2, 1}
	var u UDH
	b.ReportAllocs()
	for b.Loop() {
		if _, err := u.UnmarshalBinary(raw); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkUDHMarshalBinary(b *testing.B) {
	u := UDH{NewIEConcatMessage(2, 1, 7)}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := u.MarshalBinary(); err != nil {
			b.Fatal(err)
		}
	}
}
