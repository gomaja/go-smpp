package pdu

import (
	"errors"
	"fmt"

	"github.com/gomaja/go-smpp/data"
)

var (
	// ErrIncompletePDU identifies a failure to read a complete PDU frame.
	// The returned error also wraps the underlying reader error.
	ErrIncompletePDU = errors.New("incomplete PDU frame")

	// ErrInvalidCommandLength identifies a command_length outside the supported
	// framing range. SMPP v3.4 §§4.3, 5.1.3 associate it with ESME_RINVCMDLEN.
	ErrInvalidCommandLength = errors.New("invalid PDU command length")
)

// DecodeError describes an invalid PDU whose entire frame has been read.
// Header retains the wire header, including the command ID and sequence number.
// Status suggests a response status from SMPP v3.4 §5.1.3; the session layer
// chooses the response operation under §§2.8, 3.3 and 4.3.
// Parse returns no partially decoded PDU with this error, and the reader is
// positioned at the next frame. Framing failures do not return DecodeError.
type DecodeError struct {
	Header Header
	Status data.CommandStatusType
	Err    error
}

// Error describes the command, sequence number, suggested status and cause.
func (e *DecodeError) Error() string {
	return fmt.Sprintf("decode command 0x%08x sequence %d (status 0x%08x): %v",
		uint32(e.Header.CommandID), e.Header.SequenceNumber, uint32(e.Status), e.Err)
}

// Unwrap returns the underlying decoding error.
func (e *DecodeError) Unwrap() error {
	return e.Err
}

type decodeFieldError struct {
	status data.CommandStatusType
	err    error
}

func (e *decodeFieldError) Error() string { return e.err.Error() }
func (e *decodeFieldError) Unwrap() error { return e.err }

func withDecodeStatus(err error, status data.CommandStatusType) error {
	if err == nil {
		return nil
	}
	var field *decodeFieldError
	if errors.As(err, &field) {
		return err
	}
	return &decodeFieldError{status: status, err: err}
}

// ErrEncode identifies a PDU rejected before any bytes are sent.
var ErrEncode = errors.New("cannot encode PDU")

// EncodeError identifies the outgoing PDU whose encoding failed. The connection
// remains usable because no bytes from this PDU were written to it.
type EncodeError struct {
	PDU PDU
	Err error
}

// Error describes the failed encoding and its cause.
func (e *EncodeError) Error() string { return fmt.Sprintf("%v (%T): %v", ErrEncode, e.PDU, e.Err) }

// Unwrap returns the encoding failure.
func (e *EncodeError) Unwrap() error { return e.Err }

// Is identifies encode failures independently of their underlying cause.
func (e *EncodeError) Is(target error) bool { return target == ErrEncode }
