package gosmpp

import (
	"net"
	"sync"
	"testing"
	"time"

	"github.com/gomaja/go-smpp/pdu"
	"github.com/stretchr/testify/require"
)

func TestSessionStatusBindingType(t *testing.T) {
	for _, bt := range []pdu.BindingType{pdu.Transmitter, pdu.Receiver, pdu.Transceiver} {
		t.Run(bt.String(), func(t *testing.T) {
			s, peer, cleanup := boundSession(t)
			defer func() { _ = peer.Close(); cleanup() }()
			s.c = &connector{bindingType: bt}
			if got := s.GetSystemIDStatus("sys").BindingType; got != bt {
				t.Fatalf("bound binding type=%v, want %v", got, bt)
			}
			s.trx.Store((*transceivable)(nil))
			if got := s.GetSystemIDStatus("sys").BindingType; got != bt {
				t.Fatalf("unbound binding type=%v, want %v", got, bt)
			}
		})
	}
}

// boundSession builds a Session with a live transceivable over an in-process
// pipe, so no SMSC is needed. The returned cleanup closes both ends.
func boundSession(t *testing.T) (*Session, net.Conn, func()) {
	t.Helper()

	client, server := net.Pipe()

	s := &Session{}
	trans := newTransceivable(NewConnection(client), Settings{
		ReadTimeout:  time.Second,
		WriteTimeout: time.Second,
	}, nil)
	s.trx.Store(trans)

	return s, server, func() {
		_ = trans.Close()
		_ = server.Close()
	}
}

func TestSessionGetSystemIDStatus_Unbound(t *testing.T) {
	// A zero-value Session has no transceiver stored. This must not panic:
	// the unchecked type assertion it replaces failed with
	// "interface conversion: interface {} is nil, not *gosmpp.transceivable".
	s := &Session{}

	var status SystemIDStatus
	require.NotPanics(t, func() {
		status = s.GetSystemIDStatus("sys-1")
	})

	require.Equal(t, "sys-1", status.SystemID)
	require.Equal(t, SessionClosed, status.Status)

	// Addresses must be non-nil so that callers formatting them do not panic
	// in turn.
	require.NotNil(t, status.LocalAddr)
	require.NotNil(t, status.RemoteAddr)
	require.NotPanics(t, func() {
		_ = status.LocalAddr.String()
		_ = status.RemoteAddr.String()
		_ = status.LocalAddr.Network()
		_ = status.RemoteAddr.Network()
	})
}

func TestSessionGetSystemIDStatus_Bound(t *testing.T) {
	s, _, cleanup := boundSession(t)
	defer cleanup()

	status := s.GetSystemIDStatus("sys-1")

	require.Equal(t, "sys-1", status.SystemID)
	require.Equal(t, SessionActive, status.Status)
	require.NotNil(t, status.LocalAddr)
	require.NotNil(t, status.RemoteAddr)
}

func TestSessionGetSystemIDStatus_AfterClose(t *testing.T) {
	s, server, _ := boundSession(t)
	defer func() { _ = server.Close() }()

	require.Equal(t, SessionActive, s.GetSystemIDStatus("sys-1").Status)

	require.NoError(t, s.Close())

	require.Equal(t, SessionClosed, s.GetSystemIDStatus("sys-1").Status)
}

func TestSessionIsAlive(t *testing.T) {
	t.Run("unbound", func(t *testing.T) {
		s := &Session{}
		require.NotPanics(t, func() {
			require.False(t, s.IsAlive())
		})
		require.Equal(t, SessionClosed, s.SessionStatus())
	})

	t.Run("bound", func(t *testing.T) {
		s, _, cleanup := boundSession(t)
		defer cleanup()

		require.True(t, s.IsAlive())
		require.Equal(t, SessionActive, s.SessionStatus())
	})

	t.Run("afterClose", func(t *testing.T) {
		s, server, _ := boundSession(t)
		defer func() { _ = server.Close() }()

		require.True(t, s.IsAlive())
		require.NoError(t, s.Close())
		require.False(t, s.IsAlive())
		require.Equal(t, SessionClosed, s.SessionStatus())
	})
}

// Transceiver() returns a non-nil interface wrapping a nil *transceivable on an
// unbound session, so the obvious `trx != nil` guard passes and the caller then
// panics on use. That trap is the reason IsAlive exists.
//
// Note this asserts on `trx == nil` directly rather than require.Nil, which uses
// reflection and reports the wrapped nil pointer as nil — hiding the very
// behaviour under test.
func TestSessionTransceiverNilInterfaceTrap(t *testing.T) {
	s := &Session{}

	trx := s.Transceiver()
	require.False(t, trx == nil, //nolint:testifylint // see doc comment: require.Nil would mask this
		"a plain nil check on Transceiver() is expected to pass on an unbound session")

	require.False(t, s.IsAlive(), "IsAlive is the reliable check instead")
	require.Equal(t, SessionClosed, s.SessionStatus())
}

// GetSystemIDStatus and IsAlive must be safe to call while the session is being
// closed concurrently. Meaningful under -race.
func TestSessionStatusConcurrentWithClose(t *testing.T) {
	s, server, _ := boundSession(t)
	defer func() { _ = server.Close() }()

	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				_ = s.GetSystemIDStatus("sys-1")
				_ = s.IsAlive()
				_ = s.SessionStatus()
			}
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = s.Close()
	}()

	wg.Wait()

	// Whatever the interleaving, the session ends closed.
	require.False(t, s.IsAlive())
	require.Equal(t, SessionClosed, s.GetSystemIDStatus("sys-1").Status)
}
