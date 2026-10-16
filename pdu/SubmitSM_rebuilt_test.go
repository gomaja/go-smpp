package pdu

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/gomaja/go-smpp/data"
	"github.com/stretchr/testify/require"
)

func TestSubmitSMSplitRebuiltSinglePart(t *testing.T) {
	for _, tc := range []struct {
		name   string
		header UDH
		first  int
		last   int
		wide   bool
	}{
		{"wide-narrow", UDH{NewIEConcatMessage16Bit(9, 7, 0xbeef), NewIEConcatMessage(8, 6, 0xaa)}, 129, 134, false},
		{"narrow-wide", UDH{NewIEConcatMessage(8, 6, 0xaa), NewIEConcatMessage16Bit(9, 7, 0xbeef)}, 129, 133, true},
		{"narrow-narrow", UDH{NewIEConcatMessage(9, 7, 0xbb), NewIEConcatMessage(8, 6, 0xaa)}, 130, 134, false},
	} {
		for _, ports := range []bool{false, true} {
			first, last := tc.first, tc.last
			header := append(UDH(nil), tc.header...)
			var other UDH
			if ports {
				other = UDH{{ID: 5, Data: []byte{0x15, 0x8a, 0x15, 0x8b}}}
				header = append(header, other...)
				first, last = first-6, last-6
			}
			for size := first - 1; size <= last+1; size++ {
				t.Run(fmt.Sprintf("%s/ports=%t/%d", tc.name, ports, size), func(t *testing.T) {
					s := NewSubmitSM().(*SubmitSM)
					s.SequenceNumber = 0x7fffffff
					s.ServiceType, s.EsmClass, s.ProtocolID = "test", 0x83, 7
					s.PriorityFlag, s.RegisteredDelivery = 1, 1
					s.Message.SmDefaultMsgID = 3
					require.NoError(t, s.Message.SetUDH(header))
					input := bytes.Repeat([]byte{0x80}, size)
					require.NoError(t, s.Message.SetLongMessageDataWithEncoding(input, data.BINARY8BIT2))
					kept := []Field{
						NewFieldUint16(TagUserMessageReference, 0x1234),
						NewField(TagMessagePayload, []byte{0x42}),
						NewField(Tag(0x7777), []byte{9, 8}),
					}
					for _, field := range kept {
						s.AppendOptionalParam(field)
					}
					s.AppendOptionalParam(NewFieldUint16(TagSarMsgRefNum, 0x9876))
					s.AppendOptionalParam(NewField(TagSarTotalSegments, []byte{9}))
					s.AppendOptionalParam(NewField(TagSarSegmentSeqnum, []byte{7}))
					before := *s
					before.Message.messageData = bytes.Clone(input)
					before.Message.udHeader = make(UDH, len(header))
					for i, ie := range header {
						before.Message.udHeader[i] = InfoElement{ID: ie.ID, Data: bytes.Clone(ie.Data)}
					}
					before.OptionalParameters = make([]Field, len(s.OptionalParameters))
					for i, field := range s.OptionalParameters {
						before.OptionalParameters[i] = Field{Tag: field.Tag, Data: bytes.Clone(field.Data)}
					}

					parts, err := s.Split()
					require.NoError(t, err)
					require.Equal(t, before, *s, "Split must leave the source unchanged")
					if size < first {
						require.Len(t, parts, 1)
						require.Same(t, s, parts[0], "the fitting source remains the fast path")
						require.False(t, s.ShouldSplit())
						return
					}
					count := 1
					if size > last {
						count = 2
					}
					require.Len(t, parts, count)
					seen := map[int32]bool{s.SequenceNumber: true}
					var joined []byte
					for i, part := range parts {
						require.NotSame(t, s, part, "a rebuilt message needs a new PDU even when it is the only part")
						require.False(t, seen[part.SequenceNumber])
						require.Positive(t, part.SequenceNumber)
						seen[part.SequenceNumber] = true
						require.Equal(t, byte(0xc3), part.EsmClass)
						require.Equal(t, s.ServiceType, part.ServiceType)
						require.Equal(t, s.ProtocolID, part.ProtocolID)
						require.Equal(t, s.PriorityFlag, part.PriorityFlag)
						require.Equal(t, s.RegisteredDelivery, part.RegisteredDelivery)
						require.Equal(t, byte(3), part.Message.SmDefaultMsgID)
						require.Equal(t, kept, part.OptionalParameters)
						require.False(t, part.ShouldSplit())
						require.LessOrEqual(t, part.Message.headerLength()+len(part.Message.messageData), 140)
						total, seq, reference, found := part.Message.UDH().GetConcatInfo()
						require.True(t, found)
						require.Equal(t, byte(count), total)
						require.Equal(t, byte(i+1), seq)
						concat := NewIEConcatMessage(byte(count), byte(i+1), byte(reference))
						if tc.wide {
							concat = NewIEConcatMessage16Bit(byte(count), byte(i+1), reference)
						}
						require.Equal(t, append(UDH{concat}, other...), part.Message.UDH())
						joined = append(joined, part.Message.messageData...)
						b := NewBuffer(nil)
						part.Marshal(b)
						require.NoError(t, b.Err())
						parsed, err := Parse(bytes.NewReader(b.Bytes()))
						require.NoError(t, err)
						got := parsed.(*SubmitSM)
						require.Equal(t, part.EsmClass, got.EsmClass)
						require.Equal(t, part.SequenceNumber, got.SequenceNumber)
						require.Equal(t, part.Message.UDH(), got.Message.UDH())
						require.Equal(t, part.Message.messageData, got.Message.messageData)
						require.Equal(t, kept, got.OptionalParameters)
					}
					require.Equal(t, input, joined)
					require.Equal(t, before, *s, "marshaling rebuilt parts must not affect the source")
					parts[0].OptionalParameters[0].Data[0] = 0xff
					parts[0].Message.messageData[0] = 0x42
					require.Equal(t, before, *s, "rebuilt payload and TLVs must be copied")
				})
			}
		}
	}
}
