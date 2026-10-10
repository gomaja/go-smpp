package gosmpp

import (
	"net"

	"github.com/gomaja/go-smpp/data"
	"github.com/gomaja/go-smpp/pdu"
)

// ServerBindError reports safe operational metadata for a server-side bind
// rejection. It intentionally omits the bind password.
type ServerBindError struct {
	LocalAddr     net.Addr
	RemoteAddr    net.Addr
	SystemID      string
	BindingType   pdu.BindingType
	CommandStatus data.CommandStatusType
	BindDecoded   bool
	ResponseSent  bool
	Reason        string
	Err           error
}

// Error returns the bind rejection reason.
func (e *ServerBindError) Error() string {
	if e.Reason != "" {
		return e.Reason
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return "server bind rejected"
}

// Unwrap returns the underlying network or parsing error.
func (e *ServerBindError) Unwrap() error {
	return e.Err
}
