package main

import (
	"math/rand"
	"strconv"

	"github.com/gomaja/go-smpp/pdu"
)

// CustomSubmitSM by embedding the PDU interface
// and adding messageId as an extra field to SubmitSM
type CustomSubmitSM struct {
	pdu.PDU
	messageId string
}

// newCustomSubmitSM returns CustomSubmitSM PDU.
// Using rand.Int to generate new id for each CustomSubmitSM
func newCustomSubmitSM() (CustomSubmitSM, error) {
	submit, err := newSubmitSM()
	if err != nil {
		return CustomSubmitSM{}, err
	}
	return CustomSubmitSM{
		PDU:       submit,
		messageId: strconv.Itoa(rand.Int()),
	}, nil
}
