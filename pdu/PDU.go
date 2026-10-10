package pdu

import (
	stderrors "errors"
	"fmt"
	"io"

	"github.com/gomaja/go-smpp/data"
	"github.com/gomaja/go-smpp/errors"
)

// PDU represents PDU interface.
type PDU interface {
	// Marshal PDU to buffer.
	Marshal(*ByteBuffer)

	// Unmarshal PDU from buffer.
	Unmarshal(*ByteBuffer) error

	// CanResponse indicates that PDU could response to SMSC.
	CanResponse() bool

	// GetResponse PDU.
	GetResponse() PDU

	// RegisterOptionalParam assigns an optional param.
	RegisterOptionalParam(Field)

	// AppendOptionalParam appends an instance without replacing earlier tags.
	AppendOptionalParam(Field)

	// GetOptionalParam returns the first instance of a tag.
	GetOptionalParam(Tag) (Field, bool)

	// GetOptionalParams returns all instances of a tag in wire order.
	GetOptionalParams(Tag) []Field

	// GetOptionalParameters returns every optional parameter in wire order.
	GetOptionalParameters() []Field

	// ClearOptionalParameters removes every optional parameter.
	ClearOptionalParameters()

	// SetCommandStatus sets the command_status of the header.
	SetCommandStatus(data.CommandStatusType)

	// GetHeader returns PDU header.
	GetHeader() Header

	// IsOk returns true if command status is OK.
	IsOk() bool

	// IsGNack returns true if PDU is GNack.
	IsGNack() bool

	// AssignSequenceNumber assigns sequence number auto-incrementally.
	AssignSequenceNumber()

	// ResetSequenceNumber resets sequence number.
	ResetSequenceNumber()

	// GetSequenceNumber returns assigned sequence number.
	GetSequenceNumber() int32

	// SetSequenceNumber manually sets sequence number.
	SetSequenceNumber(int32)
}

type base struct {
	Header
	// OptionalParameters preserves wire order and duplicate tags. Indexes are
	// slice positions, not tags; use GetOptionalParam (first wins) or
	// GetOptionalParams for tag lookups. A two-value range yields index, field.
	OptionalParameters []Field
}

func newBase() (v base) {
	v.OptionalParameters = make([]Field, 0)
	v.AssignSequenceNumber()
	return
}

// GetOptionalParameters returns the PDU's optional parameters in wire order,
// duplicates included. The slice belongs to the PDU; callers must synchronize
// any access or modification. Clear detaches the slice, so later appends do
// not overwrite a slice retained by the caller.
func (c *base) GetOptionalParameters() []Field { return c.OptionalParameters }

// ClearOptionalParameters removes every optional parameter.
func (c *base) ClearOptionalParameters() { c.OptionalParameters = nil }

// SetCommandStatus sets the response status code.
func (c *base) SetCommandStatus(status data.CommandStatusType) { c.CommandStatus = status }

// GetHeader returns pdu header.
func (c *base) GetHeader() Header {
	return c.Header
}

func (c *base) unmarshal(b *ByteBuffer, bodyReader func(*ByteBuffer) error) (err error) {
	fullLen := b.Len()

	if err = c.Header.Unmarshal(b); err == nil {

		// try to unmarshal body
		// SMPP v5 §3.2.1.3 permits body omission on failure. Also tolerate
		// successful header-only responses from peers, with empty body fields.
		headerOnlyResponse := uint32(c.CommandID)&0x80000000 != 0 && c.CommandLength == data.PDU_HEADER_SIZE
		if bodyReader != nil && !headerOnlyResponse {
			err = bodyReader(b)
		}

		if err == nil {
			// command length
			cmdLength := int(c.CommandLength)

			// got - total read byte(s)
			got := fullLen - b.Len()
			if got > cmdLength {
				err = errors.ErrInvalidPDU
				return
			}

			// body < command_length, still have optional parameters ?
			if got < cmdLength {
				var optParam []byte
				if optParam, err = b.ReadN(cmdLength - got); err == nil {
					err = c.unmarshalOptionalParam(optParam)
				}
				if err != nil {
					return
				}
			}

			// validate again
			if b.Len() != fullLen-cmdLength {
				err = errors.ErrInvalidPDU
			}
		}
	}

	return
}

func (c *base) unmarshalOptionalParam(optParam []byte) (err error) {
	// SMPP v3.4 §§3.2.4.1, 5.1.3: invalid TLV framing is an
	// optional-parameter stream error, not a lost PDU frame.
	defer func() { err = withDecodeStatus(err, data.ESME_RINVOPTPARSTREAM) }()
	var fields []Field
	buf := NewBuffer(optParam)
	for buf.Len() > 0 {
		var field Field
		if err = field.Unmarshal(buf); err == nil {
			fields = append(fields, field)
		} else {
			return
		}
	}
	// SMPP v3.4 §4.7.1 Table 4-20: repeatable callbacks and their
	// presentation/alpha tags are associated by occurrence order.
	c.OptionalParameters = fields
	return
}

// Marshal to buffer.
func (c *base) marshal(b *ByteBuffer, bodyWriter func(*ByteBuffer)) {
	bodyBuf := NewBuffer(nil)

	// body
	if bodyWriter != nil {
		bodyWriter(bodyBuf)
	}

	// optional body
	for _, v := range c.OptionalParameters {
		v.Marshal(bodyBuf)
	}
	if data.PDU_HEADER_SIZE+bodyBuf.Len() > data.MAX_PDU_LEN {
		bodyBuf.setError(fmt.Errorf("PDU length exceeds supported maximum %d", data.MAX_PDU_LEN))
	}
	if bodyBuf.Err() != nil {
		b.setError(fmt.Errorf("%w: %w", ErrEncode, bodyBuf.Err()))
		return
	}

	// write header
	c.CommandLength = int32(data.PDU_HEADER_SIZE + bodyBuf.Len())
	c.Header.Marshal(b)

	// write body and its optional params
	b.WriteBuffer(bodyBuf)
}

// RegisterOptionalParam replaces the first instance of a tag, or appends it
// when absent. Later instances and their ordering are preserved.
func (c *base) RegisterOptionalParam(tlv Field) {
	for i := range c.OptionalParameters {
		if c.OptionalParameters[i].Tag == tlv.Tag {
			c.OptionalParameters[i] = tlv
			return
		}
	}
	c.AppendOptionalParam(tlv)
}

// AppendOptionalParam appends a parameter, preserving duplicate tags and order.
func (c *base) AppendOptionalParam(tlv Field) {
	c.OptionalParameters = append(c.OptionalParameters, tlv)
}

// GetOptionalParam returns the first parameter with the given tag.
// Its Data aliases the PDU's data.
func (c *base) GetOptionalParam(tag Tag) (Field, bool) {
	for _, field := range c.OptionalParameters {
		if field.Tag == tag {
			return field, true
		}
	}
	return Field{}, false
}

// GetOptionalParams returns all parameters with the given tag in wire order.
// The returned fields' Data slices alias the PDU's data.
func (c *base) GetOptionalParams(tag Tag) []Field {
	var fields []Field
	for _, field := range c.OptionalParameters {
		if field.Tag == tag {
			fields = append(fields, field)
		}
	}
	return fields
}

// IsOk is status ok.
func (c *base) IsOk() bool {
	return c.CommandStatus == data.ESME_ROK
}

// IsGNack is generic n-ack.
func (c *base) IsGNack() bool {
	return c.CommandID == data.GENERIC_NACK
}

// GetSARConcatInfo reads the first occurrence of each SAR tag. A missing or
// malformed first occurrence returns found=false, even if later ones are valid.
func (c *base) GetSARConcatInfo() (totalParts, partNum byte, mref uint16, found bool) {
	// Like GetOptionalParam, each SAR tag uses its first wire occurrence.
	reference, hasRef := c.GetOptionalParam(TagSarMsgRefNum)
	total, hasTotal := c.GetOptionalParam(TagSarTotalSegments)
	sequence, hasSequence := c.GetOptionalParam(TagSarSegmentSeqnum)
	found = hasRef && hasTotal && hasSequence && len(reference.Data) == 2 && len(total.Data) == 1 && len(sequence.Data) == 1
	if found {
		mref = uint16(reference.Data[0])<<8 | uint16(reference.Data[1])
		totalParts = total.Data[0]
		partNum = sequence.Data[0]
	}
	return
}

// GetOptionalMessagePayload returns the message bytes from the MessagePayload optional TLV
// parameter (tag 0x0424). This is used when the message content is sent as a TLV parameter
// instead of in the short_message field. Returns the message data and a boolean indicating
// whether usable payload data was found. The first wire occurrence wins.
//
// A MessagePayload parameter that is present but carries no data reports found = false, since
// an empty payload is not usable as message content. Callers that need to distinguish an absent
// parameter from a present-but-empty one should inspect OptionalParameters directly.
//
// The returned slice is the PDU's own buffer, not a copy; mutating it mutates the PDU.
func (c *base) GetOptionalMessagePayload() (data []byte, found bool) {
	if field, exists := c.GetOptionalParam(TagMessagePayload); exists && len(field.Data) > 0 {
		data = field.Data
		found = true
	}
	return
}

// Parse reads one complete PDU frame from r. Invalid content returns a
// DecodeError after consuming the frame. Incomplete reads wrap ErrIncompletePDU
// and the reader error; unsupported command lengths wrap ErrInvalidCommandLength.
func Parse(r io.Reader) (pdu PDU, err error) {
	var headerBytes [16]byte

	if _, err = io.ReadFull(r, headerBytes[:]); err != nil {
		err = fmt.Errorf("read PDU header: %w: %w", ErrIncompletePDU, err)
		return
	}

	header := ParseHeader(headerBytes)
	if header.CommandLength < 16 || header.CommandLength > data.MAX_PDU_LEN {
		err = fmt.Errorf("%w (%d): %w", ErrInvalidCommandLength, header.CommandLength, errors.ErrInvalidPDU)
		return
	}

	// read pdu body
	bodyBytes := make([]byte, header.CommandLength-16)
	if len(bodyBytes) > 0 {
		if _, err = io.ReadFull(r, bodyBytes); err != nil {
			err = fmt.Errorf("read PDU body: %w: %w", ErrIncompletePDU, err)
			return
		}
	}

	// try to create pdu
	if pdu, err = CreatePDUFromCmdID(header.CommandID); err == nil {
		buf := NewBuffer(make([]byte, 0, header.CommandLength))
		_, _ = buf.Write(headerBytes[:])
		if len(bodyBytes) > 0 {
			_, _ = buf.Write(bodyBytes)
		}
		err = pdu.Unmarshal(buf)
	}
	if err != nil {
		// SMPP v3.4 §§2.8, 3.3, 4.3, 5.1.3: a readable header and
		// fully consumed body allow an error response for this PDU.
		status := data.ESME_RINVPARLEN
		var field *decodeFieldError
		if stderrors.As(err, &field) {
			status = field.status
		}
		// SMPP v3.4 §4.3: a frame too short for mandatory fields has
		// an invalid command_length, even when the frame itself was fully read.
		if stderrors.Is(err, io.EOF) || stderrors.Is(err, io.ErrUnexpectedEOF) || (stderrors.Is(err, ErrBufferNotEnoughByteToRead) && field == nil) {
			status = data.ESME_RINVCMDLEN
		} else if stderrors.Is(err, errors.ErrUnknownCommandID) {
			status = data.ESME_RINVCMDID
		}
		err = &DecodeError{Header: header, Status: status, Err: err}
		pdu = nil
	}

	return
}
