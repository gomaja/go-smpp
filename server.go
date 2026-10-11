package gosmpp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gomaja/go-smpp/data"
	"github.com/gomaja/go-smpp/pdu"
)

// ErrNoLiveSession indicates that a system ID has no session able to accept a
// PDU: either it has no sessions at all, or every session it has is closed.
// SubmitToSystemID returns it so callers can distinguish "nothing to submit to"
// from a submit that reached a session and failed.
var ErrNoLiveSession = errors.New("no live session for system id")

// ErrServerNotInitialized indicates that NewServer was not used to create the server.
var ErrServerNotInitialized = errors.New("server must be created with NewServer")

// UnsupportedOptionalParametersError indicates that an older peer cannot receive
// a PDU's optional parameters. Stripped is true only for handler responses whose
// optional parameters were removed; submitted PDUs are rejected without sending.
type UnsupportedOptionalParametersError struct {
	SystemID         string
	InterfaceVersion byte
	Stripped         bool
}

func (e *UnsupportedOptionalParametersError) Error() string {
	return fmt.Sprintf("system ID %q: interface version %#x cannot receive optional parameters (stripped=%t)", e.SystemID, e.InterfaceVersion, e.Stripped)
}

// ServerSessionInfo identifies the bound session that supplied an application PDU.
type ServerSessionInfo struct {
	SystemID         string
	BindingType      pdu.BindingType
	InterfaceVersion byte
	LocalAddr        net.Addr
	RemoteAddr       net.Addr
}

// ServerPDUHandler handles an application PDU permitted in the session's bind state.
// SMPP v3.4 §§2.5–2.6 and v5 §2.4 restrict inbound data_sm to TX/TRX
// and inbound data_sm_resp to RX/TRX.
// Its response is written before closing when closeBind is true.
// ServerPDUHandler callbacks may call Stop or StopSystemID to initiate shutdown.
// The callback must return before cleanup finishes; an external call joins it.
type ServerPDUHandler func(ServerSessionInfo, pdu.PDU) (resp pdu.PDU, closeBind bool)

// ServerSettings configures an SMSC listener and its bind exchange.
type ServerSettings struct {
	// Address is the TCP listen address.
	Address string
	// SystemID identifies this SMSC; it may contain at most 15 octets, without NUL.
	SystemID string
	// TLSConfig optionally enables TLS on the listener.
	TLSConfig *tls.Config
	// BindTimeout bounds the entire bind exchange; non-positive uses DefaultBindTimeout.
	// Enquiries, generic nacks and intact unknown commands share a limit of eight
	// pre-bind messages; none extends the deadline. Unknown commands receive a nack.
	BindTimeout time.Duration
	// OnError receives bind, accept and bound I/O errors without passwords.
	// Pre-bind unknown commands report their first occurrence and one aggregate
	// with control counts when the handshake ends. Its terminal error is included
	// in that aggregate; intentional shutdown suppresses the final diagnostic.
	// OnError callbacks may call Stop or StopSystemID to initiate shutdown.
	// The callback must return before cleanup finishes; an external call joins it.
	OnError ErrorCallback
}

// DefaultBindTimeout is the bind exchange deadline applied when
// ServerSettings.BindTimeout or the client connector bind timeout is not set.
const DefaultBindTimeout = 30 * time.Second

// SystemIDConfig configures an ESME account. Empty BindingTypes allows TX, RX
// and TRX; zero MaxSessions is unlimited. Non-positive timeouts disable deadlines.
// OnPDU handles traffic other than enquire_link, unbind and repeated binds.
// Callbacks must return for external Stop and StopSystemID calls to finish.
type SystemIDConfig struct {
	// SystemID is the required, non-empty ESME account identifier.
	SystemID string
	// Password is compared against the bind password in constant time.
	Password string
	// BindingTypes permits these bind types; empty permits TX, RX and TRX.
	BindingTypes []pdu.BindingType
	// MaxSessions limits bound sessions and reserved handshakes; zero is unlimited.
	MaxSessions int
	// ReadTimeout bounds idle reads; non-positive disables the deadline.
	ReadTimeout time.Duration
	// WriteTimeout bounds each write; non-positive disables the deadline.
	WriteTimeout time.Duration
	// EnquireLink is the server keep-alive interval; non-positive disables it.
	EnquireLink time.Duration
	// OnPDU handles application PDUs, optionally responding and then closing.
	OnPDU ServerPDUHandler
}

type credentials struct {
	config SystemIDConfig
	// Includes reserved handshakes until their connection goroutine exits.
	currentSession int
}

// Server accepts and manages SMPP v3.4 ESME sessions.
// A Server may be started once and cannot be restarted after Stop.
// NewServer is required. A zero Server's error-returning methods report
// ErrServerNotInitialized; accessors return empty values and shutdown is a no-op.
type Server struct {
	callbacks callbackScope
	settings  ServerSettings
	mu        sync.Mutex
	listener  net.Listener
	started   bool
	stopped   bool
	stopDone  chan struct{}
	wg        sync.WaitGroup
	ctx       context.Context
	cancel    context.CancelFunc
	active    atomic.Bool
	credStore map[string]*credentials
	sessions  map[string]map[*serverSession]struct{}
	// Tracks both handshaking and bound connections until cleanup completes.
	connections map[*serverSession]struct{}
}

// NewServer creates a server without opening its listener.
func NewServer(settings ServerSettings) *Server {
	ctx, cancel := context.WithCancel(context.Background())
	return &Server{settings: settings, ctx: ctx, cancel: cancel, stopDone: make(chan struct{}),
		credStore: make(map[string]*credentials), sessions: make(map[string]map[*serverSession]struct{}), connections: make(map[*serverSession]struct{})}
}

// StartSystemID registers an ESME account, rejecting invalid or duplicate settings.
func (srv *Server) StartSystemID(config SystemIDConfig) error {
	if srv.ctx == nil {
		return ErrServerNotInitialized
	}
	if config.SystemID == "" {
		return errors.New("system ID is required")
	}
	if config.MaxSessions < 0 {
		return errors.New("MaxSessions must not be negative")
	}
	if config.ReadTimeout > 0 && config.EnquireLink >= config.ReadTimeout {
		return errors.New("EnquireLink must be less than ReadTimeout")
	}
	for _, bt := range config.BindingTypes {
		switch bt {
		case pdu.Transmitter, pdu.Receiver, pdu.Transceiver:
		default:
			return fmt.Errorf("unknown binding type %d", bt)
		}
	}
	config.BindingTypes = append([]pdu.BindingType(nil), config.BindingTypes...)
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if srv.stopped {
		return errors.New("server is stopped")
	}
	if _, ok := srv.credStore[config.SystemID]; ok {
		return fmt.Errorf("systemID %v is already started", config.SystemID)
	}
	srv.credStore[config.SystemID] = &credentials{config: config}
	return nil
}

// StopSystemID removes an account and closes its bound and reserved connections.
// External callers wait for their workers and callbacks. Inside this server's
// callback, or when joining would complete a wait cycle between instances, it
// initiates shutdown and returns; remaining callbacks can run during cleanup.
// One-way calls from another instance join. Application goroutines are external
// and their waits are not tracked. A subsequent external call joins completion.
// Cycle detection includes all workers in the Server. Stopping one account can
// therefore return conservatively when another account forms a Server wait cycle.
func (srv *Server) StopSystemID(systemID string) {
	if srv.ctx == nil {
		return
	}
	srv.mu.Lock()
	delete(srv.credStore, systemID)
	var sessions []*serverSession
	for s := range srv.connections {
		if s.systemID == systemID {
			sessions = append(sessions, s)
		}
	}
	// Remove visibility immediately; cleanup still owns each reserved slot.
	delete(srv.sessions, systemID)
	srv.mu.Unlock()
	for _, s := range sessions {
		s.abort()
	}
	for _, s := range sessions {
		if !joinCallbackScope(&srv.callbacks, s.finished) {
			return
		}
	}
}

// Start opens the TCP listener, optionally with TLS, and accepts connections.
func (srv *Server) Start() error {
	if srv.ctx == nil {
		return ErrServerNotInitialized
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if srv.started || srv.stopped {
		return errors.New("server already started or stopped")
	}
	// SMPP v3.4 §§4.1.2, 4.1.4, 4.1.6: SMSC system_id is a
	// C-Octet String of at most 16 octets, including the terminator.
	if len(srv.settings.SystemID) > 15 || strings.ContainsRune(srv.settings.SystemID, '\x00') {
		return errors.New("SMSC system ID must be at most 15 octets without NUL")
	}
	if cfg := srv.settings.TLSConfig; cfg != nil && len(cfg.Certificates) == 0 && cfg.GetCertificate == nil && cfg.GetConfigForClient == nil {
		return errors.New("tls: neither Certificates, GetCertificate, nor GetConfigForClient set in Config")
	}
	ln, err := net.Listen("tcp", srv.settings.Address)
	if err != nil {
		return err
	}
	if srv.settings.TLSConfig != nil {
		ln = tls.NewListener(ln, srv.settings.TLSConfig.Clone())
	}
	srv.listener = ln
	srv.started = true
	srv.active.Store(true)
	srv.wg.Add(1)
	go srv.acceptLoop()
	return nil
}

// Addr returns the listener address, or nil before a successful Start.
func (srv *Server) Addr() net.Addr {
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if srv.listener == nil {
		return nil
	}
	return srv.listener.Addr()
}

// Stop closes all connections and waits for workers and application callbacks.
// Concurrent external calls join the same shutdown. Inside this server's callback,
// or when joining would complete a wait cycle between instances, Stop initiates
// shutdown and returns; remaining callbacks can run during cleanup. The calling
// callback must return. One-way calls from another instance join. Application
// goroutines are external and their waits are not tracked.
// Cycle detection includes all workers in the Server as one lifecycle target.
func (srv *Server) Stop() {
	if srv.ctx == nil {
		return
	}
	srv.mu.Lock()
	if srv.stopped {
		srv.mu.Unlock()
		joinCallbackScope(&srv.callbacks, srv.stopDone)
		return
	}
	srv.stopped = true
	srv.active.Store(false)
	srv.cancel()
	ln := srv.listener
	sessions := make([]*serverSession, 0, len(srv.connections))
	for s := range srv.connections {
		sessions = append(sessions, s)
	}
	srv.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
	}
	for _, s := range sessions {
		s.abort()
	}
	go func() {
		defer close(srv.stopDone)
		defer registerCallbackWorker(&srv.callbacks)()
		srv.wg.Wait()
	}()
	joinCallbackScope(&srv.callbacks, srv.stopDone)
}

const (
	acceptRetryDelayMin = 5 * time.Millisecond
	acceptRetryDelayMax = time.Second
)

func (srv *Server) acceptLoop() {
	defer srv.wg.Done()
	defer registerCallbackWorker(&srv.callbacks)()
	var retryDelay time.Duration
	for srv.active.Load() {
		conn, err := srv.listener.Accept()
		if err != nil {
			if !srv.active.Load() || errors.Is(err, net.ErrClosed) {
				return
			}
			srv.reportWorkerError(err)
			if retryDelay == 0 {
				retryDelay = acceptRetryDelayMin
			} else {
				retryDelay = min(retryDelay*2, acceptRetryDelayMax)
			}
			timer := time.NewTimer(retryDelay)
			select {
			case <-srv.ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			continue
		}
		retryDelay = 0
		sess := newServerSession(conn, srv)
		srv.mu.Lock()
		if srv.stopped {
			srv.mu.Unlock()
			_ = conn.Close()
			return
		}
		srv.connections[sess] = struct{}{}
		srv.wg.Add(1)
		srv.mu.Unlock()
		go srv.handleConnection(sess)
	}
}

func (srv *Server) reportError(err error) {
	if srv.settings.OnError != nil {
		invokeExternalCallback(&srv.callbacks, func() { srv.settings.OnError(err) })
	}
}

func (srv *Server) reportWorkerError(err error) {
	if srv.settings.OnError != nil {
		invokeCallback(&srv.callbacks, func() { srv.settings.OnError(err) })
	}
}

func (srv *Server) handleConnection(s *serverSession) {
	defer srv.wg.Done()
	defer registerCallbackWorker(&srv.callbacks)()
	defer func() {
		s.close()
		if trx := s.trx.Load(); trx != nil {
			trx.stop(ExplicitClosing)
			<-trx.done
		}
		srv.mu.Lock()
		if s.account != nil {
			s.account.currentSession--
			s.account = nil
		}
		delete(srv.sessions[s.systemID], s)
		if len(srv.sessions[s.systemID]) == 0 {
			delete(srv.sessions, s.systemID)
		}
		delete(srv.connections, s)
		srv.mu.Unlock()
		close(s.finished)
	}()
	if err := s.handleBind(); err != nil {
		if srv.active.Load() && !s.closing.Load() && !s.bindDiagnosticReported {
			srv.reportWorkerError(err)
		}
		return
	}
	// The response must reach the wire before any I/O daemon starts, and a
	// session must have running I/O before becoming visible to callers.
	if err := s.startIO(); err != nil {
		s.reportIOError(err)
		return
	}
	if !srv.addSession(s.systemID, s) {
		return
	}
	<-s.done
}

func (srv *Server) addSession(systemID string, s *serverSession) bool {
	srv.mu.Lock()
	defer srv.mu.Unlock()
	trx := s.trx.Load()
	if srv.stopped || s.closed.Load() || trx == nil || !trx.isAlive() || s.account == nil || srv.credStore[systemID] != s.account {
		return false
	}
	select {
	case <-trx.started:
	default:
		return false
	}
	if srv.sessions[systemID] == nil {
		srv.sessions[systemID] = make(map[*serverSession]struct{})
	}
	srv.sessions[systemID][s] = struct{}{}
	return true
}

// GetSystemIDStatus returns a snapshot of the system ID's bound sessions.
func (srv *Server) GetSystemIDStatus(systemID string) []SystemIDStatus {
	srv.mu.Lock()
	defer srv.mu.Unlock()
	var statuses []SystemIDStatus
	for s := range srv.sessions[systemID] {
		status := SessionActive
		if s.closed.Load() {
			status = SessionClosed
		}
		statuses = append(statuses, SystemIDStatus{SystemID: s.systemID, LocalAddr: s.conn.LocalAddr(), RemoteAddr: s.conn.RemoteAddr(), Status: status, BindingType: s.bindingType})
	}
	return statuses
}

// SystemIDs returns a snapshot of system IDs with at least one bound session.
func (srv *Server) SystemIDs() []string {
	srv.mu.Lock()
	defer srv.mu.Unlock()
	ids := make([]string, 0, len(srv.sessions))
	for id := range srv.sessions {
		ids = append(ids, id)
	}
	return ids
}

// SubmitToSystemID queues a PDU on an arbitrary live RX or TRX session.
// A successful return means queued; asynchronous write errors go to OnError.
// Optional parameters require a peer supporting v3.4. The context bounds waiting
// for queue capacity, not delivery or the subsequent socket write.
func (srv *Server) SubmitToSystemID(ctx context.Context, systemID string, p pdu.PDU) error {
	if srv.ctx == nil {
		return ErrServerNotInitialized
	}
	if p == nil || (reflect.ValueOf(p).Kind() == reflect.Pointer && reflect.ValueOf(p).IsNil()) {
		return errors.New("cannot submit a nil PDU")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("submit to system ID %q: %w", systemID, err)
	}
	needsOptional := hasOptionalParameters(p)
	srv.mu.Lock()
	var target *serverSession
	var older *serverSession
	for s := range srv.sessions[systemID] {
		// SMPP v3.4 §2.2 and §4.6: SMSC delivery requires an RX or TRX bind.
		if !s.closed.Load() && (s.bindingType == pdu.Receiver || s.bindingType == pdu.Transceiver) {
			if needsOptional && s.interfaceVersion < data.SMPP_V34 {
				older = s
				continue
			}
			target = s
			break
		}
	}
	srv.mu.Unlock()
	if target == nil {
		if older != nil {
			return &UnsupportedOptionalParametersError{SystemID: systemID, InterfaceVersion: older.interfaceVersion}
		}
		return ErrNoLiveSession
	}
	buf := pdu.NewBuffer(nil)
	p.Marshal(buf)
	if err := buf.Err(); err != nil {
		return fmt.Errorf("submit to system ID %q: %w", systemID, &pdu.EncodeError{PDU: p, Err: err})
	}
	if err := target.trx.Load().SubmitContext(ctx, p); err != nil {
		return fmt.Errorf("submit to system ID %q: %w", systemID, err)
	}
	return nil
}

type serverSession struct {
	conn             net.Conn
	connection       *Connection // One writer and retained reader for the whole exchange.
	server           *Server
	systemID         string
	bindingType      pdu.BindingType
	interfaceVersion byte
	// Written and released under server.mu; retained across account replacement.
	account  *credentials
	config   SystemIDConfig
	trx      atomic.Pointer[transceivable]
	closed   atomic.Bool
	closing  atomic.Bool // Intentional shutdown, distinct from an I/O failure.
	done     chan struct{}
	finished chan struct{}
	// Written by the handshake worker before handleConnection resumes.
	bindDiagnosticReported bool
}

func newServerSession(conn net.Conn, srv *Server) *serverSession {
	return &serverSession{conn: conn, server: srv, done: make(chan struct{}), finished: make(chan struct{})}
}

func (srv *Server) bindTimeout() time.Duration {
	if srv.settings.BindTimeout > 0 {
		return srv.settings.BindTimeout
	}
	return DefaultBindTimeout
}

func (s *serverSession) reserveSlot(br *pdu.BindRequest) (data.CommandStatusType, string) {
	srv := s.server
	srv.mu.Lock()
	defer srv.mu.Unlock()
	account := srv.credStore[br.SystemID]
	if srv.stopped || account == nil {
		return data.ESME_RINVSYSID, "invalid system ID"
	}
	if !passwordsEqual(account.config.Password, br.Password) {
		return data.ESME_RINVPASWD, "invalid password"
	}
	allowed := len(account.config.BindingTypes) == 0
	for _, bt := range account.config.BindingTypes {
		if bt == br.BindingType {
			allowed = true
			break
		}
	}
	if !allowed {
		return data.ESME_RBINDFAIL, "invalid binding type"
	}
	if account.config.MaxSessions > 0 && account.currentSession >= account.config.MaxSessions {
		return data.ESME_RBINDFAIL, "max Sessions reached"
	}
	account.currentSession++
	s.account = account
	s.config = account.config
	s.systemID = br.SystemID
	s.bindingType = br.BindingType
	s.interfaceVersion = br.InterfaceVersion
	return data.ESME_ROK, ""
}

func (s *serverSession) handleBind() (bindErr error) {
	if err := s.conn.SetDeadline(time.Now().Add(s.server.bindTimeout())); err != nil {
		return s.newServerBindError("set bind deadline", err)
	}
	s.connection = NewConnection(s.conn)
	// Bound Open-state work independently of the absolute bind deadline. The
	// limit is local policy; SMPP specifies no pre-bind traffic count limit.
	controls := 0
	var enquiries, nacks, unknowns int
	firstReported := false
	defer func() {
		// Permitted controls alone are not errors. Unknown commands and a
		// terminal failure after controls get one summary instead of per-frame
		// diagnostics. Include the rejected ninth control in these counts.
		if unknowns == 0 && (bindErr == nil || controls == 0) {
			return
		}
		s.server.mu.Lock()
		stopped := s.server.stopped
		s.server.mu.Unlock()
		if stopped || s.closing.Load() {
			return
		}
		reason := fmt.Sprintf("pre-bind control messages: enquire_link=%d generic_nack=%d unknown_command=%d", enquiries, nacks, unknowns)
		if bindErr != nil {
			reason += "; " + bindErr.Error()
		}
		s.bindDiagnosticReported = true
		s.server.reportWorkerError(s.newServerBindError(reason, bindErr))
	}()
	consumeControl := func() error {
		if controls >= 8 {
			return s.newServerBindError("pre-bind control message limit exceeded", nil)
		}
		controls++
		return nil
	}
	var br *pdu.BindRequest
	for br == nil {
		// Raw reads leave pipelined post-bind bytes on the socket. The retained
		// Connection's buffered reader takes ownership only after binding.
		frame := &frameReader{Reader: s.conn}
		p, err := pdu.Parse(frame)
		if err != nil {
			var decoded *pdu.DecodeError
			if errors.As(err, &decoded) {
				if decoded.Status == data.ESME_RINVCMDID {
					unknowns++
					if err := consumeControl(); err != nil {
						return err
					}
					// SMPP v3.4 §§3.3, 4.3: nack an intact unknown command.
					// Its fully consumed frame permits the bind wait to continue.
					replyErr := s.writeBindErrorResponse(pdu.NewGenericNack(), decoded.Status, decoded.Header.SequenceNumber, err)
					if !replyErr.ResponseSent {
						return replyErr
					}
					if !firstReported {
						firstReported = true
						s.server.reportWorkerError(replyErr)
					}
					continue
				}
				switch decoded.Header.CommandID {
				case data.BIND_RECEIVER, data.BIND_TRANSMITTER, data.BIND_TRANSCEIVER:
					// SMPP v3.4 §2.8: a body error uses its matching response,
					// with the parser's suggested error status and no body.
					response, _ := pdu.CreatePDUFromCmdID(data.CommandIDType(uint32(decoded.Header.CommandID) | 0x80000000))
					return s.writeBindErrorResponse(response, decoded.Status, decoded.Header.SequenceNumber, err)
				}
			} else if frame.n > 0 {
				// SMPP v3.4 §§4.3, 4.3.1: damaged framing gets RINVCMDLEN;
				// retain the sequence only when the full header was received.
				var sequence int32
				if frame.n == len(frame.header) {
					sequence = pdu.ParseHeader(frame.header).SequenceNumber
				}
				return s.writeBindErrorResponse(pdu.NewGenericNack(), data.ESME_RINVCMDLEN, sequence, err)
			}
			return s.newServerBindError(err.Error(), err)
		}
		// SMPP v5 §2.4 Table 2-1 permits enquiry and nack in Open
		// (https://smpp.org/SMPP_v5.pdf). v3.4 Issue 1.2 §2.3 Table 2-1
		// lists only bound states (https://smpp.org/SMPP_v3_4_Issue1_2.pdf).
		// Keep the original deadline across these Open-state exchanges.
		switch request := p.(type) {
		case *pdu.EnquireLink:
			enquiries++
			if err := consumeControl(); err != nil {
				return err
			}
			// v3.4 §4.11: echo the enquiry's sequence in its response.
			if _, err := s.connection.WritePDU(request.GetResponse()); err != nil {
				return s.newServerBindError("write enquire_link response", err)
			}
		case *pdu.GenericNack:
			nacks++
			if err := consumeControl(); err != nil {
				return err
			}
			// No server bind request is outstanding to correlate this nack.
		case *pdu.BindRequest:
			br = request
		default:
			return s.newServerBindError("first message is not bind request", errors.New("first message is not bind request"))
		}
	}
	status, reason := s.reserveSlot(br)
	resp := br.GetResponse().(*pdu.BindResp)
	// SMPP v3.4 §5.1.3: request command_status is unused. Derive the
	// response status solely from local bind validation.
	resp.CommandStatus = status
	if status == data.ESME_ROK {
		// SMPP v3.4 §§4.1.2, 4.1.4, 4.1.6: return the SMSC identifier.
		resp.SystemID = s.server.settings.SystemID
		// SMPP v3.4 §3.4 and §5.3.2.25: advertise v3.4 only to peers
		// supporting TLVs. §4.1.5 includes interface_version per erratum
		// SMPPV3.4-05Oct99-01 (incorporated in Issue 1.2).
		if br.InterfaceVersion >= data.SMPP_V34 {
			resp.RegisterOptionalParam(pdu.NewFieldUint8(pdu.TagScInterfaceVersion, data.SMPP_V34))
		}
	}
	_, err := s.connection.WritePDU(resp)
	if status != data.ESME_ROK {
		return s.newDecodedServerBindError(br, status, reason, err, err == nil)
	}
	if err != nil {
		return s.newDecodedServerBindError(br, status, "write bind response", err, false)
	}
	return nil
}

// SMPP v3.4 §§2.8, 4.3 and v5 §3.2.1.3 require header-only error
// responses (https://smpp.org/SMPP_v3_4_Issue1_2.pdf,
// https://smpp.org/SMPP_v5.pdf). No undecoded credentials are exposed.
func (s *serverSession) writeBindErrorResponse(response pdu.PDU, status data.CommandStatusType, sequence int32, cause error) *ServerBindError {
	response.SetCommandStatus(status)
	response.SetSequenceNumber(sequence)
	_, writeErr := s.connection.WritePDU(headerOnlyResponse{response})
	err := s.newServerBindError(cause.Error(), errors.Join(cause, writeErr))
	err.CommandStatus = status
	err.ResponseSent = writeErr == nil
	return err
}

func (s *serverSession) newServerBindError(reason string, err error) *ServerBindError {
	return &ServerBindError{LocalAddr: s.conn.LocalAddr(), RemoteAddr: s.conn.RemoteAddr(), Reason: reason, Err: err}
}
func (s *serverSession) newDecodedServerBindError(br *pdu.BindRequest, status data.CommandStatusType, reason string, err error, sent bool) *ServerBindError {
	e := s.newServerBindError(reason, err)
	e.SystemID = br.SystemID
	e.BindingType = br.BindingType
	e.CommandStatus = status
	e.BindDecoded = true
	e.ResponseSent = sent
	return e
}
func (s *serverSession) contextError(err error) error {
	return fmt.Errorf("system ID %q remote %s: %w", s.systemID, s.conn.RemoteAddr(), err)
}

func (s *serverSession) reportIOError(err error) {
	// Suppress transport-close errors caused by our own shutdown. A genuine
	// write failure can precede close but reach its callback afterward.
	if (s.closed.Load() || s.closing.Load()) && (errors.Is(err, net.ErrClosed) || errors.Is(err, io.ErrClosedPipe) || errors.Is(err, ErrConnectionClosing)) {
		return
	}
	s.server.reportWorkerError(s.contextError(err))
}

// SubmitError identifies a PDU that could not be written, including queued
// PDUs discarded during shutdown. ServerSettings.OnError receives this error
// wrapped with account and address context; errors.As retrieves the PDU.
type SubmitError struct {
	PDU pdu.PDU
	Err error
}

func (e *SubmitError) Error() string {
	return fmt.Sprintf("submit %T sequence %d: %v", e.PDU, e.PDU.GetSequenceNumber(), e.Err)
}
func (e *SubmitError) Unwrap() error { return e.Err }

func (s *serverSession) reportSubmitError(p pdu.PDU, err error) {
	s.server.reportWorkerError(s.contextError(&SubmitError{PDU: p, Err: err}))
}

func (s *serverSession) reportResponseError(p pdu.PDU, err error) {
	// A handler that stopped its account cannot respond afterward. Queued
	// application submissions still go through reportSubmitError unconditionally.
	if (s.closed.Load() || s.closing.Load()) && (errors.Is(err, net.ErrClosed) || errors.Is(err, io.ErrClosedPipe) || errors.Is(err, ErrConnectionClosing)) {
		return
	}
	s.reportSubmitError(p, err)
}

func (s *serverSession) startIO() error {
	if err := s.conn.SetDeadline(time.Time{}); err != nil {
		return err
	}
	settings := Settings{ReadTimeout: s.config.ReadTimeout, WriteTimeout: s.config.WriteTimeout, EnquireLink: s.config.EnquireLink,
		OnAllPDU: s.handlePDU,
		OnReceivingError: func(err error) {
			s.reportIOError(err)
		},
		OnSubmitError: func(p pdu.PDU, err error) {
			s.reportSubmitError(p, err)
			if !errors.Is(err, pdu.ErrEncode) && !errors.Is(err, ErrWindowsFull) {
				s.close()
			}
		},
		OnClosed: func(State) { s.close() }}
	if s.connection == nil {
		s.connection = NewConnection(s.conn)
	}
	trx := newTransceivable(s.connection, settings, nil)
	trx.callbacks.parent = &s.server.callbacks
	s.trx.Store(trx)
	trx.start()
	return nil
}

func (s *serverSession) handlePDU(p pdu.PDU) (pdu.PDU, bool) {
	var response pdu.PDU
	var closeBind bool
	// SMPP v3.4 §§4.11, 4.2 and §5.1.3: housekeeping is independent
	// of the application; an already-bound ESME receives ESME_RALYBND.
	switch request := p.(type) {
	case *pdu.EnquireLink:
		response = request.GetResponse()
	case *pdu.Unbind:
		response = request.GetResponse()
		closeBind = true
	case *pdu.BindRequest:
		r := request.GetResponse().(*pdu.BindResp)
		r.CommandStatus = data.ESME_RALYBND
		response = r
	default:
		if !s.permitsInbound(p) {
			if !p.CanResponse() {
				return nil, false
			}
			r := p.GetResponse()
			r.SetCommandStatus(data.ESME_RINVBNDSTS)
			response = headerOnlyResponse{r}
		} else if s.config.OnPDU != nil {
			info := ServerSessionInfo{SystemID: s.systemID, BindingType: s.bindingType, InterfaceVersion: s.interfaceVersion, LocalAddr: s.conn.LocalAddr(), RemoteAddr: s.conn.RemoteAddr()}
			response, closeBind = s.config.OnPDU(info, p)
		} else if p.CanResponse() {
			response = p.GetResponse()
			response.SetCommandStatus(data.ESME_RSYSERR)
		}
	}
	if response != nil {
		originalResponse := response
		var err error
		response, err = s.preparePDU(response)
		// Write the response synchronously so unbind_resp reaches the peer before
		// closing. The shared Connection serializes this with the write daemon.
		trx := s.trx.Load()
		if err == nil {
			_, err = trx.conn.writePDUWithTimeout(response, s.config.WriteTimeout)
		}
		if err != nil {
			s.reportResponseError(originalResponse, err)
			if !errors.Is(err, pdu.ErrEncode) {
				s.close()
				return nil, false
			}
			// SMPP v3.4 §2.8 and v5 §3.2.1.3: a response that cannot be encoded
			// is replaced by a header-only error reply, so the request is still
			// answered. The handler's closeBind decision stands: the bind closes
			// after the fallback is written when the handler asked for that.
			if p.CanResponse() {
				fallback := p.GetResponse()
				fallback.SetCommandStatus(data.ESME_RSYSERR)
				fallback.SetSequenceNumber(p.GetSequenceNumber())
				if _, fallbackErr := trx.conn.writePDUWithTimeout(headerOnlyResponse{fallback}, s.config.WriteTimeout); fallbackErr != nil {
					s.reportResponseError(fallback, fallbackErr)
					s.close()
					return nil, false
				}
			}
		}
	}
	if closeBind {
		s.closing.Store(true)
		s.close()
	}
	return nil, false
}

// abort interrupts transport I/O even if another closer is already sending
// TLS close_notify. Shutdown must not wait for an unresponsive peer to read it.
func (s *serverSession) abort() {
	s.closing.Store(true)
	if conn, ok := s.conn.(*tls.Conn); ok {
		_ = conn.NetConn().Close()
	}
	s.close()
}

func (s *serverSession) close() {
	if s.closed.CompareAndSwap(false, true) {
		_ = s.conn.Close()
		close(s.done)
	}
}

// SystemIDStatus describes a bound session at the instant it was inspected.
type SystemIDStatus struct {
	SystemID    string
	LocalAddr   net.Addr
	RemoteAddr  net.Addr
	Status      SessionStatus
	BindingType pdu.BindingType
}

func (s *serverSession) preparePDU(p pdu.PDU) (pdu.PDU, error) {
	// SMPP v3.4 §3.4 forbids sending optional parameters to older ESMEs,
	// including application responses and server-originated delivery PDUs.
	if p == nil || s.interfaceVersion >= data.SMPP_V34 {
		return p, nil
	}
	if !hasOptionalParameters(p) {
		return p, nil
	}
	b := pdu.NewBuffer(nil)
	p.Marshal(b)
	if err := b.Err(); err != nil {
		return nil, &pdu.EncodeError{PDU: p, Err: err}
	}
	copyPDU, err := pdu.Parse(bytes.NewReader(b.Bytes()))
	if err != nil {
		return nil, s.contextError(err)
	}
	copyPDU.ClearOptionalParameters()
	s.server.reportWorkerError(s.contextError(&UnsupportedOptionalParametersError{SystemID: s.systemID, InterfaceVersion: s.interfaceVersion, Stripped: true}))
	return copyPDU, nil
}

func hasOptionalParameters(p pdu.PDU) bool {
	return len(p.GetOptionalParameters()) != 0
}

// permitsInbound follows SMPP v3.4 §2.3 Table 2-1, clarified by §§2.5–2.7:
// data_sm and replace_sm require TX/TRX; data_sm_resp requires RX/TRX.
// These directions are confirmed by v5 §2.4 Table 2-1. Housekeeping requests
// are handled before this check.
func (s *serverSession) permitsInbound(p pdu.PDU) bool {
	switch p.(type) {
	case *pdu.SubmitSM, *pdu.SubmitMulti, *pdu.QuerySM, *pdu.CancelSM, *pdu.ReplaceSM, *pdu.DataSM:
		return s.bindingType == pdu.Transmitter || s.bindingType == pdu.Transceiver
	case *pdu.UnbindResp, *pdu.EnquireLinkResp, *pdu.GenericNack:
		return true
	case *pdu.DeliverSMResp, *pdu.DataSMResp:
		return s.bindingType == pdu.Receiver || s.bindingType == pdu.Transceiver
	default:
		return false
	}
}

type headerOnlyResponse struct{ pdu.PDU }

func (r headerOnlyResponse) Marshal(b *pdu.ByteBuffer) {
	// SMPP v5 §3.2.1.3: non-NULL status responses omit the body.
	h := r.GetHeader()
	h.CommandLength = data.PDU_HEADER_SIZE
	h.Marshal(b)
}

// passwordsEqual compares fixed-length digests of the two passwords, so
// neither the result nor the time taken reveals where they first differ or
// how long the configured password is. Comparing the raw bytes with
// subtle.ConstantTimeCompare would return early on a length mismatch.
//
// The digests exist only for the duration of the comparison and are never
// stored: SHA-256 equalizes the lengths here, it is not password storage, so
// a deliberately slow password hash would only add per-bind CPU cost.
func passwordsEqual(want, got string) bool {
	w := sha256.Sum256([]byte(want))
	g := sha256.Sum256([]byte(got))
	return subtle.ConstantTimeCompare(w[:], g[:]) == 1
}
