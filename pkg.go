package gosmpp

import (
	"io"
	"time"

	"github.com/gomaja/go-smpp/pdu"
)

// Transceiver interface.
type Transceiver interface {
	io.Closer
	// Submit queues a PDU. With request tracking, an outstanding sequence returns
	// ErrSequenceInUse before queueing. A full queue on this connection's own
	// writer callback, or a queue wait that would complete an instance wait
	// cycle, returns ErrSubmitQueueFull instead of blocking.
	// Application PDUs wait for window admission; a library-worker callback
	// receives ErrSubmitQueueFull instead of waiting for a full window.
	Submit(pdu.PDU) error
	SystemID() string
}

// Transmitter interface.
type Transmitter interface {
	io.Closer
	// Submit queues a PDU. With request tracking, an outstanding sequence returns
	// ErrSequenceInUse before queueing. A full queue on this connection's own
	// writer callback, or a queue wait that would complete an instance wait
	// cycle, returns ErrSubmitQueueFull instead of blocking.
	// Application PDUs wait for window admission; a library-worker callback
	// receives ErrSubmitQueueFull instead of waiting for a full window.
	Submit(pdu.PDU) error
	SystemID() string
}

// Receiver interface.
type Receiver interface {
	io.Closer
	SystemID() string
}

// Settings for TX (transmitter), RX (receiver), TRX (transceiver).
type Settings struct {
	// ReadTimeout is timeout for reading PDU from SMSC.
	// A non-positive value clears the underlying read deadline.
	// NewSession requires ReadTimeout > max(0, EnquireLink) to detect
	// connection failure; server accounts may disable the deadline.
	ReadTimeout time.Duration

	// WriteTimeout is timeout for submitting PDU.
	WriteTimeout time.Duration

	// EnquireLink periodically sends an untracked EnquireLink to SMSC, independent
	// of request-store availability. Its response uses OnUnexpectedPduResponse
	// with window tracking enabled. It retires the previous unanswered written
	// application enquiry; cleanup failures reach OnReceivingError.
	// The duration must not be smaller than 1 minute.
	//
	// Zero duration disables auto enquire link.
	EnquireLink time.Duration

	// OnPDU handles received PDU from SMSC.
	//
	// `Responded` flag indicates this pdu is responded automatically,
	// no manual respond needed.
	//
	// OnAllPDU takes precedence. With window tracking, this handles requests
	// when OnReceivedPduRequest is nil; responses use the window callbacks.
	OnPDU PDUCallback

	// OnAllPDU handles all received PDU from SMSC.
	//
	// This pdu is NOT responded to automatically,
	// manual response/handling is needed
	//
	// User can also decide to close bind by retuning true, default is false
	//
	// With window tracking, this handles requests when OnReceivedPduRequest
	// is nil; responses use the window callbacks. An unencodable response is
	// reported through OnSubmitError and replaced with a header-only RSYSERR
	// response; the returned closeBind decision still applies.
	OnAllPDU AllPDUCallback

	// OnReceivingError notifies happened error while reading PDU
	// from SMSC.
	OnReceivingError ErrorCallback

	// OnSubmitError reports an accepted PDU that could not be written. It may run
	// on the writer; a resubmit to its full queue returns ErrSubmitQueueFull.
	// Rejected Submit calls return their error directly without this callback.
	OnSubmitError PDUErrorCallback

	// OnRebindingError notifies error while rebinding.
	OnRebindingError ErrorCallback

	// OnClosed runs once when a connection closes, including ExplicitClosing
	// with automatic rebinding enabled. External Close waits for it to return.
	// Closing the same instance, or completing a wait cycle with other instances,
	// initiates shutdown without joining; remaining callbacks can run afterward.
	OnClosed ClosedCallback

	// OnRebind notifies a rebind event. Retrying from a library-worker callback
	// can return ErrSubmitQueueFull; check Submit's return and hand retry work to
	// an application goroutine with its own deadline.
	OnRebind RebindCallback

	// SMPP Bind Window tracking feature config
	*WindowedRequestTracking

	response func(pdu.PDU)
}

// WindowedRequestTracking settings for TX (transmitter) and TRX (transceiver) request store.
type WindowedRequestTracking struct {

	// OnReceivedPduRequest handles received PDU request from SMSC.
	//
	// User can also decide to close bind by retuning true, default is false
	OnReceivedPduRequest AllPDUCallback

	// OnExpectedPduResponse handles expected PDU response from SMSC.
	// Only triggered for a transmitted request found in the window cache.
	// Automatic keepalives are untracked and use OnUnexpectedPduResponse.
	//
	// Handle is optional
	// If not set, response will be dropped
	OnExpectedPduResponse func(Response)

	// OnUnexpectedPduResponse handles unexpected PDU response from SMSC.
	// Only triggered if the original request is not found in the window cache
	//
	// Handle is optional
	// If not set, response will be dropped
	OnUnexpectedPduResponse func(pdu.PDU)

	// OnExpiredPduRequest handles expired PDU request with no response received
	//
	// Mandatory: the PduExpireTimeOut must be set
	// Handle is optional
	// If not set, expired PDU will be removed from cache
	// the bind can be closed by retuning true on closeBind.
	OnExpiredPduRequest func(pdu.PDU) (closeBind bool)

	// OnClosePduRequest returns this session's transmitted, unanswered requests
	// on close, including those backend cleanup could not reach within its budget.
	// Each locally owned request completes once, unless already abandoned after a
	// store error. An external Close joins these callbacks. Retained cleanup
	// identities are removed silently later; a dead generation whose local claim
	// is still pending can also complete through recovery. Recovery uses a joined helper
	// away from the writer. Check Submit's return: when capacity is unavailable,
	// ErrSubmitQueueFull is returned synchronously without OnSubmitError. Hand
	// retry work to an application goroutine rather than blocking this callback.
	// Requests left by another
	// session after its final Close are removed silently from a shared store.
	OnClosePduRequest func(pdu.PDU)

	// Set the time duration to expire a request sent to the SMSC
	//
	// Zero duration disables pdu expire check, and the cache may fill up over time with expired PDU request
	// Recommended: equal or less to the value set in ReadTimeout + EnquireLink
	PduExpireTimeOut time.Duration

	// The time period between each check of the expired PDU in the cache
	//
	// Zero duration disables pdu expire check and the cache may fill up over time with expired PDU request
	// Recommended: Less or half the time set in for PduExpireTimeOut
	// Don't be too aggressive, there is a performance hit if the check is done often
	ExpireCheckTimer time.Duration

	// The maximum number of written, unanswered application requests. Pending
	// reservations do not count. Application EnquireLink bypasses this capacity
	// limit; the writer retires its previous unanswered written enquiry. Automatic
	// enquiries are untracked. Retirement produces no response, expiry or close
	// completion callback. A session retains at most 1024 uncertain cleanup
	// identities; healthy pending admissions do not count toward this bound.
	// Further tracked work returns ErrRequestCleanupFull until cleanup progresses.
	// Application queue admission also holds at most this many permits until
	// completion; callers wait for a permit before entering the outbound queue.
	// Sessions sharing a store share capacity; checks are advisory across
	// concurrent writers and during backend metadata failures.
	//
	// Maximum value is 255
	MaxWindowSize uint8

	// if enabled, EnquireLink and Unbind request will be responded to automatically
	EnableAutoRespond bool

	// Set the time duration to expire a request for storing or retrieving data from request store
	// Close and each recovery scan bound backend work by this duration; individual
	// operations have fresh deadlines limited by the remaining budget. Close uses
	// up to eight concurrent deletions. Its local completion callbacks run outside
	// that budget, and unfinished backend cleanup is retried silently.
	//
	// Value must be greater than 0
	// 200 to 1000 milliseconds is a good starting point
	StoreAccessTimeOut time.Duration
}
