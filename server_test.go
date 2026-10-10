package gosmpp

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gomaja/go-smpp/data"
	"github.com/gomaja/go-smpp/pdu"
	"github.com/stretchr/testify/require"
)

const testBindWait = 2 * time.Second

// startTestServer brings up a Server on an ephemeral loopback port and returns
// its address. Everything is in-process; no external SMSC is involved.
func startTestServer(t *testing.T, settings ServerSettings) (*Server, string) {
	t.Helper()

	if settings.Address == "" {
		settings.Address = "127.0.0.1:0"
	}

	srv := NewServer(settings)
	require.NoError(t, srv.Start())
	t.Cleanup(srv.Stop)

	return srv, srv.Addr().String()
}

// bind dials the server and performs a bind handshake, returning the response.
// The connection is left open so the caller can inspect session state.
func bind(t *testing.T, addr, systemID, password string, bt pdu.BindingType) (net.Conn, *pdu.BindResp) {
	t.Helper()

	conn, err := net.DialTimeout("tcp", addr, testBindWait)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	req := pdu.NewBindRequest(bt)
	req.SystemID = systemID
	req.Password = password

	buf := pdu.NewBuffer(nil)
	req.Marshal(buf)

	require.NoError(t, conn.SetDeadline(time.Now().Add(testBindWait)))
	_, err = conn.Write(buf.Bytes())
	require.NoError(t, err)

	p, err := pdu.Parse(conn)
	require.NoError(t, err)

	resp, ok := p.(*pdu.BindResp)
	require.True(t, ok, "expected BindResp, got %T", p)

	require.NoError(t, conn.SetDeadline(time.Time{}))
	return conn, resp
}

// rawBind performs a bind and decodes only the command ID and status from the
// response header, without going through pdu.Parse. Needed where the response
// body cannot be unmarshalled.
func rawBind(t *testing.T, addr, systemID, password string, bt pdu.BindingType) (data.CommandStatusType, data.CommandIDType) {
	t.Helper()

	conn, err := net.DialTimeout("tcp", addr, testBindWait)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	req := pdu.NewBindRequest(bt)
	req.SystemID = systemID
	req.Password = password

	buf := pdu.NewBuffer(nil)
	req.Marshal(buf)

	require.NoError(t, conn.SetDeadline(time.Now().Add(testBindWait)))
	_, err = conn.Write(buf.Bytes())
	require.NoError(t, err)

	// SMPP header: command_length, command_id, command_status, sequence_number.
	var header [16]byte
	_, err = io.ReadFull(conn, header[:])
	require.NoError(t, err)

	cmdID := data.CommandIDType(binary.BigEndian.Uint32(header[4:8]))
	status := data.CommandStatusType(binary.BigEndian.Uint32(header[8:12]))
	return status, cmdID
}

// waitForSessions blocks until the server reports n sessions for systemID, or
// fails. Binding completes asynchronously, so polling avoids a flaky sleep.
func waitForSessions(t *testing.T, srv *Server, systemID string, n int) {
	t.Helper()

	deadline := time.Now().Add(testBindWait)
	for time.Now().Before(deadline) {
		if len(srv.GetSystemIDStatus(systemID)) == n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	require.Len(t, srv.GetSystemIDStatus(systemID), n, "timed out waiting for %d session(s)", n)
}

func waitForServerBindError(t *testing.T, errs <-chan error) *ServerBindError {
	t.Helper()

	select {
	case err := <-errs:
		var bindErr *ServerBindError
		require.ErrorAs(t, err, &bindErr)
		return bindErr
	case <-time.After(testBindWait):
		t.Fatal("timed out waiting for server bind error")
	}

	return nil
}

func currentSessions(t *testing.T, srv *Server, systemID string) int32 {
	t.Helper()

	srv.mu.Lock()
	defer srv.mu.Unlock()

	cred, ok := srv.credStore[systemID]
	require.True(t, ok, "system ID %q not registered", systemID)
	return int32(cred.currentSession)
}

func TestServerStartSystemID(t *testing.T) {
	srv := NewServer(ServerSettings{Address: "127.0.0.1:0"})

	require.NoError(t, srv.StartSystemID(SystemIDConfig{SystemID: "sys", Password: "pw", BindingTypes: []pdu.BindingType{pdu.Transceiver}, MaxSessions: int(3), ReadTimeout: 3 * time.Second, WriteTimeout: time.Second, EnquireLink: time.Second}))

	t.Run("storesCredentials", func(t *testing.T) {
		cred, ok := srv.credStore["sys"]
		require.True(t, ok)
		require.Equal(t, "pw", cred.config.Password)
		require.Equal(t, 3, cred.config.MaxSessions)
		require.Equal(t, []pdu.BindingType{pdu.Transceiver}, cred.config.BindingTypes)
		require.Equal(t, "sys", cred.config.SystemID)
	})

	t.Run("rejectsDuplicate", func(t *testing.T) {
		err := srv.StartSystemID(SystemIDConfig{SystemID: "sys", Password: "other", BindingTypes: []pdu.BindingType{pdu.Receiver}, MaxSessions: int(1), ReadTimeout: 0, WriteTimeout: 0, EnquireLink: 0})
		require.Error(t, err)

		// The original registration must survive a rejected duplicate.
		cred := srv.credStore["sys"]
		require.Equal(t, "pw", cred.config.Password)
		require.Equal(t, []pdu.BindingType{pdu.Transceiver}, cred.config.BindingTypes)
	})
}

func TestServerBindRejections(t *testing.T) {
	srv, addr := startTestServer(t, ServerSettings{})
	require.NoError(t, srv.StartSystemID(SystemIDConfig{SystemID: "sys", Password: "pw", BindingTypes: []pdu.BindingType{pdu.Transceiver}, ReadTimeout: 3 * time.Second, WriteTimeout: time.Second, EnquireLink: time.Second}))

	tests := []struct {
		name     string
		systemID string
		password string
		binding  pdu.BindingType
		want     data.CommandStatusType
	}{
		{"unknownSystemID", "nope", "pw", pdu.Transceiver, data.ESME_RINVSYSID},
		{"wrongPassword", "sys", "wrong", pdu.Transceiver, data.ESME_RINVPASWD},
		{"wrongBindingType", "sys", "pw", pdu.Receiver, data.ESME_RBINDFAIL},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, resp := bind(t, addr, tc.systemID, tc.password, tc.binding)
			require.Equal(t, tc.want, resp.CommandStatus)
		})
	}

	// The server answers in the binding type the client asked for, so a receiver
	// bind is rejected with a bind_receiver_resp rather than a transceiver one.
	t.Run("wrongBindingTypeAnswersInKind", func(t *testing.T) {
		_, cmdID := rawBind(t, addr, "sys", "pw", pdu.Receiver)
		require.Equal(t, data.BIND_RECEIVER_RESP, cmdID)
	})

	// A rejected bind must not consume a session slot.
	require.Equal(t, int32(0), currentSessions(t, srv, "sys"))
	require.Empty(t, srv.GetSystemIDStatus("sys"))
}

func TestServerBindRejectionsReportStructuredError(t *testing.T) {
	const leakedPassword = "do-not-log-this-password"

	tests := []struct {
		name        string
		systemID    string
		password    string
		binding     pdu.BindingType
		wantStatus  data.CommandStatusType
		wantReason  string
		maxSessions int32
		preBind     bool
	}{
		{
			name:       "unknownSystemID",
			systemID:   "unknown-peer",
			password:   "pw",
			binding:    pdu.Transceiver,
			wantStatus: data.ESME_RINVSYSID,
			wantReason: "invalid system ID",
		},
		{
			name:       "wrongPassword",
			systemID:   "sys",
			password:   leakedPassword,
			binding:    pdu.Transceiver,
			wantStatus: data.ESME_RINVPASWD,
			wantReason: "invalid password",
		},
		{
			name:       "wrongBindingType",
			systemID:   "sys",
			password:   "pw",
			binding:    pdu.Receiver,
			wantStatus: data.ESME_RBINDFAIL,
			wantReason: "invalid binding type",
		},
		{
			name:        "maxSessionsReached",
			systemID:    "sys",
			password:    "pw",
			binding:     pdu.Transceiver,
			wantStatus:  data.ESME_RBINDFAIL,
			wantReason:  "max Sessions reached",
			maxSessions: 1,
			preBind:     true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			errs := make(chan error, 1)
			srv, addr := startTestServer(t, ServerSettings{
				OnError: func(err error) {
					errs <- err
				},
			})

			maxSessions := tc.maxSessions
			require.NoError(t, srv.StartSystemID(SystemIDConfig{SystemID: "sys", Password: "pw", BindingTypes: []pdu.BindingType{pdu.Transceiver}, MaxSessions: int(maxSessions), ReadTimeout: 3 * time.Second, WriteTimeout: time.Second, EnquireLink: time.Second}))

			if tc.preBind {
				_, resp := bind(t, addr, "sys", "pw", pdu.Transceiver)
				require.Equal(t, data.ESME_ROK, resp.CommandStatus)
				waitForSessions(t, srv, "sys", 1)
			}

			conn, resp := bind(t, addr, tc.systemID, tc.password, tc.binding)
			require.Equal(t, tc.wantStatus, resp.CommandStatus)

			bindErr := waitForServerBindError(t, errs)
			require.Equal(t, conn.RemoteAddr().String(), bindErr.LocalAddr.String())
			require.Equal(t, conn.LocalAddr().String(), bindErr.RemoteAddr.String())
			require.Equal(t, tc.systemID, bindErr.SystemID)
			require.Equal(t, tc.binding, bindErr.BindingType)
			require.Equal(t, tc.wantStatus, bindErr.CommandStatus)
			require.Equal(t, tc.wantReason, bindErr.Reason)
			require.Equal(t, tc.wantReason, bindErr.Error())
			require.True(t, bindErr.BindDecoded)
			require.True(t, bindErr.ResponseSent)
			require.NotContains(t, fmt.Sprintf("%+v", bindErr), leakedPassword)
		})
	}
}

func TestServerBindRejectsNonBindFirstPDUWithStructuredError(t *testing.T) {
	errs := make(chan error, 1)
	_, addr := startTestServer(t, ServerSettings{
		OnError: func(err error) {
			errs <- err
		},
	})

	conn, err := net.DialTimeout("tcp", addr, testBindWait)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	el := pdu.NewEnquireLink()
	buf := pdu.NewBuffer(nil)
	el.Marshal(buf)

	require.NoError(t, conn.SetDeadline(time.Now().Add(testBindWait)))
	_, err = conn.Write(buf.Bytes())
	require.NoError(t, err)

	_, err = pdu.Parse(conn)
	require.Error(t, err, "server must close a peer whose first PDU is not bind")

	bindErr := waitForServerBindError(t, errs)
	require.Equal(t, conn.RemoteAddr().String(), bindErr.LocalAddr.String())
	require.Equal(t, conn.LocalAddr().String(), bindErr.RemoteAddr.String())
	require.Empty(t, bindErr.SystemID)
	require.False(t, bindErr.BindDecoded)
	require.False(t, bindErr.ResponseSent)
	require.Equal(t, "first message is not bind request", bindErr.Reason)
	require.Equal(t, bindErr.Reason, bindErr.Error())
}

func TestServerBindParseFailureReportsStructuredError(t *testing.T) {
	errs := make(chan error, 1)
	_, addr := startTestServer(t, ServerSettings{
		BindTimeout: 150 * time.Millisecond,
		OnError: func(err error) {
			errs <- err
		},
	})

	conn, err := net.DialTimeout("tcp", addr, testBindWait)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	require.NoError(t, conn.SetDeadline(time.Now().Add(testBindWait)))
	buf := make([]byte, 1)
	_, err = conn.Read(buf)
	require.Error(t, err, "server must drop a peer that never sends bind")

	bindErr := waitForServerBindError(t, errs)
	require.Equal(t, conn.RemoteAddr().String(), bindErr.LocalAddr.String())
	require.Equal(t, conn.LocalAddr().String(), bindErr.RemoteAddr.String())
	require.Empty(t, bindErr.SystemID)
	require.False(t, bindErr.BindDecoded)
	require.False(t, bindErr.ResponseSent)
	require.True(t, strings.Contains(bindErr.Reason, "i/o timeout") || strings.Contains(bindErr.Reason, "timeout"),
		"reason should preserve the parse failure context, got %q", bindErr.Reason)

	var netErr net.Error
	require.True(t, errors.As(bindErr, &netErr), "underlying parse error should remain inspectable")
	require.True(t, netErr.Timeout())
}

func TestServerBindSuccess(t *testing.T) {
	srv, addr := startTestServer(t, ServerSettings{})
	require.NoError(t, srv.StartSystemID(SystemIDConfig{SystemID: "sys", Password: "pw", BindingTypes: []pdu.BindingType{pdu.Transceiver}, ReadTimeout: 3 * time.Second, WriteTimeout: time.Second, EnquireLink: time.Second}))

	_, resp := bind(t, addr, "sys", "pw", pdu.Transceiver)
	require.Equal(t, data.ESME_ROK, resp.CommandStatus)
	require.Equal(t, "", resp.SystemID)

	waitForSessions(t, srv, "sys", 1)
	require.Equal(t, int32(1), currentSessions(t, srv, "sys"))

	statuses := srv.GetSystemIDStatus("sys")
	require.Len(t, statuses, 1)
	require.Equal(t, "sys", statuses[0].SystemID)
	require.Equal(t, SessionActive, statuses[0].Status)
	require.NotNil(t, statuses[0].LocalAddr)
	require.NotNil(t, statuses[0].RemoteAddr)
}

func TestServerMaxSessions(t *testing.T) {
	t.Run("enforcedAtLimit", func(t *testing.T) {
		srv, addr := startTestServer(t, ServerSettings{})
		require.NoError(t, srv.StartSystemID(SystemIDConfig{SystemID: "sys", Password: "pw", BindingTypes: []pdu.BindingType{pdu.Transceiver}, MaxSessions: 2, ReadTimeout: 3 * time.Second, WriteTimeout: time.Second, EnquireLink: time.Second}))

		for i := range 2 {
			_, resp := bind(t, addr, "sys", "pw", pdu.Transceiver)
			require.Equal(t, data.ESME_ROK, resp.CommandStatus, "bind %d should succeed", i+1)
		}
		waitForSessions(t, srv, "sys", 2)

		// The third bind exceeds maxSessions.
		_, resp := bind(t, addr, "sys", "pw", pdu.Transceiver)
		require.Equal(t, data.ESME_RBINDFAIL, resp.CommandStatus)

		require.Equal(t, int32(2), currentSessions(t, srv, "sys"))
	})

	t.Run("zeroMeansUnlimited", func(t *testing.T) {
		srv, addr := startTestServer(t, ServerSettings{})
		require.NoError(t, srv.StartSystemID(SystemIDConfig{SystemID: "sys", Password: "pw", BindingTypes: []pdu.BindingType{pdu.Transceiver}, ReadTimeout: 3 * time.Second, WriteTimeout: time.Second, EnquireLink: time.Second}))

		for i := range 5 {
			_, resp := bind(t, addr, "sys", "pw", pdu.Transceiver)
			require.Equal(t, data.ESME_ROK, resp.CommandStatus, "bind %d should succeed", i+1)
		}
		waitForSessions(t, srv, "sys", 5)
		require.Equal(t, int32(5), currentSessions(t, srv, "sys"))
	})
}

// Session accounting must return to zero when a bound session goes away,
// otherwise a system ID slowly loses its capacity to bind.
func TestServerSessionAccountingReleasedOnDisconnect(t *testing.T) {
	srv, addr := startTestServer(t, ServerSettings{})
	require.NoError(t, srv.StartSystemID(SystemIDConfig{SystemID: "sys", Password: "pw", BindingTypes: []pdu.BindingType{pdu.Transceiver}, MaxSessions: 2, ReadTimeout: 3 * time.Second, WriteTimeout: time.Second, EnquireLink: time.Second}))

	conn, resp := bind(t, addr, "sys", "pw", pdu.Transceiver)
	require.Equal(t, data.ESME_ROK, resp.CommandStatus)
	waitForSessions(t, srv, "sys", 1)

	require.NoError(t, conn.Close())
	waitForSessions(t, srv, "sys", 0)

	require.Equal(t, int32(0), currentSessions(t, srv, "sys"),
		"session slot must be released when the peer disconnects")
}

func TestServerStopSystemID(t *testing.T) {
	srv, addr := startTestServer(t, ServerSettings{})
	require.NoError(t, srv.StartSystemID(SystemIDConfig{SystemID: "sys", Password: "pw", BindingTypes: []pdu.BindingType{pdu.Transceiver}, ReadTimeout: 3 * time.Second, WriteTimeout: time.Second, EnquireLink: time.Second}))

	_, resp := bind(t, addr, "sys", "pw", pdu.Transceiver)
	require.Equal(t, data.ESME_ROK, resp.CommandStatus)
	waitForSessions(t, srv, "sys", 1)

	srv.StopSystemID("sys")

	// Credentials gone and sessions closed.
	srv.mu.Lock()
	_, stillRegistered := srv.credStore["sys"]
	srv.mu.Unlock()
	require.False(t, stillRegistered)
	require.Empty(t, srv.GetSystemIDStatus("sys"))

	// Binding again is rejected as an unknown system ID.
	_, resp = bind(t, addr, "sys", "pw", pdu.Transceiver)
	require.Equal(t, data.ESME_RINVSYSID, resp.CommandStatus)
}

func TestServerGetSystemIDStatusUnknown(t *testing.T) {
	srv, _ := startTestServer(t, ServerSettings{})
	require.Empty(t, srv.GetSystemIDStatus("never-registered"))
}

func TestServerStopLifecycle(t *testing.T) {
	t.Run("withoutStartDoesNotPanic", func(t *testing.T) {
		srv := NewServer(ServerSettings{Address: "127.0.0.1:0"})
		require.NotPanics(t, srv.Stop, "Stop before Start must be safe: listener is nil")
	})

	t.Run("isIdempotent", func(t *testing.T) {
		srv := NewServer(ServerSettings{Address: "127.0.0.1:0"})
		require.NoError(t, srv.Start())
		require.NotPanics(t, func() {
			srv.Stop()
			srv.Stop()
			srv.Stop()
		})
	})

	t.Run("stopRejectsNewConnections", func(t *testing.T) {
		srv, addr := startTestServer(t, ServerSettings{})
		require.NoError(t, srv.StartSystemID(SystemIDConfig{SystemID: "sys", Password: "pw", BindingTypes: []pdu.BindingType{pdu.Transceiver}, ReadTimeout: 3 * time.Second, WriteTimeout: time.Second, EnquireLink: time.Second}))

		srv.Stop()

		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			// On some platforms the dial itself succeeds against a closed
			// listener; the bind exchange must still fail.
			_ = conn.SetDeadline(time.Now().Add(500 * time.Millisecond))
			_, perr := pdu.Parse(conn)
			require.Error(t, perr)
			_ = conn.Close()
		}
	})

	// Stop() closing the listener must not be reported through OnError as if it
	// were a real failure.
	t.Run("stopDoesNotReportSpuriousError", func(t *testing.T) {
		var errCount atomic.Int32
		srv, _ := startTestServer(t, ServerSettings{
			OnError: func(error) { errCount.Add(1) },
		})

		srv.Stop()

		require.Zero(t, errCount.Load(),
			"closing the listener via Stop must not surface an error to OnError")
	})
}

// The bind exchange is bounded, so a peer that connects and never sends a bind
// request cannot pin a goroutine and a descriptor indefinitely.
func TestServerBindTimeout(t *testing.T) {
	var errCount atomic.Int32
	_, addr := startTestServer(t, ServerSettings{
		BindTimeout: 150 * time.Millisecond,
		OnError:     func(error) { errCount.Add(1) },
	})

	conn, err := net.DialTimeout("tcp", addr, testBindWait)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	// Send nothing at all.
	require.NoError(t, conn.SetDeadline(time.Now().Add(testBindWait)))
	buf := make([]byte, 1)
	_, readErr := conn.Read(buf)
	require.Error(t, readErr, "server must drop a peer that never binds")

	require.Positive(t, errCount.Load(), "the timeout should be reported via OnError")
}

func TestServerBindTimeoutDefault(t *testing.T) {
	srv := NewServer(ServerSettings{Address: "127.0.0.1:0"})
	require.Equal(t, DefaultBindTimeout, srv.bindTimeout())

	srv = NewServer(ServerSettings{Address: "127.0.0.1:0", BindTimeout: 7 * time.Second})
	require.Equal(t, 7*time.Second, srv.bindTimeout())
}

// OnAllPDU must receive PDUs sent after a successful bind.
func TestServerOnAllPDU(t *testing.T) {
	var (
		mu       sync.Mutex
		received []pdu.PDU
	)

	srv, addr := startTestServer(t, ServerSettings{})
	require.NoError(t, srv.StartSystemID(SystemIDConfig{SystemID: "sys", Password: "pw", BindingTypes: []pdu.BindingType{pdu.Transceiver}, ReadTimeout: 3 * time.Second, WriteTimeout: time.Second, EnquireLink: time.Second, OnPDU: func(_ ServerSessionInfo, p pdu.PDU) (pdu.PDU, bool) {
		mu.Lock()
		received = append(received, p)
		mu.Unlock()
		return nil, false
	}}))

	conn, resp := bind(t, addr, "sys", "pw", pdu.Transceiver)
	require.Equal(t, data.ESME_ROK, resp.CommandStatus)
	waitForSessions(t, srv, "sys", 1)

	el := pdu.NewSubmitSM()
	buf := pdu.NewBuffer(nil)
	el.Marshal(buf)
	_, err := conn.Write(buf.Bytes())
	require.NoError(t, err)

	deadline := time.Now().Add(testBindWait)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(received)
		mu.Unlock()
		if n > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("OnAllPDU was never invoked for a post-bind PDU")
}

// Concurrent binds against a capped system ID must never oversubscribe it.
// Meaningful under -race.
func TestServerConcurrentBinds(t *testing.T) {
	const (
		limit   = 4
		clients = 12
	)

	srv, addr := startTestServer(t, ServerSettings{})
	require.NoError(t, srv.StartSystemID(SystemIDConfig{SystemID: "sys", Password: "pw", BindingTypes: []pdu.BindingType{pdu.Transceiver}, MaxSessions: limit, ReadTimeout: 3 * time.Second, WriteTimeout: time.Second, EnquireLink: time.Second}))

	var (
		wg        sync.WaitGroup
		succeeded atomic.Int32
		attempted sync.WaitGroup
	)

	attempted.Add(clients)
	for range clients {
		wg.Add(1)
		go func() {
			defer wg.Done()
			finishedAttempt := false
			defer func() {
				if !finishedAttempt {
					attempted.Done()
				}
			}()

			conn, err := net.DialTimeout("tcp", addr, testBindWait)
			if err != nil {
				return
			}
			defer func() { _ = conn.Close() }()

			req := pdu.NewBindRequest(pdu.Transceiver)
			req.SystemID = "sys"
			req.Password = "pw"

			buf := pdu.NewBuffer(nil)
			req.Marshal(buf)

			_ = conn.SetDeadline(time.Now().Add(testBindWait))
			if _, err = conn.Write(buf.Bytes()); err != nil {
				return
			}

			p, err := pdu.Parse(conn)
			if err != nil {
				return
			}
			if r, ok := p.(*pdu.BindResp); ok && r.CommandStatus == data.ESME_ROK {
				succeeded.Add(1)
				// Hold slots until all concurrent handshakes have finished.
				attempted.Done()
				attempted.Wait()
				finishedAttempt = true
			}
		}()
	}

	wg.Wait()

	require.LessOrEqual(t, succeeded.Load(), int32(limit),
		"maxSessions must never be oversubscribed under concurrent binds")
	require.Positive(t, succeeded.Load(), "at least one bind should have succeeded")
}

func TestServerSystemIDs(t *testing.T) {
	// Each subtest gets its own server: bind() registers a t.Cleanup that closes
	// the connection when the subtest ends, so sessions do not survive across
	// subtests and shared state would make these order-dependent.
	newServerWithSystemIDs := func(t *testing.T, systemIDs ...string) (*Server, string) {
		t.Helper()

		srv, addr := startTestServer(t, ServerSettings{})
		for _, systemID := range systemIDs {
			require.NoError(t, srv.StartSystemID(SystemIDConfig{SystemID: systemID, Password: "pw", BindingTypes: []pdu.BindingType{pdu.Transceiver}, ReadTimeout: 3 * time.Second, WriteTimeout: time.Second, EnquireLink: time.Second}))
		}
		return srv, addr
	}

	t.Run("emptyBeforeAnyBind", func(t *testing.T) {
		// Registering credentials is not the same as having a session.
		srv, _ := newServerWithSystemIDs(t, "alpha", "beta")
		require.Empty(t, srv.SystemIDs())
	})

	t.Run("listsOnlySystemIDsWithSessions", func(t *testing.T) {
		srv, addr := newServerWithSystemIDs(t, "alpha", "beta")

		_, resp := bind(t, addr, "alpha", "pw", pdu.Transceiver)
		require.Equal(t, data.ESME_ROK, resp.CommandStatus)
		waitForSessions(t, srv, "alpha", 1)

		require.Equal(t, []string{"alpha"}, srv.SystemIDs(),
			"beta is registered but unbound, so it must not appear")
	})

	t.Run("deduplicatesAcrossMultipleSessions", func(t *testing.T) {
		srv, addr := newServerWithSystemIDs(t, "alpha")

		for range 2 {
			_, resp := bind(t, addr, "alpha", "pw", pdu.Transceiver)
			require.Equal(t, data.ESME_ROK, resp.CommandStatus)
		}
		waitForSessions(t, srv, "alpha", 2)

		require.Equal(t, []string{"alpha"}, srv.SystemIDs(),
			"two sessions on one system ID must yield one entry")
	})

	t.Run("reportsEverySystemIDWithSessions", func(t *testing.T) {
		srv, addr := newServerWithSystemIDs(t, "alpha", "beta")

		for _, systemID := range []string{"alpha", "beta"} {
			_, resp := bind(t, addr, systemID, "pw", pdu.Transceiver)
			require.Equal(t, data.ESME_ROK, resp.CommandStatus)
			waitForSessions(t, srv, systemID, 1)
		}

		require.ElementsMatch(t, []string{"alpha", "beta"}, srv.SystemIDs(),
			"order is unspecified, but both must be present")
	})

	t.Run("dropsSystemIDWhenLastSessionGoes", func(t *testing.T) {
		srv, addr := newServerWithSystemIDs(t, "alpha")

		_, resp := bind(t, addr, "alpha", "pw", pdu.Transceiver)
		require.Equal(t, data.ESME_ROK, resp.CommandStatus)
		waitForSessions(t, srv, "alpha", 1)
		require.Equal(t, []string{"alpha"}, srv.SystemIDs())

		srv.StopSystemID("alpha")
		require.Empty(t, srv.SystemIDs())
	})
}

func TestServerSubmitToSystemID(t *testing.T) {
	t.Run("deliversToBoundSession", func(t *testing.T) {
		srv, addr := startTestServer(t, ServerSettings{})
		require.NoError(t, srv.StartSystemID(SystemIDConfig{SystemID: "sys", Password: "pw", BindingTypes: []pdu.BindingType{pdu.Transceiver}, ReadTimeout: 3 * time.Second, WriteTimeout: time.Second, EnquireLink: time.Second}))

		conn, resp := bind(t, addr, "sys", "pw", pdu.Transceiver)
		require.Equal(t, data.ESME_ROK, resp.CommandStatus)
		waitForSessions(t, srv, "sys", 1)

		dl := pdu.NewDeliverSM()
		require.NoError(t, srv.SubmitToSystemID(context.Background(), "sys", dl))

		// The PDU must actually reach the wire, not just be accepted.
		require.NoError(t, conn.SetDeadline(time.Now().Add(testBindWait)))
		got, err := pdu.Parse(conn)
		require.NoError(t, err)
		require.IsType(t, &pdu.DeliverSM{}, got)
	})

	t.Run("unknownSystemID", func(t *testing.T) {
		srv, _ := startTestServer(t, ServerSettings{})
		require.ErrorIs(t, srv.SubmitToSystemID(context.Background(), "never-registered", pdu.NewDeliverSM()),
			ErrNoLiveSession)
	})

	t.Run("registeredButNeverBound", func(t *testing.T) {
		srv, _ := startTestServer(t, ServerSettings{})
		require.NoError(t, srv.StartSystemID(SystemIDConfig{SystemID: "sys", Password: "pw", BindingTypes: []pdu.BindingType{pdu.Transceiver}, ReadTimeout: 3 * time.Second, WriteTimeout: time.Second, EnquireLink: time.Second}))

		require.ErrorIs(t, srv.SubmitToSystemID(context.Background(), "sys", pdu.NewDeliverSM()),
			ErrNoLiveSession)
	})

	t.Run("allSessionsClosed", func(t *testing.T) {
		srv, addr := startTestServer(t, ServerSettings{})
		require.NoError(t, srv.StartSystemID(SystemIDConfig{SystemID: "sys", Password: "pw", BindingTypes: []pdu.BindingType{pdu.Transceiver}, ReadTimeout: 3 * time.Second, WriteTimeout: time.Second, EnquireLink: time.Second}))

		_, resp := bind(t, addr, "sys", "pw", pdu.Transceiver)
		require.Equal(t, data.ESME_ROK, resp.CommandStatus)
		waitForSessions(t, srv, "sys", 1)

		srv.StopSystemID("sys")

		require.ErrorIs(t, srv.SubmitToSystemID(context.Background(), "sys", pdu.NewDeliverSM()),
			ErrNoLiveSession,
			"a system ID whose sessions have all closed must be distinguishable from a wire failure")
	})

	t.Run("sessionWithoutTransceivable", func(t *testing.T) {
		// addSession runs before startIO stores the transceivable, so a session
		// can be in the map with no write path. That is not submittable.
		srv := NewServer(ServerSettings{Address: "127.0.0.1:0"})
		require.NoError(t, srv.StartSystemID(SystemIDConfig{SystemID: "sys", Password: "pw", BindingTypes: []pdu.BindingType{pdu.Transceiver}, MaxSessions: 2, ReadTimeout: 0, WriteTimeout: 0, EnquireLink: 0}))

		client, peer := net.Pipe()
		defer func() { _ = peer.Close() }()

		sess := newServerSession(client, srv)
		sess.systemID = "sys"
		srv.addSession("sys", sess)

		require.Nil(t, sess.trx.Load(), "precondition: no transceivable yet")
		require.False(t, sess.closed.Load(), "precondition: session is not closed")

		require.ErrorIs(t, srv.SubmitToSystemID(context.Background(), "sys", pdu.NewDeliverSM()),
			ErrNoLiveSession)
	})

	t.Run("skipsClosedSessionForLiveOne", func(t *testing.T) {
		srv, addr := startTestServer(t, ServerSettings{})
		require.NoError(t, srv.StartSystemID(SystemIDConfig{SystemID: "sys", Password: "pw", BindingTypes: []pdu.BindingType{pdu.Transceiver}, ReadTimeout: 3 * time.Second, WriteTimeout: time.Second, EnquireLink: time.Second}))

		// Two sessions, one of which is then closed. Selection must land on the
		// survivor regardless of map iteration order.
		_, resp := bind(t, addr, "sys", "pw", pdu.Transceiver)
		require.Equal(t, data.ESME_ROK, resp.CommandStatus)
		liveConn, resp := bind(t, addr, "sys", "pw", pdu.Transceiver)
		require.Equal(t, data.ESME_ROK, resp.CommandStatus)
		waitForSessions(t, srv, "sys", 2)

		// Close whichever session is not the one we hold the client end of.
		srv.mu.Lock()
		var toClose *serverSession
		for sess := range srv.sessions["sys"] {
			if sess.conn.RemoteAddr().String() != liveConn.LocalAddr().String() {
				toClose = sess
				break
			}
		}
		srv.mu.Unlock()
		require.NotNil(t, toClose, "expected to find the other session")
		toClose.close()

		require.NoError(t, srv.SubmitToSystemID(context.Background(), "sys", pdu.NewDeliverSM()))

		require.NoError(t, liveConn.SetDeadline(time.Now().Add(testBindWait)))
		got, err := pdu.Parse(liveConn)
		require.NoError(t, err)
		require.IsType(t, &pdu.DeliverSM{}, got)
	})

	t.Run("concurrentSubmitsDoNotStallSessionChurn", func(t *testing.T) {
		srv, l := pipeServer(t, ServerSettings{}, 0)
		require.NoError(t, srv.StartSystemID(SystemIDConfig{SystemID: "sys", Password: "pw"}))
		conn := pipeDial(t, l)
		bindPipe(t, conn, pdu.Transceiver, 0x34)
		require.NoError(t, conn.SetDeadline(time.Time{}))
		waitForSessions(t, srv, "sys", 1)
		srv.mu.Lock()
		var out *transmittable
		for s := range srv.sessions["sys"] {
			out = s.trx.Load().out
		}
		srv.mu.Unlock()
		require.NotNil(t, out)
		// The peer does not read. One PDU blocks in Write and a second fills
		// the queue, so the third submit must wait for capacity.
		require.NoError(t, srv.SubmitToSystemID(context.Background(), "sys", pdu.NewDeliverSM()))
		require.NoError(t, srv.SubmitToSystemID(context.Background(), "sys", pdu.NewDeliverSM()))
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		submitted := make(chan error, 1)
		go func() { submitted <- srv.SubmitToSystemID(ctx, "sys", pdu.NewDeliverSM()) }()
		await(t, func() bool { return atomic.LoadInt32(&out.pendingWrite) == 1 && len(out.input) == 1 })
		done := make(chan struct{})
		go func() { _ = srv.SystemIDs(); _ = srv.GetSystemIDStatus("sys"); close(done) }()
		select {
		case <-done:
		case <-time.After(100 * time.Millisecond):
			t.Fatal("session enumeration blocked behind in-flight submit")
		}
		cancel()
		select {
		case err := <-submitted:
			require.ErrorIs(t, err, context.Canceled)
		case <-time.After(time.Second):
			t.Fatal("cancelled submit did not return")
		}
	})
}

func TestServerSystemIDLength(t *testing.T) {
	for _, n := range []int{15, 16} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			srv := NewServer(ServerSettings{Address: "127.0.0.1:0", SystemID: strings.Repeat("x", n)})
			err := srv.Start()
			defer srv.Stop()
			if n == 15 {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestServerTLSListener(t *testing.T) {
	serverConfig, clientConfig := testTLSConfigs(t)
	srv, addr := startTestServer(t, ServerSettings{TLSConfig: serverConfig})
	require.NoError(t, srv.StartSystemID(SystemIDConfig{SystemID: "sys", Password: "pw"}))
	raw, err := net.DialTimeout("tcp", addr, testBindWait)
	require.NoError(t, err)
	defer func() { _ = raw.Close() }()
	require.NoError(t, raw.SetDeadline(time.Now().Add(testBindWait)))
	client := tls.Client(raw, clientConfig)
	require.NoError(t, client.Handshake())
	response := bindPipe(t, client, pdu.Transceiver, 0x34)
	require.Zero(t, binary.BigEndian.Uint32(response[8:12]))
	srv.Stop()
}
