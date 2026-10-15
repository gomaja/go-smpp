package pdu

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/gomaja/go-smpp/data"
	"github.com/gomaja/go-smpp/errors"
	"github.com/gomaja/go-sms/encoding/gsm7"
	smsucs2 "github.com/gomaja/go-sms/encoding/ucs2"

	"github.com/stretchr/testify/require"
)

func TestSplitUsesCurrentEncodedBytes(t *testing.T) {
	s := NewSubmitSM().(*SubmitSM)
	require.NoError(t, s.Message.SetLongMessageWithEnc(strings.Repeat("A", 161), data.GSM7BIT))
	s.Message.messageData[0] = 'B'
	parts, err := s.Split()
	require.NoError(t, err)
	require.Equal(t, byte('B'), parts[0].Message.messageData[0], "cached text must not replace existing bytes")
}

func TestSplitRejectsLossyReencoding(t *testing.T) {
	s := NewSubmitSM().(*SubmitSM)
	payload := append(bytes.Repeat([]byte{0, 'A'}, 70), 0)
	require.NoError(t, s.Message.SetMessageDataWithEncoding(payload, data.UCS2))
	_, err := s.Split()
	require.Error(t, err, "incomplete UTF16 must not turn into a replacement character")
	require.Equal(t, payload, s.Message.messageData)
}

func TestLongMessagePackedSeptetBudgets(t *testing.T) {
	for _, text := range []string{strings.Repeat("A", 160), strings.Repeat("A", 161), strings.Repeat("A", 152) + "^" + strings.Repeat("B", 7), strings.Repeat("@", 161)} {
		parts, err := NewLongMessageWithEncoding(text, data.GSM7BITPACKED)
		require.NoError(t, err)
		var joined string
		for _, p := range parts {
			require.LessOrEqual(t, len(p.messageData)+p.UDH().UDHL(), 140)
			wire := NewBuffer(nil)
			request := NewSubmitSM().(*SubmitSM)
			request.Message = *p
			if p.UDH().UDHL() > 0 {
				request.EsmClass = data.SM_UDH_GSM
			}
			request.Marshal(wire)
			require.NoError(t, wire.Err())
			parsed, err := Parse(bytes.NewReader(wire.Bytes()))
			require.NoError(t, err)
			got, err := parsed.(*SubmitSM).Message.GetMessageWithEncoding(data.GSM7BITPACKED)
			require.NoError(t, err)
			var septets []byte
			for _, r := range got {
				switch r {
				case 'A':
					septets = append(septets, 0x41)
				case 'B':
					septets = append(septets, 0x42)
				case '@':
					septets = append(septets, 0)
				case '^':
					septets = append(septets, 0x1b, 0x14)
				default:
					t.Fatalf("unexpected fixture rune %q", r)
				}
			}
			b := NewBuffer(nil)
			require.NoError(t, p.Marshal(b))
			// TS 23.040 §9.2.3.24: start after the next septet boundary.
			startBit := ((p.UDH().UDHL()*8 + 6) / 7) * 7
			for i, want := range septets {
				var value byte
				for k := 0; k < 7; k++ {
					bit := startBit + i*7 + k
					value |= ((b.Bytes()[3+bit/8] >> (bit % 8)) & 1) << k
				}
				require.Equal(t, want, value, "packed septet %d", i)
			}
			joined += got
		}
		require.Equal(t, text, joined, "packed fill bits must not become text")
	}
}

func TestPackedSetUDHPreservesRawPayload(t *testing.T) {
	udh := UDH{NewIEConcatMessage(2, 1, 1)}
	for _, count := range []int{7, 15} {
		payload, err := encodeTextWithUDH(strings.Repeat("A", count), data.GSM7BITPACKED, udh)
		require.NoError(t, err)
		s, err := NewBinaryShortMessageWithEncoding(payload, data.GSM7BITPACKED)
		require.NoError(t, err)
		s.udHeader = udh
		require.NoError(t, s.SetUDH(udh))
		require.Equal(t, payload, s.messageData, "identical header must preserve packed wire bytes")
		require.Error(t, s.SetUDH(UDH{NewIEConcatMessage16Bit(2, 1, 1)}), "raw packed alignment cannot guess a septet count")
		require.Equal(t, payload, s.messageData)
		require.Equal(t, udh, s.UDH())
	}
}

func TestShortMessageSettersValidateFullLength(t *testing.T) {
	s, err := NewBinaryShortMessage(make([]byte, 248))
	require.NoError(t, err)
	udh := UDH{NewIEConcatMessage(2, 1, 1)}
	require.NoError(t, s.SetUDH(udh))
	require.Error(t, s.SetMessageDataWithEncoding(make([]byte, 249), data.ASCII))
	require.Equal(t, data.BINARY8BIT2, s.Encoding())
	require.Len(t, s.messageData, 248)
	require.Error(t, s.SetMessageWithEncoding(strings.Repeat("A", 249), data.ASCII))
	require.Len(t, s.messageData, 248)
	plain, err := NewBinaryShortMessage(make([]byte, 254))
	require.NoError(t, err)
	require.Error(t, plain.SetUDH(udh))
	require.Empty(t, plain.UDH())
	b := NewBuffer(nil)
	require.NoError(t, plain.Marshal(b))
	require.NoError(t, b.Err())
	require.Equal(t, byte(254), b.Bytes()[2])
	_, err = NewBinaryShortMessage(make([]byte, 255))
	require.Error(t, err)
}

func TestShortMessageAcceptsWireLength255(t *testing.T) {
	for _, cmd := range []data.CommandIDType{data.SUBMIT_SM, data.SUBMIT_MULTI, data.DELIVER_SM, data.REPLACE_SM} {
		for _, udhi := range []bool{false, true} {
			if udhi && cmd == data.REPLACE_SM {
				continue
			}
			for _, n := range []int{254, 255} {
				p, err := CreatePDUFromCmdID(cmd)
				require.NoError(t, err)
				b := NewBuffer(nil)
				p.Marshal(b)
				require.NoError(t, b.Err())
				body := append([]byte(nil), b.Bytes()[16:]...)
				body[len(body)-1] = byte(n)
				payload := bytes.Repeat([]byte{'A'}, n)
				if udhi {
					offset := 7
					if cmd == data.SUBMIT_MULTI {
						offset = 5
					}
					body[offset] = data.SM_UDH_GSM
					payload = append([]byte{5, 0, 3, 9, 2, 1}, bytes.Repeat([]byte{'A'}, n-6)...)
				}
				wire := decodeTestFrame(cmd, append(body, payload...))
				parsed, err := Parse(bytes.NewReader(wire))
				require.NoError(t, err)
				require.Equal(t, n == 255, Validate(parsed) != nil)
				var message ShortMessage
				switch v := parsed.(type) {
				case *SubmitSM:
					message = v.Message
				case *SubmitMulti:
					message = v.Message
				case *DeliverSM:
					message = v.Message
				case *ReplaceSM:
					message = v.Message
				}
				got, err := message.GetMessageData()
				require.NoError(t, err)
				if udhi {
					require.Equal(t, payload[6:], got)
					total, part, ref, ok := message.UDH().GetConcatInfo()
					require.True(t, ok)
					require.EqualValues(t, 2, total)
					require.EqualValues(t, 1, part)
					require.EqualValues(t, 9, ref)
				} else {
					require.Equal(t, payload, got)
				}

				round := NewBuffer(nil)
				parsed.Marshal(round)

				require.NoError(t, round.Err())
				require.Equal(t, wire, round.Bytes())
			}
		}
	}
}

func TestShortMessageSetterFailurePreservesState(t *testing.T) {
	s, err := NewShortMessage("ok")
	require.NoError(t, err)
	s.SmDefaultMsgID = 3
	_ = s.SetUDH(UDH{NewIEConcatMessage(2, 1, 9)})
	before := NewBuffer(nil)
	_ = s.Marshal(before)
	for _, text := range []string{strings.Repeat("A", 300), "😀"} {
		require.Error(t, s.SetMessageWithEncoding(text, data.GSM7BIT))
		after := NewBuffer(nil)
		_ = s.Marshal(after)
		require.Equal(t, before.Bytes(), after.Bytes(), "rejected setter changed wire content")
		require.Equal(t, data.GSM7BIT, s.Encoding())
	}
}

func TestShortMessageFullEncodedLength(t *testing.T) {
	for _, n := range []int{248, 249, 250, 254, 255} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			s := ShortMessage{enc: data.BINARY8BIT2, messageData: make([]byte, n), udHeader: UDH{NewIEConcatMessage(2, 1, 1)}}
			b := NewBuffer(nil)
			_ = s.Marshal(b)
			if n+6 > 255 {
				require.Zero(t, b.Len(), "invalid full encoded length %d must emit no bytes", n+6)
			} else {
				require.Equal(t, n+6, int(b.Bytes()[2]))
				require.Len(t, b.Bytes(), n+9)
			}
		})
	}
}

func TestLongMessageSegmentCountLimit(t *testing.T) {
	parts, err := NewLongMessageWithEncoding(strings.Repeat("A", 134*255), data.LATIN1)
	require.NoError(t, err)
	require.Len(t, parts, 255)
	total, part, _, ok := parts[254].UDH().GetConcatInfo()
	require.True(t, ok)
	require.Equal(t, byte(255), total)
	require.Equal(t, byte(255), part)
	_, err = NewLongMessageWithEncoding(strings.Repeat("A", 134*255+1), data.LATIN1)
	require.Error(t, err, "256 segments cannot fit concatenation count")
}

func TestLongMessageUnpackedSeptetBudgets(t *testing.T) {
	for _, tc := range []struct {
		text  string
		sizes []int
	}{
		{strings.Repeat("A", 160), []int{160}},
		{strings.Repeat("A", 161), []int{153, 8}},
		{strings.Repeat("A", 300), []int{153, 147}},
		{strings.Repeat("^", 80), []int{160}},
		{strings.Repeat("A", 152) + "^" + strings.Repeat("B", 7), []int{152, 9}},
	} {
		parts, err := NewLongMessage(tc.text)
		require.NoError(t, err)
		require.Len(t, parts, len(tc.sizes), "wrong septet budget")
		var joined string
		for i, p := range parts {
			require.Len(t, p.messageData, tc.sizes[i])
			text, err := p.GetMessage()
			require.NoError(t, err)
			joined += text
		}
		require.Equal(t, tc.text, joined)
	}
}

func TestLongMessageUTF16OctetBudgets(t *testing.T) {
	for _, text := range []string{strings.Repeat("😀", 40), strings.Repeat("😀", 71), strings.Repeat("A", 66) + "😀" + strings.Repeat("B", 4)} {
		parts, err := NewLongMessageWithEncoding(text, data.UCS2)
		require.NoError(t, err)
		var joined string
		for _, p := range parts {
			require.LessOrEqual(t, len(p.messageData)+p.UDH().UDHL(), 140, "UTF16 encoded octet budget")
			decoded, err := p.GetMessage()
			require.NoError(t, err)
			joined += decoded
		}
		require.Equal(t, text, joined)
	}
}

func TestSplitExistingEncodedMessage(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload []byte
		enc     data.Encoding
	}{
		{"parsed", []byte("hello"), data.GSM7BIT},
		{"binary", []byte{1, 2, 3}, data.BINARY8BIT2},
		{"binaryLong", bytes.Repeat([]byte{0xa5}, 141), data.BINARY8BIT2},
		{"parsedLong", bytes.Repeat([]byte{'A'}, 161), data.GSM7BIT},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewSubmitSM().(*SubmitSM)
			var err error
			s.Message, err = NewBinaryShortMessageWithEncoding(tc.payload, tc.enc)
			require.NoError(t, err)
			b := NewBuffer(nil)
			s.Marshal(b)
			parsed, err := Parse(bytes.NewReader(b.Bytes()))
			require.NoError(t, err)
			parts, err := parsed.(*SubmitSM).Split()
			require.NoError(t, err)
			var joined []byte
			for _, p := range parts {
				joined = append(joined, p.Message.messageData...)
			}
			require.Equal(t, tc.payload, joined, "Split must preserve encoded content")
		})
	}
}

type customEncoder struct{}

func (*customEncoder) Encode(str string) ([]byte, error) {
	return []byte(str), nil
}

func (*customEncoder) Decode(data []byte) (string, error) {
	return string(data), nil
}

// TS 23.038 §6.2.3; TS 23.040 §§9.2.3.24.1, 9.2.3.24.8: decode
// complete 16-bit units, but never carry a final octet into another segment.
func TestShortMessageOddUCS2(t *testing.T) {
	for _, coding := range []struct {
		name string
		enc  data.Encoding
	}{{"UCS2", data.UCS2}, {"UCS2Strict", data.UCS2Strict}} {
		for _, header := range []struct {
			name string
			udh  UDH
		}{
			{"no_UDH", nil},
			{"concat_8bit", UDH{NewIEConcatMessage(2, 1, 7)}},
			{"concat_16bit", UDH{NewIEConcatMessage16Bit(2, 1, 7)}},
			{"national_language", UDH{{ID: 0x25, Data: []byte{1}}}},
		} {
			for _, tc := range []struct {
				name string
				wire []byte
				want string
			}{
				{"prefix", []byte{0, 0x41, 0x6f, 0x22, 0xff}, "A漢"},
				{"high_before_final_octet", []byte{0, 0x41, 0xd8, 0x3d, 0xde}, "A\ufffd"},
			} {
				t.Run(coding.name+"/"+header.name+"/"+tc.name, func(t *testing.T) {
					s, err := NewBinaryShortMessageWithEncoding(tc.wire, coding.enc)
					require.NoError(t, err)
					require.NoError(t, s.SetUDH(header.udh))
					for _, getter := range []struct {
						name string
						get  func() (string, error)
					}{
						{"GetMessage", s.GetMessage},
						{"GetMessageWithEncoding", func() (string, error) { return s.GetMessageWithEncoding(coding.enc) }},
					} {
						t.Run(getter.name, func(t *testing.T) {
							text, err := getter.get()
							require.Equal(t, tc.want, text)
							require.ErrorIs(t, err, smsucs2.ErrInvalidLength)
							var dangling smsucs2.ErrDanglingSurrogate
							require.NotErrorAs(t, err, &dangling)
						})
					}
				})
			}
		}
	}
}

func TestShortMessage(t *testing.T) {
	t.Run("invalidCoding", func(t *testing.T) {
		var s ShortMessage
		require.NotNil(t, s.SetMessageWithEncoding("agjwklgjkwPфngưỡng", data.LATIN1))
	})

	t.Run("customCoding", func(t *testing.T) {
		var s ShortMessage

		customCoding := data.NewCustomEncoding(246, &customEncoder{})
		err := s.SetMessageDataWithEncoding([]byte{0x61, 0x62, 0x63}, customCoding) // "abc"
		require.NoError(t, err)
		require.EqualValues(t, 246, s.Encoding().DataCoding())

		m, err := s.GetMessage()
		require.Nil(t, err)
		require.Equal(t, "abc", m)

		// try to get message string with other encoding
		m, err = s.GetMessageWithEncoding(data.FromDataCoding(data.UCS2Coding))
		require.ErrorIs(t, err, smsucs2.ErrInvalidLength)
		require.Equal(t, "\u6162", m) // 61 62 is one big-endian unit; 63 is incomplete.

		// get message string with custom encoding
		m, err = s.GetMessageWithEncoding(customCoding)
		require.Nil(t, err)
		require.Equal(t, "abc", m)
	})

	t.Run("customCodingFromPeer", func(t *testing.T) {
		var senderSM ShortMessage

		// set custom data coding for test
		customCoding := data.NewCustomEncoding(0x19, &customEncoder{})

		err := senderSM.SetMessageDataWithEncoding([]byte{0x61, 0x62, 0x63}, customCoding) // "abc"
		require.NoError(t, err)

		b := NewBuffer(nil)
		_ = senderSM.Marshal(b)

		// From here the message is not know anymore to the receiver in terms of encoding methods, but the receiver wants to know the encoding code once receiving the packet
		var receivedSM ShortMessage
		err = receivedSM.Unmarshal(b, false)
		require.NoError(t, err)

		require.NotNil(t, receivedSM.Encoding())
	})

	t.Run("invalidSize", func(t *testing.T) {
		var s ShortMessage
		require.Equal(t, errors.ErrShortMessageLengthTooLarge,
			s.SetMessageWithEncoding("agjwklgjkwPфngưỡngasdfasdfasdfasdagjwklgjkwPфngưỡngasdfasdfasdfasdagjwklgjkwPфngưỡngasdfasdfasdfasdagjwklgjkwPфngưỡngasdfasdfasdfasd", data.UCS2))
	})

	t.Run("getMessageWithoutCoding", func(t *testing.T) {
		var s ShortMessage
		s.messageData = []byte{0x61, 0x62, 0x63}

		m, err := s.GetMessage()
		require.Nil(t, err)
		require.Equal(t, "abc", m)
	})

	t.Run("getMessageData", func(t *testing.T) {
		s, err := NewBinaryShortMessage([]byte{0x00, 0x01, 0x02, 0x03})
		require.NoError(t, err)

		messageData, err := s.GetMessageData()
		require.NoError(t, err)
		require.Equal(t, "00010203", toHex(messageData))
	})

	t.Run("marshalBinaryMessage", func(t *testing.T) {
		s, err := NewBinaryShortMessage([]byte{0x00, 0x01, 0x02, 0x03, 0x04})
		require.NoError(t, err)

		buf := NewBuffer(nil)
		_ = s.Marshal(buf)

		require.Equal(t, "0400050001020304", toHex(buf.Bytes()))
	})

	t.Run("marshalWithoutCoding", func(t *testing.T) {
		var s ShortMessage
		err := s.SetMessageDataWithEncoding([]byte("abc"), nil)
		require.NoError(t, err)
		s.messageData = append(s.messageData, 0)
		s.enc = nil

		buf := NewBuffer(nil)
		_ = s.Marshal(buf)
		require.Equal(t, "00000461626300", toHex(buf.Bytes()))
	})

	t.Run("marshalWithCoding", func(t *testing.T) {
		s, err := NewShortMessageWithEncoding("abc", data.GSM7BIT)
		require.NoError(t, err)

		buf := NewBuffer(nil)
		_ = s.Marshal(buf)
		require.Equal(t, "000003616263", toHex(buf.Bytes()))
	})

	t.Run("marshalWithCoding160chars", func(t *testing.T) {
		s, err := NewShortMessageWithEncoding("abcabcabcabcabcabcabcabcabcabcabcabcabcabcabcabcabcabcabcabcabcabcabcabcabcabcabcabcabcabcabcabcabcabcabcabcabcab", data.GSM7BIT)
		require.NoError(t, err)

		buf := NewBuffer(nil)
		_ = s.Marshal(buf)

		require.Equal(t, 116, len(buf.Bytes()))
	})

	t.Run("marshalGSM7WithUDHConcat", func(t *testing.T) {
		s, err := NewShortMessageWithEncoding("abc", data.GSM7BIT)
		require.NoError(t, err)
		_ = s.SetUDH(UDH{NewIEConcatMessage(2, 1, 12)})

		buf := NewBuffer(nil)
		_ = s.Marshal(buf)
		require.Equal(t, "0000090500030c0201616263", toHex(buf.Bytes()))
	})

	t.Run("unmarshalBinaryWithUDHConcat", func(t *testing.T) {
		s := &ShortMessage{}

		buf := NewBuffer([]byte{0x04, 0x00, 0x09, 0x05, 0x00, 0x03, 0x0c, 0x02, 0x01, 0x01, 0x02, 0x03})

		// check encoding
		require.NoError(t, s.Unmarshal(buf, true))
		require.Equal(t, data.BINARY8BIT2, s.Encoding())

		// check message
		messageData, err := s.GetMessageData()
		require.NoError(t, err)
		require.Equal(t, []byte{0x01, 0x02, 0x03}, messageData)
	})

	t.Run("unmarshalGSM7WithUDHConcat", func(t *testing.T) {
		s := &ShortMessage{}

		buf := NewBuffer([]byte{0x00, 0x00, 0x09, 0x05, 0x00, 0x03, 0x0c, 0x02, 0x01, 0x61, 0x62, 0x63})

		// check encoding
		require.NoError(t, s.Unmarshal(buf, true))
		require.Equal(t, data.GSM7BIT, s.Encoding())

		// check message
		message, err := s.GetMessageWithEncoding(s.Encoding())
		require.NoError(t, err)
		require.Equal(t, "abc", message)
	})

	t.Run("shortMessageSplitGSM7_169chars", func(t *testing.T) {
		// over gsm7 chars limit ( 169/160 ), split
		sm, err := NewLongMessageWithEncoding("abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyz1234123456789", data.GSM7BIT)
		require.NoError(t, err)

		require.Equal(t, 2, len(sm))
	})

	t.Run("shortMessageSplitGSM7_160chars", func(t *testing.T) {
		// TS 23.038 §6.1.2.1: 160 default-alphabet septets fit one SMS.
		sm, err := NewLongMessageWithEncoding("abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyz1234", data.GSM7BIT)
		require.NoError(t, err)

		require.Equal(t, 1, len(sm))
	})

	t.Run("shortMessageSplitUCS2_89chars", func(t *testing.T) {
		// over UCS2 chars limit (89/67), split
		sm, err := NewLongMessageWithEncoding("biggest gift của Christmas là có nhiều big/challenging/meaningful problems để sấp mặt làm", data.UCS2)
		require.NoError(t, err)

		require.Equal(t, 2, len(sm))
	})

	t.Run("shortMessageSplitUCS2_67chars", func(t *testing.T) {
		// still within UCS2 chars limit (67/67), not split
		sm, err := NewLongMessageWithEncoding("biggest gift của Christmas là có nhiều big/challenging/meaningful p", data.UCS2)
		require.NoError(t, err)

		require.Equal(t, 1, len(sm))
	})

	t.Run("shortMessageSplitGSM7_empty", func(t *testing.T) {
		// over UCS2 chars limit (89/67), split
		sm, err := NewLongMessageWithEncoding("", data.GSM7BIT)
		require.NoError(t, err)

		require.Equal(t, 1, len(sm))
	})

	t.Run("indempotentMarshal", func(t *testing.T) {
		// over gsm7 chars limit ( 160/160 ), split
		multiSM, err := NewLongMessageWithEncoding("abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyz1234", data.GSM7BIT)
		require.NoError(t, err)

		for i := range multiSM {
			b1, b2 := NewBuffer(nil), NewBuffer(nil)
			_ = multiSM[i].Marshal(b1)
			_ = multiSM[i].Marshal(b2)
			require.Equal(t, b1.Bytes(), b2.Bytes())
		}
	})
}

func TestPackedMessageWireDecoding(t *testing.T) {
	for _, u := range []UDH{nil, {NewIEConcatMessage(2, 1, 1)}, {NewIEConcatMessage(2, 1, 1), {ID: 0x70}}} {
		for n := 1; n <= 170; n++ {
			text := strings.Repeat("A", n)
			if n == 7 {
				text = "testing"
			}
			s, err := NewShortMessageWithEncoding(text, data.GSM7BITPACKED)
			require.NoError(t, err)
			require.NoError(t, s.SetUDH(u))
			p := NewSubmitSM().(*SubmitSM)
			p.Message = s
			if u != nil {
				p.EsmClass = data.SM_UDH_GSM
			}
			wire := NewBuffer(nil)
			p.Marshal(wire)
			require.NoError(t, wire.Err())
			parsed, err := Parse(bytes.NewReader(wire.Bytes()))
			require.NoError(t, err)
			got, err := parsed.(*SubmitSM).Message.GetMessageWithEncoding(data.GSM7BITPACKED)
			require.NoError(t, err)
			require.Equal(t, text, got, "header %d length %d", u.UDHL(), n)
		}
	}
}
func TestCustomGSMAlphabetSplitting(t *testing.T) {
	for _, coding := range []byte{0x10, 0x40, 0xF0, 0xF1} {
		enc := data.FromDataCoding(coding)
		for _, text := range []string{strings.Repeat("a", 133) + "€" + strings.Repeat("b", 10), strings.Repeat("A", 160), strings.Repeat("A", 161), strings.Repeat("A", 152) + "€" + strings.Repeat("B", 8)} {
			p := NewSubmitSM().(*SubmitSM)
			require.NoError(t, p.Message.SetLongMessageWithEnc(text, enc))
			septets, err := enc.Encode(text)
			require.NoError(t, err)
			require.Equal(t, len(septets) > 160, p.ShouldSplit())
			parts, err := p.Split()
			require.NoError(t, err)
			var joined string
			for _, part := range parts {
				wire := NewBuffer(nil)
				part.Marshal(wire)
				require.NoError(t, wire.Err())
				parsed, err := Parse(bytes.NewReader(wire.Bytes()))
				require.NoError(t, err)
				sm := parsed.(*SubmitSM).Message
				got, err := sm.GetMessage()
				require.NoError(t, err)
				joined += got
				raw, _ := sm.GetMessageData()
				limit := 160
				if len(parts) > 1 {
					limit = 153
				}
				require.LessOrEqual(t, len(raw), limit)
				require.Equal(t, coding, sm.Encoding().DataCoding())
			}
			require.Equal(t, text, joined)
			if text == strings.Repeat("A", 161) {
				first, _ := parts[0].Message.GetMessageData()
				require.Len(t, first, 153, "custom GSM concatenation uses 153 septets")
			}
			if len(septets) <= 160 {
				require.Len(t, parts, 1)
			}
		}
	}
}
func TestIgnoredUDHPreservesPayloadAndWire(t *testing.T) {
	for _, header := range [][]byte{{4, 0, 3, 1, 2}, {1, 0}, {6, 0, 3, 1, 2, 1, 0x70}, {0}} {
		p := NewSubmitSM().(*SubmitSM)
		p.EsmClass = data.SM_UDH_GSM
		require.NoError(t, p.Message.SetMessageDataWithEncoding(append(append([]byte(nil), header...), []byte("hello")...), data.GSM7BIT))
		wire := NewBuffer(nil)
		p.Marshal(wire)
		require.NoError(t, wire.Err())
		parsed, err := Parse(bytes.NewReader(wire.Bytes()))
		require.NoError(t, err)
		sm := parsed.(*SubmitSM).Message
		require.Empty(t, sm.UDH())
		payload, _ := sm.GetMessageData()
		require.Equal(t, []byte("hello"), payload)
		round := NewBuffer(nil)
		parsed.Marshal(round)
		require.NoError(t, round.Err())
		require.Equal(t, wire.Bytes(), round.Bytes())
	}
}
func TestSplitDropsSARParameters(t *testing.T) {
	p := NewSubmitSM().(*SubmitSM)
	require.NoError(t, p.Message.SetLongMessageWithEnc(strings.Repeat("A", 200), data.GSM7BIT))
	for _, field := range []Field{NewFieldUint16(TagSarMsgRefNum, 42), NewFieldUint8(TagSarTotalSegments, 1), NewFieldUint8(TagSarSegmentSeqnum, 1), NewFieldUint8(TagSarSegmentSeqnum, 3), NewFieldUint8(TagUserMessageReference, 7)} {
		p.AppendOptionalParam(field)
	}
	parts, err := p.Split()
	require.NoError(t, err)
	require.Len(t, parts, 2)
	for _, part := range parts {
		_, _, _, ok := part.GetSARConcatInfo()
		require.False(t, ok)
		for _, tag := range []Tag{TagSarMsgRefNum, TagSarTotalSegments, TagSarSegmentSeqnum} {
			require.Empty(t, part.GetOptionalParams(tag))
		}
		_, ok = part.GetOptionalParam(TagUserMessageReference)
		require.True(t, ok)
	}
	require.Len(t, p.OptionalParameters, 5)
}

func TestSplitRetainsZeroSpareAsAt(t *testing.T) {
	// Under CR inference this zero-filled spare septet is an actual @.
	raw, err := gsm7.Pack7Bit(bytes.Repeat([]byte{'A'}, 167), 0)
	require.NoError(t, err)
	sm, err := NewBinaryShortMessageWithEncoding(raw, data.GSM7BITPACKED)
	require.NoError(t, err)
	parts, err := sm.split()
	require.NoError(t, err)
	var joined string
	for _, part := range parts {
		wire := NewBuffer(nil)
		require.NoError(t, part.Marshal(wire))
		var received ShortMessage
		require.NoError(t, received.Unmarshal(wire, true))
		text, err := received.GetMessageWithEncoding(data.GSM7BITPACKED)
		require.NoError(t, err)
		joined += text
	}
	require.Equal(t, strings.Repeat("A", 167)+"@", joined)
	require.Equal(t, raw, sm.messageData)
}

func TestPackedMessageWirePadding(t *testing.T) {
	for _, u := range []UDH{nil, {NewIEConcatMessage(2, 1, 1)}, {NewIEConcatMessage16Bit(2, 1, 1)}, {NewIEConcatMessage(2, 1, 1), {ID: 0x70}}} {
		fill := packedFillBits(u.UDHL())
		for n := 1; n <= 40; n++ {
			if (8-(n*7+fill)%8)%8 != 7 {
				continue
			}
			sm, err := NewShortMessageWithEncoding(strings.Repeat("A", n), data.GSM7BITPACKED)
			require.NoError(t, err)
			require.NoError(t, sm.SetUDH(u))
			request := NewSubmitSM().(*SubmitSM)
			request.Message = sm
			if u != nil {
				request.EsmClass = data.SM_UDH_GSM
			}
			wire := NewBuffer(nil)
			request.Marshal(wire)
			require.NoError(t, wire.Err())
			parsed, err := Parse(bytes.NewReader(wire.Bytes()))
			require.NoError(t, err)
			raw, _ := parsed.(*SubmitSM).Message.GetMessageData()
			bit := fill + n*7
			var pad byte
			for k := 0; k < 7; k++ {
				pad |= ((raw[(bit+k)/8] >> uint((bit+k)%8)) & 1) << uint(k)
			}
			require.Equal(t, byte(0x0d), pad, "fill %d septets %d", fill, n)
		}
	}
}

func TestIgnoredPackedUDHSettersUseWireAlignment(t *testing.T) {
	p := NewSubmitSM().(*SubmitSM)
	p.EsmClass = data.SM_UDH_GSM
	require.NoError(t, p.Message.SetMessageDataWithEncoding([]byte{3, 0, 3, 1, 'A'}, data.GSM7BIT))
	b := NewBuffer(nil)
	p.Marshal(b)
	parsed, err := Parse(bytes.NewReader(b.Bytes()))
	require.NoError(t, err)
	sm := parsed.(*SubmitSM).Message
	require.Equal(t, []byte{3, 0, 3, 1}, sm.IgnoredUDH())
	raw := sm.IgnoredUDH()
	raw[0] = 99
	require.Equal(t, byte(3), sm.IgnoredUDH()[0])
	require.NoError(t, sm.SetMessageWithEncoding("AA@", data.GSM7BITPACKED))
	got, err := sm.GetMessage()
	require.NoError(t, err)
	require.Equal(t, "AA@", got)
	require.NoError(t, sm.SetMessageWithEncoding("ABC", data.GSM7BITPACKED))
	require.NoError(t, sm.SetUDH(UDH{NewIEConcatMessage(2, 1, 1)}))
	require.Empty(t, sm.IgnoredUDH())
	got, err = sm.GetMessage()
	require.NoError(t, err)
	require.Equal(t, "ABC", got)
}

func TestCustomDefaultAlphabetUsesSuppliedCodec(t *testing.T) {
	for _, coding := range []byte{0, 0x10, 0x80} {
		enc := data.NewCustomEncoding(coding, data.UCS2)
		for _, n := range []int{70, 71, 100} {
			text := strings.Repeat("A", n)
			parts, err := NewLongMessageWithEncoding(text, enc)
			require.NoError(t, err)
			var joined string
			for _, part := range parts {
				raw, _ := part.GetMessageData()
				require.LessOrEqual(t, len(raw)+part.UDH().UDHL(), 140)
				got, err := enc.Decode(raw)
				require.NoError(t, err)
				joined += got
			}
			require.Equal(t, text, joined)
			if n == 70 {
				require.Len(t, parts, 1)
			} else {
				require.Greater(t, len(parts), 1)
			}
		}
	}
}
func TestSplitPackedTailsFollowCRPolicy(t *testing.T) {
	prefix := strings.Repeat("A", 167)
	for _, tc := range []struct {
		text, inferred string
		reject         bool
	}{
		{prefix, prefix, true},
		{prefix + "\r", prefix + "\r\r", false},
		{prefix + "@", prefix + "@", false},
		{prefix + "\r\r", prefix + "\r\r", false},
	} {
		raw, err := data.GSM7BITPACKED.Encode(tc.text)
		require.NoError(t, err)
		sm, err := NewBinaryShortMessageWithEncoding(raw, data.GSM7BITPACKED)
		require.NoError(t, err)
		parts, err := sm.split()
		if tc.reject {
			require.Error(t, err, "whole CR pad still has no exact count")
		} else {
			require.NoError(t, err, "new CR policy defines the raw tail for %q", tc.text)
			var joined string
			for _, part := range parts {
				wire := NewBuffer(nil)
				require.NoError(t, part.Marshal(wire))
				var received ShortMessage
				require.NoError(t, received.Unmarshal(wire, true))
				text, err := received.GetMessageWithEncoding(data.GSM7BITPACKED)
				require.NoError(t, err)
				joined += text
			}
			require.Equal(t, tc.inferred, joined)
		}
		require.Equal(t, raw, sm.messageData)
		known, err := NewShortMessageWithEncoding(tc.text, data.GSM7BITPACKED)
		require.NoError(t, err)
		_, err = known.split()
		require.NoError(t, err, "retained original source text resolves the count")
	}
}

func TestMarshalRejectsTruncatedDeclaredUDH(t *testing.T) {
	for _, p := range []PDU{NewSubmitSM(), NewSubmitMulti(), NewDeliverSM()} {
		var sm *ShortMessage
		switch v := p.(type) {
		case *SubmitSM:
			v.EsmClass = data.SM_UDH_GSM
			sm = &v.Message
		case *SubmitMulti:
			v.EsmClass = data.SM_UDH_GSM
			sm = &v.Message
		case *DeliverSM:
			v.EsmClass = data.SM_UDH_GSM
			sm = &v.Message
		}
		require.NoError(t, sm.SetMessageDataWithEncoding([]byte{5, 0, 3}, data.GSM7BIT))
		b := NewBuffer(nil)
		p.Marshal(b)
		require.Error(t, b.Err())
		require.Empty(t, b.Bytes())
	}
}

func TestCustomPackedCodecAlignment(t *testing.T) {
	enc := data.NewCustomEncoding(0, data.GSM7BITPACKED)
	for _, u := range []UDH{{NewIEConcatMessage(2, 1, 1)}, {NewIEConcatMessage(2, 1, 1), {ID: 0x70}}} {
		sm, err := NewShortMessageWithEncoding("testing", enc)
		require.NoError(t, err)
		require.NoError(t, sm.SetUDH(u))
		p := NewSubmitSM().(*SubmitSM)
		p.EsmClass = data.SM_UDH_GSM
		p.Message = sm
		b := NewBuffer(nil)
		p.Marshal(b)
		parsed, err := Parse(bytes.NewReader(b.Bytes()))
		require.NoError(t, err)
		raw, _ := parsed.(*SubmitSM).Message.GetMessageData()
		expected, err := encodeTextWithUDH("testing", data.GSM7BITPACKED, u)
		require.NoError(t, err)
		require.Equal(t, expected, raw)
		got, err := parsed.(*SubmitSM).Message.GetMessageWithEncoding(enc)
		require.NoError(t, err)
		require.Equal(t, "testing", got)
	}
}

type identityTextCodec struct{}

func (identityTextCodec) Encode(s string) ([]byte, error) { return []byte(s), nil }
func (identityTextCodec) Decode(b []byte) (string, error) { return string(b), nil }
func TestCustomCodecWithoutSplitterFitsWireField(t *testing.T) {
	enc := data.NewCustomEncoding(0x03, data.NewCustomEncoding(0x03, identityTextCodec{}))
	for _, tc := range []struct {
		name    string
		header  UDH
		maximum int
	}{{"plain", nil, 254}, {"UDH", UDH{NewIEConcatMessage(1, 1, 7)}, 248}} {
		for _, n := range []int{140, 141, 200, tc.maximum, tc.maximum + 1, 256} {
			t.Run(fmt.Sprintf("%s/%d", tc.name, n), func(t *testing.T) {
				p := NewSubmitSM().(*SubmitSM)
				require.NoError(t, p.Message.SetUDH(tc.header))
				if tc.header != nil {
					p.EsmClass = data.SM_UDH_GSM
				}
				require.NoError(t, p.Message.SetLongMessageWithEnc(strings.Repeat("x", n), enc))
				require.Equal(t, n > tc.maximum, p.ShouldSplit())
				parts, err := p.Split()
				if n > tc.maximum {
					require.Error(t, err)
					return
				}
				require.NoError(t, err)
				require.Len(t, parts, 1)
				require.Same(t, p, parts[0])
				require.NoError(t, Validate(p))
				b := NewBuffer(nil)
				p.Marshal(b)
				require.NoError(t, b.Err())
				q, err := Parse(b)
				require.NoError(t, err)
				raw, _ := q.(*SubmitSM).Message.GetMessageData()
				require.Equal(t, strings.Repeat("x", n), string(raw))
			})
		}
	}
}

func TestCustomCodecWithoutSplitterConstructorLimit(t *testing.T) {
	enc := data.NewCustomEncoding(0x03, identityTextCodec{})
	for _, n := range []int{254, 255} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			parts, err := NewLongMessageWithEncoding(strings.Repeat("x", n), enc)
			if n == 255 {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Len(t, parts, 1)
			p := NewSubmitSM().(*SubmitSM)
			p.Message = *parts[0]
			require.NoError(t, Validate(p))
		})
	}
}

func TestOpaqueTextCodingSplitBudgets(t *testing.T) {
	for _, code := range []byte{0x05, 0x09, 0x0a, 0x0d, 0x0e} {
		for _, n := range []int{140, 141} {
			p := NewSubmitSM().(*SubmitSM)
			require.NoError(t, p.Message.SetMessageDataWithEncoding(make([]byte, n), data.FromDataCoding(code)))
			parts, err := p.Split()
			if n == 141 {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Len(t, parts, 1)
				require.Equal(t, code, parts[0].Message.Encoding().DataCoding())
			}
		}
	}
}

func TestPackedTerminalCRPaddingOnWire(t *testing.T) {
	for _, header := range []UDH{nil, {NewIEConcatMessage(1, 1, 7)}} {
		for _, n := range []int{8, 9, 16, 17} {
			text := strings.Repeat("A", n-1) + "\r"
			p := NewSubmitSM().(*SubmitSM)
			require.NoError(t, p.Message.SetUDH(header))
			if header != nil {
				p.EsmClass = data.SM_UDH_GSM
			}
			require.NoError(t, p.Message.SetMessageWithEncoding(text, data.GSM7BITPACKED))
			b := NewBuffer(nil)
			p.Marshal(b)
			require.NoError(t, b.Err())
			q, err := Parse(b)
			require.NoError(t, err)
			m := q.(*SubmitSM).Message
			got, err := m.GetMessageWithEncoding(data.GSM7BITPACKED)
			require.NoError(t, err)
			expected := text
			if (packedFillBits(header.UDHL())+n*7)%8 == 0 {
				// TS 23.038 §6.1.2.3.1: inferred receive keeps the doubled CR.
				expected += "\r"
			}
			require.Equal(t, expected, got)
		}
	}
}

func TestPackedSplitPreservesLiteralDoubleCR(t *testing.T) {
	text := strings.Repeat("A", 297) + "\r\r"
	p := NewSubmitSM().(*SubmitSM)
	require.NoError(t, p.Message.SetLongMessageWithEnc(text, data.GSM7BITPACKED))
	parts, err := p.Split()
	require.NoError(t, err)
	var joined string
	for _, part := range parts {
		b := NewBuffer(nil)
		part.Marshal(b)
		require.NoError(t, b.Err())
		q, err := Parse(b)
		require.NoError(t, err)
		got, err := q.(*SubmitSM).Message.GetMessageWithEncoding(data.GSM7BITPACKED)
		require.NoError(t, err)
		joined += got
	}
	require.Equal(t, text, joined)
}
