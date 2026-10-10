package gosmpp

import (
	"sync/atomic"

	"github.com/gomaja/go-smpp/pdu"
)

// SessionStatus describes the liveness of a bound session.
type SessionStatus string

const (
	// SessionActive identifies a session with live read and write paths.
	SessionActive SessionStatus = "active"
	// SessionClosed identifies an unbound or closed session.
	SessionClosed SessionStatus = "closed"
)

// unboundAddr stands in for the local and remote addresses of a Session that has
// no bound transceiver. Callers format these with String(), so a nil net.Addr
// would only move the panic into the caller.
type unboundAddr struct{}

func (unboundAddr) Network() string { return "" }
func (unboundAddr) String() string  { return "" }

// GetSystemIDStatus reports the addresses and liveness of the session.
//
// A Session that is registered but has no bound transceiver — bind lost and
// rebinding not yet completed, or created before its first successful bind —
// is reported as closed with placeholder addresses rather than panicking.
func (s *Session) GetSystemIDStatus(systemID string) SystemIDStatus {
	c := s.bound()
	if c == nil {
		return SystemIDStatus{
			SystemID:    systemID,
			LocalAddr:   unboundAddr{},
			RemoteAddr:  unboundAddr{},
			Status:      SessionClosed,
			BindingType: s.bindingType(),
		}
	}

	return SystemIDStatus{
		SystemID:    systemID,
		LocalAddr:   c.conn.LocalAddr(),
		RemoteAddr:  c.conn.RemoteAddr(),
		Status:      c.GetSessionStatus(),
		BindingType: s.bindingType(),
	}
}

// IsAlive reports whether the session has a bound transceiver that is still
// alive.
//
// Session.Transceiver() cannot be used for this: it returns an interface that
// always wraps the stored *transceivable, so an unbound session yields a
// non-nil interface holding a nil pointer, and a plain nil check passes.
func (s *Session) IsAlive() bool {
	c := s.bound()
	return c != nil && c.isAlive()
}

// SessionStatus reports "active" or "closed", mirroring the status carried by
// GetSystemIDStatus.
func (s *Session) SessionStatus() SessionStatus {
	if s.IsAlive() {
		return SessionActive
	}
	return SessionClosed
}

func (t *transceivable) isAlive() bool {
	return atomic.LoadInt32(&t.aliveState) == Alive &&
		atomic.LoadInt32(&t.in.aliveState) == Alive && atomic.LoadInt32(&t.out.aliveState) == Alive
}

func (t *transceivable) GetSessionStatus() SessionStatus {
	if t.isAlive() {
		return SessionActive
	}
	return SessionClosed
}

func (s *Session) bindingType() pdu.BindingType {
	if s.c != nil {
		return s.c.GetBindType()
	}
	return pdu.BindingType(0)
}
