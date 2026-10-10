package gosmpp

import (
	"errors"
	"io"
	"testing"

	"github.com/gomaja/go-smpp/pdu"
)

func TestWindowInterruptedWriteRetainsClosingAndTransportCause(t *testing.T) {
	v, gate, _ := newWindowGatedWriter(t, lifecycleSettings(), io.ErrClosedPipe)
	result := make(chan error, 1)
	go func() { _, err := v.out.write(pdu.NewSubmitSM()); result <- err }()
	lifecycleWait(t, gate.entered)
	v.out.stop(StoppingProcessOnly)
	close(gate.release)
	err := <-result
	if !errors.Is(err, ErrConnectionClosing) || !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("interrupted write lost closing or transport cause: %v", err)
	}
}
