package pdu

import (
	"bytes"
	"fmt"
	"sync/atomic"

	"github.com/gomaja/go-smpp/data"
	"github.com/gomaja/go-smpp/errors"
	"github.com/gomaja/go-sms/encoding/gsm7"
)

var ref = uint32(0)

// ShortMessage message.
type ShortMessage struct {
	SmDefaultMsgID byte
	message        string
	enc            data.Encoding
	udHeader       UDH
	ignoredUDH     []byte
	messageData    []byte
}

// NewShortMessage returns new ShortMessage.
func NewShortMessage(message string) (s ShortMessage, err error) {
	err = s.SetMessageWithEncoding(message, data.GSM7BIT)
	return
}

// NewShortMessageWithEncoding returns new ShortMessage with predefined encoding.
func NewShortMessageWithEncoding(message string, enc data.Encoding) (s ShortMessage, err error) {
	err = s.SetMessageWithEncoding(message, enc)
	return
}

// NewBinaryShortMessage returns new ShortMessage.
func NewBinaryShortMessage(messageData []byte) (s ShortMessage, err error) {
	err = s.SetMessageDataWithEncoding(messageData, data.BINARY8BIT2)
	return
}

// NewBinaryShortMessageWithEncoding returns new ShortMessage with predefined encoding.
func NewBinaryShortMessageWithEncoding(messageData []byte, enc data.Encoding) (s ShortMessage, err error) {
	err = s.SetMessageDataWithEncoding(messageData, enc)
	return
}

// NewLongMessage returns long message splitted into multiple short message
func NewLongMessage(message string) (s []*ShortMessage, err error) {
	return NewLongMessageWithEncoding(message, data.GSM7BIT)
}

// NewLongMessageWithEncoding returns long message splitted into multiple short message with encoding of choice
func NewLongMessageWithEncoding(message string, enc data.Encoding) (s []*ShortMessage, err error) {
	sm := &ShortMessage{message: message, enc: enc}
	if opts, gsm := gsm7Options(enc); gsm && (opts.LockingShift != 0 || opts.SingleShift != 0) {
		if err := sm.SetLongMessageWithEnc(message, enc); err != nil {
			return nil, err
		}
	}
	return sm.split()
}

// SetMessageWithEncoding sets message with encoding.
func (c *ShortMessage) SetMessageWithEncoding(message string, enc data.Encoding) error {
	return c.setTextMessage(message, enc, false)
}

// SetLongMessageWithEnc sets ShortMessage with message longer than  256 bytes
// callers are expected to call Split() after this
func (c *ShortMessage) SetLongMessageWithEnc(message string, enc data.Encoding) error {
	return c.setTextMessage(message, enc, true)
}

func (c *ShortMessage) setTextMessage(message string, enc data.Encoding, long bool) error {
	if enc == nil {
		enc = data.GSM7BIT
	}
	next := *c
	var err error
	next.udHeader, err = nationalLanguageHeader(enc, c.udHeader)
	if err != nil {
		return err
	}
	if c.ignoredUDH != nil && next.udHeader.hasNationalLanguage() {
		return fmt.Errorf("replace the ignored UDH before selecting national language tables")
	}
	header, err := next.headerBytes()
	if err != nil {
		return err
	}
	payload, err := encodeTextWithHeaderLength(message, enc, len(header))
	if err != nil {
		return err
	}
	if !long {
		if err := next.validatePayload(payload); err != nil {
			return err
		}
	}
	next.message, next.enc, next.messageData = message, enc, payload
	*c = next
	return nil
}

// UDH gets user data header for short message
func (c *ShortMessage) UDH() UDH {
	return c.udHeader
}

// IgnoredUDH returns a copy of a bounded header ignored under TS 23.040
// §9.2.3.24 because its final information element was truncated. UDH returns
// no parsed elements for such a header; the original bytes remain on marshal.
func (c *ShortMessage) IgnoredUDH() []byte { return append([]byte(nil), c.ignoredUDH...) }

func (c *ShortMessage) headerBytes() ([]byte, error) {
	if c.ignoredUDH != nil {
		return c.ignoredUDH, nil
	}
	return c.udHeader.MarshalBinary()
}
func (c *ShortMessage) headerLength() int {
	if c.ignoredUDH != nil {
		return len(c.ignoredUDH)
	}
	return c.udHeader.UDHL()
}

func (c *ShortMessage) validatePayload(payload []byte) error {
	header, err := c.headerBytes()
	if err != nil {
		return err
	}
	if len(header)+len(payload) > data.SM_MSG_LEN {
		return errors.ErrShortMessageLengthTooLarge
	}
	return nil
}

// SetUDH sets the user data header after validating the complete encoded field.
func (c *ShortMessage) SetUDH(udh UDH) error {
	// Preserve the selected sender tables when callers replace other IEs.
	// An empty/raw message may stage a header before selecting its text codec.
	opts, gsm := gsm7Options(c.enc)
	if gsm && (c.message != "" || opts.LockingShift != 0 || opts.SingleShift != 0) {
		var err error
		udh, err = nationalLanguageHeader(c.enc, udh)
		if err != nil {
			return err
		}
	}
	payload := c.messageData
	if packedGSMAlphabet(c.enc) && packedFillBits(c.headerLength()) != packedFillBits(udh.UDHL()) {
		// Raw packed data has no septet count. Changing its alignment must
		// not turn padding into an extra character; use verified source text.
		original, err := encodeTextWithHeaderLength(c.message, c.enc, c.headerLength())
		if err != nil || !bytes.Equal(original, payload) {
			return fmt.Errorf("changing packed UDH alignment requires the original text")
		}
		payload, err = encodeTextWithUDH(c.message, c.enc, udh)
		if err != nil {
			return err
		}
	}
	if _, err := encodedMessageLength(payload, udh); err != nil {
		return err
	}
	c.udHeader, c.messageData, c.ignoredUDH = udh, payload, nil
	return nil
}

// SetMessageDataWithEncoding sets the raw data used for PDU marshaling.
func (c *ShortMessage) SetMessageDataWithEncoding(d []byte, enc data.Encoding) error {
	if err := c.validatePayload(d); err != nil {
		return err
	}
	c.messageData, c.enc, c.message = d, enc, ""
	return nil
}

// encodedMessageLength validates SMPP v3.4 §§5.2.21–22: sm_length
// is at most 254 octets, including the UDH.
func encodedMessageLength(payload []byte, udh UDH) ([]byte, error) {
	header, err := udh.MarshalBinary()
	if err != nil {
		return nil, err
	}
	if len(header)+len(payload) > data.SM_MSG_LEN {
		return nil, errors.ErrShortMessageLengthTooLarge
	}
	return header, nil
}

// GetMessageData returns underlying binary message.
func (c *ShortMessage) GetMessageData() (d []byte, err error) {
	return c.messageData, nil
}

// GetMessage returns underlying message.
func (c *ShortMessage) GetMessage() (st string, err error) {
	enc := c.enc
	if enc == nil {
		enc = data.GSM7BIT
	}
	st, err = c.GetMessageWithEncoding(enc)
	return
}

// GetMessageWithEncoding returns (decoded) underlying message.
func (c *ShortMessage) GetMessageWithEncoding(enc data.Encoding) (st string, err error) {
	if packedGSMAlphabet(enc) {
		// SMPP carries an octet length, not a septet count. Retain exact
		// source text only while its encoding and all selected NLI IEs
		// match the current bytes/header (TS 23.040 §9.2.3.24.15–16).
		encoded, encodeErr := encodeTextWithHeaderLength(c.message, enc, c.headerLength())
		matchingHeader, headerErr := nationalLanguageHeader(enc, c.udHeader)
		if encodeErr == nil && headerErr == nil && len(matchingHeader) == len(c.udHeader) && bytes.Equal(encoded, c.messageData) {
			return c.message, nil
		}
		if !c.udHeader.hasNationalLanguage() {
			return data.DecodeGSM7Packed(c.messageData, uint8(packedFillBits(c.headerLength())), -1)
		}
		septets := gsm7.Unpack7BitCR(c.messageData, packedFillBits(c.headerLength()))
		return decodeGSM7WithUDH(septets, c.udHeader)
	}
	if unpackedGSMAlphabet(enc) && c.udHeader.hasNationalLanguage() {
		return decodeGSM7WithUDH(c.messageData, c.udHeader)
	}
	if opts, gsm := gsm7Options(enc); gsm && (opts.LockingShift != 0 || opts.SingleShift != 0) {
		// An explicit decode codec selects packing, while received UDH selects
		// the tables. Absent IEs mean defaults for this segment.
		return decodeGSM7WithUDH(c.messageData, c.udHeader)
	}
	if len(c.messageData) > 0 {
		st, err = enc.Decode(c.messageData)
	}
	return
}

// encodeTextWithUDH aligns packed GSM after the header to a septet boundary
// (TS 23.040 §9.2.3.24). All other encodings start on an octet boundary.
func encodeTextWithUDH(text string, enc data.Encoding, udh UDH) ([]byte, error) {
	header, err := udh.MarshalBinary()
	if err != nil {
		return nil, err
	}
	return encodeTextWithHeaderLength(text, enc, len(header))
}
func encodeTextWithHeaderLength(text string, enc data.Encoding, headerLength int) ([]byte, error) {
	if !packedGSMAlphabet(enc) {
		return enc.Encode(text)
	}
	if text == "" {
		return nil, nil
	}
	codec, err := unpackedGSMCodec(enc)
	if err != nil {
		return nil, err
	}
	septets, err := codec.Encode(text)
	if err != nil {
		return nil, err
	}
	return gsm7.Pack7BitCR(septets, packedFillBits(headerLength))
}

func packedFillBits(headerLength int) int {
	return (7 - (headerLength*8)%7) % 7
}

func (c *ShortMessage) payloadLimit() int {
	headerLength := c.headerLength()
	if headerLength < 0 {
		return -1
	}
	// A custom codec without segmentation defines its own representation.
	// Construct one complete field within SMPP v3.4 §§5.2.21–22's 254 octets.
	if _, ok := c.enc.(*data.CustomEncoding); ok && !binaryAlphabet(c.enc) {
		if !supportsTextSegmentation(c.enc) {
			return data.SM_MSG_LEN - headerLength
		}
	}
	if unpackedGSMAlphabet(c.enc) {
		return 160 - (headerLength*8+6)/7
	}
	return data.SM_GSM_MSG_LEN - headerLength
}

// split splits one short message into multiple short messages with UDH according
// to 3GPP TS 23.040 section 9.2.3.24.1.
//
// A message that fits its encoded budget is returned unchanged.
func (c *ShortMessage) split() ([]*ShortMessage, error) {
	encoding := c.enc
	if encoding == nil {
		encoding = data.GSM7BIT
	}
	payload := c.messageData
	if payload == nil && c.message != "" {
		var err error
		payload, err = encoding.Encode(c.message)
		if err != nil {
			return nil, err
		}
	}
	_, err := c.udHeader.MarshalBinary()
	if err != nil {
		return nil, err
	}
	// TS 23.038 §6.1.2.1 and TS 23.040 §9.2.3.24: unpacked
	// SMPP GSM carries one septet per octet; packed GSM uses octet budgets.
	limit := c.payloadLimit()
	if limit >= 0 && len(payload) <= limit {
		c.messageData, c.enc = payload, encoding
		return []*ShortMessage{c}, nil
	}
	// Replace old concatenation metadata, preserving its reference width and
	// every other IE (TS 23.040 sections 9.2.3.24.1 and 9.2.3.24.8).
	var udh UDH
	wideReference := false
	for _, ie := range c.udHeader {
		if ie.ID != data.UDH_CONCAT_MSG_8_BIT_REF && ie.ID != data.UDH_CONCAT_MSG_16_BIT_REF {
			udh = append(udh, ie)
		} else {
			wideReference = ie.ID == data.UDH_CONCAT_MSG_16_BIT_REF
		}
	}
	concatIE := NewIEConcatMessage(1, 1, 0)
	if wideReference {
		concatIE = NewIEConcatMessage16Bit(1, 1, 0)
	}
	udh = append(UDH{concatIE}, udh...)
	header, err := udh.MarshalBinary()
	if err != nil {
		return nil, err
	}
	limit = data.SM_GSM_MSG_LEN - len(header)
	if unpackedGSMAlphabet(encoding) {
		limit = 160 - (len(header)*8+6)/7
	}
	if limit <= 0 {
		return nil, errors.ErrUDHTooLong
	}
	var segments [][]byte
	var segmentText []string
	if c.udHeader.hasNationalLanguage() && (unpackedGSMAlphabet(encoding) || packedGSMAlphabet(encoding)) {
		septets := payload
		if packedGSMAlphabet(encoding) {
			original, encodeErr := encodeTextWithHeaderLength(c.message, encoding, c.headerLength())
			matchingHeader, headerErr := nationalLanguageHeader(encoding, c.udHeader)
			if encodeErr == nil && headerErr == nil && len(matchingHeader) == len(c.udHeader) && bytes.Equal(original, payload) {
				// Verified source text supplies an exact count, including a
				// terminal CR whose packed representation contains another CR.
				codec, codecErr := unpackedGSMCodec(encoding)
				if codecErr != nil {
					return nil, codecErr
				}
				septets, err = codec.Encode(c.message)
			} else {
				if packedCountAmbiguous(payload, packedFillBits(c.headerLength())) {
					return nil, fmt.Errorf("resegmenting ambiguous packed data requires the original text or a septet count")
				}
				septets = gsm7.Unpack7BitCR(payload, packedFillBits(c.headerLength()))
			}
			if err != nil {
				return nil, err
			}
			limit = 160 - (len(header)*8+6)/7
			segments, err = splitPackedSeptets(septets, limit, len(header))
			for i, part := range segments {
				segments[i], err = gsm7.Pack7BitCR(part, packedFillBits(len(header)))
				if err != nil {
					return nil, err
				}
			}
		} else {
			segments, err = splitGSMSeptets(septets, limit)
		}
		if err != nil {
			return nil, err
		}
	} else if splitter, ok := encoding.(data.Splitter); ok && !binaryAlphabet(encoding) {
		text := c.message
		if c.messageData != nil {
			if packedGSMAlphabet(encoding) && packedCountAmbiguous(payload, packedFillBits(c.headerLength())) {
				original, encodeErr := encodeTextWithHeaderLength(c.message, encoding, c.headerLength())
				if encodeErr != nil || !bytes.Equal(original, payload) {
					return nil, fmt.Errorf("resegmenting ambiguous packed data requires the original text or a septet count")
				}
			}
			text, err = c.GetMessageWithEncoding(encoding)
			if err != nil {
				return nil, err
			}
			roundTrip, err := encodeTextWithHeaderLength(text, encoding, c.headerLength())
			if err != nil || !bytes.Equal(roundTrip, payload) {
				return nil, fmt.Errorf("message cannot be resegmented without changing encoded content")
			}
		}
		if packedGSMAlphabet(encoding) {
			// Split before packing so the exact septet count is retained and
			// any preserved UDH receives its own correct fill-bit alignment.
			splitter = data.GSM7BIT.(data.Splitter)
			limit = 160 - (len(header)*8+6)/7
		}
		if packedGSMAlphabet(encoding) {
			septets, encodeErr := data.GSM7BIT.Encode(text)
			if encodeErr != nil {
				return nil, encodeErr
			}
			segments, err = splitPackedSeptets(septets, limit, len(header))
		} else {
			segments, err = splitter.EncodeSplit(text, uint(limit))
		}
		if err != nil {
			return nil, err
		}
		if packedGSMAlphabet(encoding) {
			for i, segment := range segments {
				text, err := data.GSM7BIT.Decode(segment)
				if err != nil {
					return nil, err
				}
				segmentText = append(segmentText, text)
				segments[i], err = encodeTextWithUDH(text, encoding, udh)
				if err != nil {
					return nil, err
				}
			}
		}
	} else {
		if !binaryAlphabet(encoding) {
			return nil, fmt.Errorf("encoding 0x%02x does not support text segmentation", encoding.DataCoding())
		}
		for start := 0; start < len(payload); start += limit {
			segments = append(segments, append([]byte(nil), payload[start:min(start+limit, len(payload))]...))
		}
	}
	// TS 23.040 §9.2.3.24.1: count and part number are one octet, 1..255.
	if len(segments) > 255 {
		return nil, fmt.Errorf("message requires %d segments, maximum is 255", len(segments))
	}
	reference := uint16(getRefNum())
	result := make([]*ShortMessage, 0, len(segments))
	for i, segment := range segments {
		partUDH := make(UDH, len(udh))
		for j, ie := range udh {
			partUDH[j] = InfoElement{ID: ie.ID, Data: append([]byte(nil), ie.Data...)}
		}
		partUDH[0] = NewIEConcatMessage(byte(len(segments)), byte(i+1), byte(reference))
		if wideReference {
			partUDH[0] = NewIEConcatMessage16Bit(byte(len(segments)), byte(i+1), reference)
		}
		part := &ShortMessage{enc: encoding, SmDefaultMsgID: c.SmDefaultMsgID, messageData: segment, udHeader: partUDH}
		if len(segmentText) > 0 {
			part.message = segmentText[i]
		}
		result = append(result, part)
	}
	return result, nil
}

// Marshal encodes the message with data_coding. Errors also appear in b.Err().
func (c *ShortMessage) Marshal(b *ByteBuffer) error {
	return c.marshal(b, true)
}

func (c *ShortMessage) marshal(b *ByteBuffer, withDataCoding bool) (err error) {
	defer func() { b.setError(err) }()
	header, err := c.headerBytes()
	if err == nil && len(header)+len(c.messageData) > 255 {
		err = errors.ErrShortMessageLengthTooLarge
	}
	if err != nil {
		return err
	}
	b.Grow(len(header) + len(c.messageData) + 3)
	if withDataCoding {
		var coding byte
		if c.enc != nil {
			coding = c.enc.DataCoding()
		}
		_ = b.WriteByte(coding)
	}
	_ = b.WriteByte(c.SmDefaultMsgID)
	_ = b.WriteByte(byte(len(header) + len(c.messageData)))
	_, _ = b.Write(header)
	_, _ = b.Write(c.messageData)
	return nil
}

// Unmarshal decodes a message containing data_coding.
func (c *ShortMessage) Unmarshal(b *ByteBuffer, udhi bool) error {
	return c.unmarshal(b, udhi, true)
}

func (c *ShortMessage) unmarshal(b *ByteBuffer, udhi, withDataCoding bool) (err error) {
	// SMPP v3.4 §§5.1.3, 5.2.21–22: malformed short_message,
	// including its UDH, reports a message-length error for this PDU.
	defer func() { err = withDecodeStatus(err, data.ESME_RINVMSGLEN) }()
	var coding byte
	if withDataCoding {
		if coding, err = b.ReadByte(); err != nil {
			return err
		}
	}
	var next ShortMessage
	if next.SmDefaultMsgID, err = b.ReadByte(); err != nil {
		return err
	}
	n, err := b.ReadByte()
	if err != nil {
		return err
	}
	// SMPP v3.4 §3.3: receive all one-octet lengths when the bytes fit.
	if next.messageData, err = b.ReadN(int(n)); err != nil {
		return err
	}
	next.enc = data.FromDataCoding(coding)
	if udhi && n > 0 {
		if len(next.messageData) < 1+int(next.messageData[0]) {
			return fmt.Errorf("declared UDH exceeds short_message")
		}
		consumed, err := next.udHeader.UnmarshalBinary(next.messageData)
		if err != nil {
			// TS 23.040 §9.2.3.24: a truncated final IE causes the whole
			// bounded UDH to be ignored, while its following payload survives.
			consumed = 1 + int(next.messageData[0])
			next.ignoredUDH = append([]byte(nil), next.messageData[:consumed]...)
			next.udHeader = nil
		}
		next.messageData = next.messageData[consumed:]
	}
	*c = next
	return nil
}

// Encoding returns message encoding.
func (c *ShortMessage) Encoding() data.Encoding {
	return c.enc
}

func getRefNum() uint32 {
	return atomic.AddUint32(&ref, 1)
}

// FromDataCoding resolves TS 23.038 §4 alphabet bits. An explicitly supplied
// custom codec, particularly SMSC-default coding 0, retains its own alphabet.
func unpackedGSMAlphabet(enc data.Encoding) bool {
	opts, gsm := gsm7Options(enc)
	return gsm && !opts.Packed
}

func binaryAlphabet(enc data.Encoding) bool {
	if custom, ok := enc.(*data.CustomEncoding); ok {
		base, ok := custom.BaseEncoding().(data.Encoding)
		return ok && binaryAlphabet(base)
	}
	return enc == data.BINARY8BIT1 || enc == data.BINARY8BIT2
}

// Keep ESC pairs together and avoid tails whose inferred CR decoding changes
// their septets (TS 23.038 §§6.1.2.3.1, 6.2.1.1). go-sms owns padding policy.
func splitPackedSeptets(septets []byte, limit, headerLength int) ([][]byte, error) {
	fill := packedFillBits(headerLength)
	unambiguous := func(src []byte) bool {
		packed, err := gsm7.Pack7BitCR(src, fill)
		return err == nil && len(packed) <= data.SM_GSM_MSG_LEN-headerLength && bytes.Equal(gsm7.Unpack7BitCR(packed, fill), src)
	}
	var parts [][]byte
	for start := 0; start < len(septets); {
		end := min(start+limit, len(septets))
		for end > start {
			if !endsWithGSMEscape(septets[start:end]) && unambiguous(septets[start:end]) && (len(septets)-end > limit || unambiguous(septets[end:])) {
				break
			}
			end--
		}
		if end == start {
			return nil, fmt.Errorf("packed text cannot be segmented unambiguously without a septet count")
		}
		parts = append(parts, append([]byte(nil), septets[start:end]...))
		start = end
	}
	return parts, nil
}

func packedCountAmbiguous(src []byte, fill int) bool {
	return !bytes.Equal(gsm7.Unpack7Bit(src, fill), gsm7.Unpack7BitCR(src, fill))
}

func (c *ShortMessage) validateUDHI(udhi bool) error {
	if !udhi {
		return nil
	}
	header, err := c.headerBytes()
	if err != nil {
		return err
	}
	if len(header) > 0 || len(c.messageData) == 0 {
		return nil
	}
	// TS 23.040 §9.2.3.24: UDHL must fit the complete user-data field.
	if 1+int(c.messageData[0]) > len(c.messageData) {
		return fmt.Errorf("declared UDH exceeds short_message")
	}
	return nil
}

func packedGSMAlphabet(enc data.Encoding) bool {
	opts, gsm := gsm7Options(enc)
	return gsm && opts.Packed
}

func supportsTextSegmentation(codec data.EncDec) bool {
	for {
		custom, ok := codec.(*data.CustomEncoding)
		if !ok {
			break
		}
		codec = custom.BaseEncoding()
	}
	_, ok := codec.(data.Splitter)
	return ok
}
