package pdu

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

// Independent wire fixtures shared by the parse benchmarks and allocation regression.

func benchmarkFrame(id uint32, body []byte) []byte {
	w := make([]byte, 16+len(body))
	binary.BigEndian.PutUint32(w[0:], uint32(len(w)))
	binary.BigEndian.PutUint32(w[4:], id)
	binary.BigEndian.PutUint32(w[12:], 7)
	copy(w[16:], body)
	return w
}

func benchmarkTLV(tag uint16, value []byte) []byte {
	out := make([]byte, 4+len(value))
	binary.BigEndian.PutUint16(out[0:], tag)
	binary.BigEndian.PutUint16(out[2:], uint16(len(value)))
	copy(out[4:], value)
	return out
}

// submit_sm, 153-octet GSM segment, SAR + user_message_reference TLVs.
func benchmarkSubmitSM() []byte {
	var body bytes.Buffer
	body.WriteString("\x00")                     // service_type
	body.WriteString("\x05\x00SENDER\x00")       // source ton/npi/addr
	body.WriteString("\x01\x01447700900123\x00") // dest ton/npi/addr
	body.WriteString("\x40\x00\x00")             // esm_class(UDHI), protocol_id, priority
	body.WriteString("\x00\x00")                 // schedule, validity
	body.WriteString("\x01\x00\x00\x00")         // registered, replace, data_coding, default_msg_id
	msg := append([]byte{0x05, 0x00, 0x03, 0x2a, 0x02, 0x01}, []byte(strings.Repeat("a", 147))...)
	body.WriteByte(byte(len(msg)))
	body.Write(msg)
	body.Write(benchmarkTLV(0x0204, []byte{0x12, 0x34}))
	body.Write(benchmarkTLV(0x020c, []byte{0x00, 0x2a}))
	body.Write(benchmarkTLV(0x020e, []byte{0x02}))
	body.Write(benchmarkTLV(0x020f, []byte{0x01}))
	return benchmarkFrame(0x00000004, body.Bytes())
}

// deliver_sm delivery receipt with receipted_message_id, message_state, network_error_code.
func benchmarkDeliverSM() []byte {
	var body bytes.Buffer
	body.WriteString("\x00")
	body.WriteString("\x01\x01447700900123\x00")
	body.WriteString("\x05\x00SENDER\x00")
	body.WriteString("\x04\x00\x00")
	body.WriteString("\x00\x00")
	body.WriteString("\x00\x00\x00\x00")
	receipt := "id:0123456789 sub:001 dlvrd:001 submit date:2610101200 done date:2610101201 stat:DELIVRD err:000 text:hello"
	body.WriteByte(byte(len(receipt)))
	body.WriteString(receipt)
	body.Write(benchmarkTLV(0x001e, []byte("0123456789\x00")))
	body.Write(benchmarkTLV(0x0427, []byte{0x02}))
	body.Write(benchmarkTLV(0x0423, []byte{0x03, 0x00, 0x00}))
	return benchmarkFrame(0x00000005, body.Bytes())
}

func benchmarkBenchParse(b *testing.B, wire []byte) {
	r := bytes.NewReader(wire)
	b.ReportAllocs()
	b.SetBytes(int64(len(wire)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.Reset(wire)
		p, err := Parse(r)
		if err != nil || p == nil {
			b.Fatal(err)
		}
	}
}

func benchmarkParseFrames() []struct {
	name string
	wire []byte
} {
	return []struct {
		name string
		wire []byte
	}{
		{"submit_sm", benchmarkSubmitSM()},
		{"deliver_sm", benchmarkDeliverSM()},
		{"deliver_sm_udh", udhFrame(0x00000005, []byte{5, 0, 3, 7, 2, 1, 'A'}, true)},
		{"enquire_link", benchmarkFrame(0x15, nil)},
		{"submit_sm_resp", benchmarkFrame(0x80000004, []byte("0123456789\x00"))},
		{"submit_multi_resp", benchmarkFrame(0x80000021, []byte("mid\x00\x02\x01\x01a\x00\x00\x00\x00\x0b\x02\x09b\x00\x80\x00\x00\x01"))},
	}
}

func BenchmarkParse(b *testing.B) {
	for _, tc := range benchmarkParseFrames() {
		b.Run(tc.name, func(b *testing.B) { benchmarkBenchParse(b, tc.wire) })
	}
}

// Budgets measured with these independent fixtures. The UDH deliver_sm budget
// includes copied IE data and conversion to the public header type.
func TestParseAllocationBudget(t *testing.T) {
	budgets := map[string]float64{
		"submit_sm": 28, "deliver_sm": 26, "deliver_sm_udh": 20, "enquire_link": 9,
		"submit_sm_resp": 12, "submit_multi_resp": 18,
	}
	for _, tc := range benchmarkParseFrames() {
		t.Run(tc.name, func(t *testing.T) {
			r := bytes.NewReader(tc.wire)
			allocs := testing.AllocsPerRun(100, func() {
				r.Reset(tc.wire)
				if _, err := Parse(r); err != nil {
					t.Fatal(err)
				}
			})
			if allocs > budgets[tc.name] {
				t.Errorf("Parse allocated %.0f times; budget is %.0f", allocs, budgets[tc.name])
			}
		})
	}
}
