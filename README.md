# go-smpp

[![Go Report Card](https://goreportcard.com/badge/github.com/gomaja/go-smpp)](https://goreportcard.com/report/github.com/gomaja/go-smpp)
[![pkg.go.dev](https://pkg.go.dev/badge/github.com/gomaja/go-smpp.svg)](https://pkg.go.dev/github.com/gomaja/go-smpp)

SMPP (3.4) client and SMSC server library in pure Go.

## Installation
```
go get github.com/gomaja/go-smpp@main
```

## Versioning

The module requires Go 1.26 or later and is tested on the Go releases that are
still supported upstream (currently 1.26 and 1.27); the minimum moves with them.

This module publishes no releases. `main` is the only supported line, and its
API changes whenever a better design is found, without a deprecation period.
Go records the commit you build against as a pseudo-version in your `go.mod`;
upgrade by running the command above again.

The former tags `v1.0.0` and `v1.0.1` are retracted, so `go get` warns about
them and `@latest` resolves to the tip of `main`.

## Validation

The repository runs no hosted CI. Every check runs locally with one command,
before each push and merge:

```
scripts/validate.sh            # everything below
scripts/validate.sh --fast     # skip race, extra toolchains and CodeQL
scripts/validate.sh --fuzz 30s # also run each fuzz target for 30s
```

It checks formatting and whitespace, `go mod tidy`/`verify`, build, vet, tests
with and without `-race` on the host (linux/amd64), the other supported Go
toolchains, vet and test compilation for linux/arm64, 32-bit, big-endian,
Windows and macOS targets, staticcheck, golangci-lint, govulncheck, gitleaks
over the history and the working tree, shellcheck for the script itself, and
CodeQL with the `security-extended` and `security-and-quality` suites when the
`codeql` CLI is installed. Tool versions are pinned in the script. CodeQL
findings that were reviewed and accepted are listed in
`scripts/codeql-accepted.txt`.

## Usage

### Highlight

- go-smpp is written in event-based style and fully manages your SMPP session, connection, error handling, rebinding, and related hooks:

```go
		trans, err := gosmpp.NewSession(
		gosmpp.TRXConnector(gosmpp.NonTLSDialer, auth),
		gosmpp.Settings{
			EnquireLink: 5 * time.Second,

			ReadTimeout: 10 * time.Second,

			OnSubmitError: func(_ pdu.PDU, err error) {
				log.Fatal("SubmitPDU error:", err)
			},

			OnReceivingError: func(err error) {
				fmt.Println("Receiving PDU/Network error:", err)
			},

			OnRebindingError: func(err error) {
				fmt.Println("Rebinding but error:", err)
			},

			OnPDU: handlePDU(),

			OnClosed: func(state gosmpp.State) {
				fmt.Println(state)
			},
		}, 5*time.Second)
		if err != nil {
		  log.Println(err)
		}
		defer func() {
		  _ = trans.Close()
		}()
```

### Examples

Full client examples are available under [example](https://github.com/gomaja/go-smpp/blob/main/example).
Set `Auth.SMSC` to a reachable SMSC endpoint before running them.

### Session shutdown and request windows

`Session.Close()` rejects further submissions, interrupts transport I/O, stops
rebinding, and waits for every session worker and callback, including `OnClosed`.
On explicit shutdown it first attempts an unbind (SMPP v3.4 §4.2), with a
50-millisecond write deadline. It skips unbind when a write is active or an
earlier write left a partial frame or a permanent transport error, then
proceeds with shutdown. Encoding errors still permit graceful unbind. It does
not wait for unbind_resp.
`Connection.WritePDU` rejects writes with `ErrConnectionClosing` after shutdown
begins, a write leaves a partial frame, or a permanent transport error occurs,
including a writer that was waiting for transport access.
This also prevents another worker from appending a PDU to an incomplete frame
while the first write's error callback runs. A zero-byte write timeout leaves
framing intact and still permits a response before shutdown.
TLS transport closure may additionally wait for Go's close_notify timeout.
Concurrent external calls wait for the same completion. Calling `Close()` from
inside a callback belonging to that session initiates shutdown and returns. The
same applies when joining would create a wait cycle between instances, such as
two sessions whose `OnClosed` callbacks close each other. In either case, the calling
callback and other callbacks already running may finish afterward, and queued
submission errors, pending-request completion and store-error callbacks, and
`OnClosed` may still run during cleanup. Other callbacks due from in-flight
processing or rebinding may also begin before cleanup finishes. The calling
callback must return. A subsequent external call waits for cleanup. A call from
another instance's callback also waits unless it would complete a wait cycle.
Cycle detection considers every worker in the owning Session or Server. Closing
`Session.Transceiver()` or stopping one server account can therefore return
without joining when a different worker in that same Session or Server closes
the cycle, even if the selected connection could finish independently.
Goroutines started by the application are external callers; their own waits are
not tracked. The same contract applies to server `Stop()` and
`StopSystemID()` for the connections they stop. `OnClosed(ExplicitClosing)` runs
once even when automatic rebinding is enabled; explicit shutdown never rebinds.

Client bind exchanges use `DefaultBindTimeout` (30 seconds), covering both the
bind write and response read. Set `WithBindTimeout(duration)` on TX, RX or TRX
connectors to change it; non-positive values use the default. Successful exchanges
clear this deadline before session I/O. Built-in connectors also expose
`ConnectContext(context.Context)`, which session rebinding uses to interrupt bind
I/O on shutdown. A custom connector without that method must return from
`Connect()` before external `Close()` can finish. `Dialer.DialContext` accepts a
context; the built-in TCP dialer uses `net.Dialer.DialContext` and shutdown joins
that dial. Custom dialers should
honor cancellation. If one ignores it, the connector abandons that attempt and
closes a late connection; its dialing goroutine remains until the function
returns, although session shutdown completes without waiting for it.
During binding, both the client and server answer enquire_link and continue
waiting under the original bind deadline (SMPP v5 §2.4 Table 2-1 permits it in
Open; v3.4 §2.3 Table 2-1 lists it only in bound states). The server ignores
generic_nack while awaiting the first bind. On the client, a
generic_nack with the bind sequence or sequence zero fails with `BindError`
carrying the peer's status (SMPP v3.4 §4.3.1).
The server permits at most eight pre-bind control PDUs in total: enquire_link,
generic_nack and unknown commands. A ninth closes the connection, and these
exchanges never extend the bind deadline. Unknown commands receive generic_nack
with `ESME_RINVCMDID` before the server continues waiting. A malformed bind
receives its matching header-only bind response with the decode status, then
closes; lost framing receives generic_nack with `ESME_RINVCMDLEN`, then closes.
For interoperability, the server tolerates a nonzero request command_status;
authentication alone determines the bind response status. The same Connection
uses raw reads throughout the handshake and allocates its 128 KiB read buffer
only on the first buffered Read after binding. Idle unauthenticated connections
therefore retain no read buffer. Unknown-command diagnostics report the first
occurrence immediately and one aggregate when the handshake ends, with counts
for enquiries, generic nacks and unknown commands (including the rejected ninth
control). Intentional shutdown suppresses the final aggregate.

Window entries are reserved during `Submit`, before queue admission and writing,
and removed independently of response callbacks. Reusing an outstanding sequence
returns `ErrSequenceInUse` without replacing its request or sending another PDU.
`TimeSent` records reservation time and is available even to an immediate response;
`WrittenAt` records completed writing. A reservation becomes eligible for response
correlation immediately before its transport write begins. A response to a request
still waiting for admission goes to `OnUnexpectedPduResponse` and preserves the
reservation. Pending writes have zero `WrittenAt` and
cannot expire; expiry measures age from `WrittenAt`. `generic_nack` completes its matching entry. Each reservation belongs
to its sending connection and session lineage; exactly one response, expiry or close operation may
complete it. Conditional removal and the local ledger arbitrate completion. A
response that finds its row already removed can complete the local request if
expiry has not claimed it yet. Store operations have bounded deadlines; application
callbacks consume none of that deadline. Close bounds backend cleanup by one
`StoreAccessTimeOut`, with up to eight concurrent deletions and a fresh deadline
for each operation, limited by the remaining budget. It completes every locally
owned transmitted request that has not already completed or been abandoned,
even when the backend budget ends or List omits the row. `OnClosePduRequest`
runs before an external Close returns. Unfinished row cleanup retains its exact
identity for later silent recovery without occupying local window capacity.
Final Close has no background-scan lookup-count cap. The pre-write capacity check and
reservation share a store budget; the written marker gets a fresh budget after
transport I/O. Only written, unanswered application requests count against
`MaxWindowSize`; queued reservations do not. Tracked enquire_link bypasses that
limit and remains correlated. The next application or automatic enquiry retires
the previous unanswered application enquiry without a completion callback, keeping
at most one written application enquiry outstanding per connection. Its late
response is unexpected. Automatic keepalives always bypass the request store;
their responses use `OnUnexpectedPduResponse` when tracking is enabled. An error
retiring a previous enquiry reaches `OnReceivingError` and does not prevent the
keepalive from being sent. A live connection may expire reservations left
by a dead generation of the same session. Failed cleanup and a full window
trigger asynchronous recovery; ordinary writes do not scan and the writer never
waits for a scan. At most one scan runs at a time, with attempts limited to once
per `ExpireCheckTimer`, or once per second when that timer is disabled. A joined
helper invokes `OnClosePduRequest` away from the writer, so the callback can
resubmit. Each scan bounds backend work by one `StoreAccessTimeOut` and performs
at most 256 absence lookups across eligible lineages. Healthy scans start with
own-lineage lookups; when both own and foreign cleanup need lookups, each gets
up to half that count. After a scan exhausts its time budget, the next scan
rotates its starting phase, so own lookups, List cleanup and closed-lineage
lookups each get a fresh budget within three such scans. Each operation may use
the full remaining budget. List cleanup uses up to eight concurrent operations.
Identity, List-row, phase and background-lineage cursors survive rebinds and let
later scans proceed past unavailable rows. Periodic
checks also find finally closed lineages on the same store, even without a new
submission or an own-lineage hint. Recovery runs with expiry disabled. Expiry
checks shutdown before each backend operation and cancels in-progress operations
so Close does not wait for one timeout per expired row.
Sessions sharing a store share window capacity, but each session reports only
its own requests, including across rebinds. Once another session has completed
shutdown, its unrecoverable store entries may be removed silently. Capacity
checks are advisory across concurrent writers and while a backend metadata
update is unavailable. A fully written PDU whose `MarkWritten` update fails is
reported through `OnReceivingError`; the connection stays usable and recovery
retries the marker conditionally. A store Get deadline or
Delete error releases local written capacity and admission immediately because
the response has arrived. It reaches `OnReceivingError`; the exact reservation
is retained only for silent cleanup and no response/expiry/close callback follows.
A successful Get returning a row remains usable even if its deadline just
elapsed. A missing row without a store deadline instead completes through the
local ledger; response and expiry still deliver at most one completion callback.
Failed admissions are resolved when a bounded Get confirms their identity is
absent or has been replaced, so a Set that never committed cannot retain a
permanent cleanup hint. The most recent 1024 rejected identities remain as small
tombstones after absence settlement, so a late-committing Set is removed silently
during close or recovery. This retention is bounded: older rejections and rows
committed after final cleanup may require administrative removal.
Each session lineage retains at most 1024 uncertain
cleanup identities; healthy queued and admission-waiting requests, successful
cancellation rollbacks and identities retained after a close completion do not
consume that bound. Abandonment suppresses completion for as long as its local
cleanup identity remains, even after a small replay tombstone is evicted. Further
tracked submissions return `ErrRequestCleanupFull` and trigger throttled recovery
until space is reclaimed; automatic keepalives remain independent of this bound. Zero `PduExpireTimeOut` disables expiry. Inbound
requests use `OnAllPDU`, `OnPDU` or the default response path when the window's
`OnReceivedPduRequest` is nil. A malformed response is
reported through `OnReceivingError` and, when correlated, through
`OnExpectedPduResponse` with `Response.Err` set and a non-nil response containing
the received header. Check `Err` before treating it as a successful response.
A generic_nack rejecting one of our responses can share a sequence with an
unrelated outstanding request and complete that request: the peer's sequence
space overlaps ours, and generic_nack carries no rejected command ID.

Every accepted outbound PDU that cannot be written reaches `OnSubmitError`,
including queued PDUs discarded during shutdown. Such discarded submissions
carry `ErrConnectionClosing`. A successful `Submit` means queued, not written;
rejected submissions return their error directly. Reservations also include
queued submissions and callers waiting for queue admission. A canceled admission
removes its reservation and produces no completion callback. Calling `Submit` or
`SubmitContext` from the connection's own writer callback returns
`ErrSubmitQueueFull` when its queue is full. A blocking admission also returns
that error if it would complete a callback wait cycle with another instance;
otherwise callers retain queue backpressure. Queue waits register their
dependency until admission, cancellation or shutdown releases it.
Queue admission commits under the shutdown lock. A producer still waiting when
shutdown begins returns `ErrConnectionClosing`, even if the writer frees a queue
slot during cleanup; rejected submissions produce no completion callback.
Canceling a producer that was woken for an available slot passes that notification
to another waiter, so cancellation cannot strand a live producer.
Application submissions also wait for a window admission permit before queueing;
at most `MaxWindowSize` application requests are admitted until completion.
Reserved requests still waiting for admission consume no permit. This keeps
producers from outrunning responses and makes a full window exert backpressure.
Enquire_link bypasses admission permits. A callback on any library worker
cannot wait for a full application window and receives `ErrSubmitQueueFull`,
including when it forwards into another Session. Waiting for a silent peer
from a callback could otherwise prevent shutdown from completing. Application
goroutines spawned by callbacks retain ordinary admission backpressure. `SubmitContext` cancellation
also bounds waiting for a window permit. `GetWindowSize` reports written,
unanswered application requests, excluding queued reservations and enquire_link.
Encoding errors affect only
the offending PDU and leave the connection usable.
An unencodable handler response is reported and replaced with a matching,
header-only `ESME_RSYSERR` response using the request sequence; the handler's
`closeBind` decision still applies. Malformed alert_notification body fields are
reported without a reply, since that command has no response (v3.4 §2.8).

## SMPP server

Register ESME accounts and start a TCP listener. Empty `BindingTypes` permits
TX, RX and TRX; zero `MaxSessions` is unlimited. Zero timeouts disable deadlines.

```go
server := gosmpp.NewServer(gosmpp.ServerSettings{
    Address: "127.0.0.1:2775",
    SystemID: "smsc",
    OnError: func(err error) { log.Print(err) },
})
err := server.StartSystemID(gosmpp.SystemIDConfig{
    SystemID: "esme",
    Password: "secret",
    MaxSessions: 4,
    OnPDU: func(session gosmpp.ServerSessionInfo, request pdu.PDU) (pdu.PDU, bool) {
        if submit, ok := request.(*pdu.SubmitSM); ok {
            response := submit.GetResponse().(*pdu.SubmitSMResp)
            response.MessageID = "message-1"
            return response, false
        }
        return nil, false
    },
})
if err != nil { log.Fatal(err) }
if err = server.Start(); err != nil { log.Fatal(err) }
defer server.Stop()

// Delivery requires a live receiver or transceiver session.
ctx, cancel := context.WithTimeout(context.Background(), time.Second)
defer cancel()
err = server.SubmitToSystemID(ctx, "esme", pdu.NewDeliverSM())
if err != nil { log.Print(err) }
```

`Addr()` reports the listener address, including an assigned ephemeral port.
Set `TLSConfig` to a `*tls.Config` with certificates, `GetCertificate`, or
`GetConfigForClient` to accept TLS connections; the bind timeout also covers TLS
handshaking. `SystemID` is the SMSC identifier
returned to peers and may contain at most 15 octets, excluding the terminator.
ESME credential lengths are left to deployments.

The server always handles enquire_link, unbind and repeated binds. It checks
inbound PDUs against the bind state before calling `OnPDU`, rejecting disallowed
requests with `ESME_RINVBNDSTS` and dropping disallowed responses. The handler
receives `data_sm` only on TX/TRX binds and `data_sm_resp` only on RX/TRX binds
(SMPP v3.4 §§2.5–2.6; v5 §2.4 Table 2-1). It
receives a `ServerSessionInfo` value identifying the account, bind type, protocol
version and addresses. Without a handler, permitted requests receive
`ESME_RSYSERR` and responses are discarded. A handler returning `closeBind` closes after its response is
written. `StopSystemID` removes an account and closes its connections, while
`Stop()` closes all connections. External shutdown calls wait for handlers and
cleanup; calls inside callbacks initiate shutdown as described above. A stopped
server cannot restart.

`GetSystemIDStatus` includes the bind type and typed `SessionActive` or
`SessionClosed` status. `SystemIDs` lists accounts with bound sessions.
Submission success means queued, not delivered; track `deliver_sm_resp` to learn
the ESME's result. The context bounds waiting for queue capacity, not a later
socket write. PDUs with optional parameters require a live v3.4-or-later RX/TRX
session; otherwise `SubmitToSystemID` returns
`*gosmpp.UnsupportedOptionalParametersError` without sending. Handler responses
to older peers have optional parameters stripped, with a diagnostic in `OnError`.
Write failures also reach `OnError` with account and remote address context.
Use `errors.As(err, &submitError)` with `*gosmpp.SubmitError` to identify the PDU
that failed; its `Err` unwraps to the underlying error. This also covers queued
PDUs discarded when a connection closes.
Submitting a delivery receipt inside `OnPDU` for the message being handled can
put that receipt on the wire before the handler's `submit_sm_resp`; applications
requiring response-first ordering must coordinate it themselves.
See [specification compliance](docs/spec-compliance.md).

## Supported PDUs

- [x] bind_transmitter
- [x] bind_transmitter_resp
- [x] bind_receiver
- [x] bind_receiver_resp
- [x] bind_transceiver
- [x] bind_transceiver_resp
- [x] outbind
- [x] unbind
- [x] unbind_resp
- [x] submit_sm
- [x] submit_sm_resp
- [x] submit_sm_multi
- [x] submit_sm_multi_resp
- [x] data_sm
- [x] data_sm_resp
- [x] deliver_sm
- [x] deliver_sm_resp
- [x] query_sm
- [x] query_sm_resp
- [x] cancel_sm
- [x] cancel_sm_resp
- [x] replace_sm
- [x] replace_sm_resp
- [x] enquire_link
- [x] enquire_link_resp
- [x] alert_notification
- [x] generic_nack

## Breaking changes on main

`PDU.OptionalParameters` is now an ordered `[]pdu.Field` that retains repeated
TLVs. Replace `p.OptionalParameters[tag]` with `p.GetOptionalParam(tag)` for the
first occurrence, or `p.GetOptionalParams(tag)` for all occurrences. Indexing
by a tag still compiles but uses a slice index and may panic. In
`for index, field := range p.OptionalParameters`, the first value is an index;
read the tag from `field.Tag`. `RegisterOptionalParam` replaces the first
occurrence; `AppendOptionalParam` adds another in wire order.

Encoding failures are available through `ByteBuffer.Err()` and
`errors.Is(err, pdu.ErrEncode)`. `Connection.WritePDU` returns
`*pdu.EncodeError` with the offending PDU; `OnSubmitError` receives it while
the session stays connected. Check buffer errors before writing marshalled bytes.

Marshal and transports now check wire representability, allowing over-limit
received fields to be forwarded. Call `pdu.Validate(p)` for all SMPP v3.4
mandatory-field maxima; its joined `*pdu.ValidationError` values identify
fields and suggested response statuses. Setters retain their construction
limits. See [the receive/send policy](docs/spec-compliance.md#receive-and-send-length-policy).

Address does not know its containing PDU; call `pdu.Validate` before sending
to enforce the per-PDU 21/65-octet limits (including NUL). Address setters
accept up to 64 content octets for every PDU, and Marshal preserves them.
An address accepted by the setter can exceed submit_sm's maximum and be
rejected by a strict SMSC. This allows a transit SMSC to forward accepted traffic.

`ClearOptionalParameters` detaches the slice; later appends do not overwrite
an earlier slice returned by `GetOptionalParameters`.

`RequestStore.Delete` now takes the full `Request` and returns `(bool, error)`.
Implement it as an atomic comparison and removal: sequence number, `Owner` and `ID`
must match the stored reservation. Timestamps are metadata and must not affect
identity. `false, nil` means absent or
replaced. A successful removal must also claim the local ledger before invoking
a completion callback; a response finding the row absent can claim that ledger
before expiry does. Preserve these
values through `Set`, `Get`, `List`, and serialization. Store operations must be
safe concurrently and respect context cancellation; `List` should be complete
but may return stale or truncated snapshots. Recovery confirms each uncertain
identity with `Get` and conditionally deletes a matching row even if `List`
omitted it. Entries must remain until explicitly removed: cache
eviction must not discard outstanding reservations. `Clear` is administrative;
session cleanup uses conditional deletion of its own reservations.
Administrative `Clear` or direct removal of library-owned reservations requires
all sessions using that store to be closed first. Removing entries independently
of the library's completion path invalidates its admission and completion ledger.

`RequestStore.Set` must atomically insert only an absent sequence and return
`ErrSequenceInUse` for an existing reservation, leaving it unchanged.
A failed `Set` is followed by conditional cleanup because a remote backend may
have inserted the reservation before its acknowledgement failed.

`RequestStore.MarkWritten(ctx, request, sentAt)` must atomically set `WrittenAt`
only if the pending reservation still matches. Return `false, nil` if a response
already removed it or another reservation replaced it; never recreate an entry.
`TimeSent` is nonzero from reservation; zero `WrittenAt` denotes an in-flight
write and is excluded from expiry. Preserve both timestamps and `Lineage` when
serializing requests. A session retains its lineage through rebinds. Callbacks
do not consume store deadlines. A store may be shared by sessions in one process;
`Owner`, `Lineage` and `ID` use counters seeded with a random 64-bit process prefix
at startup, with a time/process-ID fallback if the entropy reader fails. Collisions
between processes or restarts are negligible. Silent foreign cleanup requires an
exact sequence and reservation ID retained in a known, finally closed local owner's
ledger; an unknown process's rows are preserved. Separate backend namespaces per
process remain recommended: capacity and sequence numbers are shared, and one
process cannot reclaim another process's rows after it exits. For absence recovery
across local Sessions, reuse the same store instance (a pointer implementation or
comparable value such as DefaultStore), rather than separate wrappers around one
backend. Decorators containing non-comparable interface values are safe to use,
but cannot establish backend identity for absence settlement across lineages.
Close also settles uncertain identities whose rows were never inserted;
if its reads fail, a later Session on that store can confirm absence silently.
Settling the final identity of a closed lineage also clears its abandoned markers
and recovery hint.
Retries rejected by a retained sequence still trigger rate-limited recovery, so
recovery does not require a successful new submission.

`RequestStore.WrittenLength(ctx, excludeOwner)` counts entries with nonzero
`WrittenAt`, excluding enquire_link and the supplied nonzero owner. Zero includes
all owners. Maintain this count atomically with `Set`, `MarkWritten`, `Delete`
and `Clear`. `Length` still counts every reservation. The writer adds its own
local written count so a failed metadata update cannot erase its capacity usage.

`Dialer` now requires `DialContext(context.Context, string) (net.Conn, error)`.
Use `DialerFunc` to adapt a custom function. Pass its context to TCP/TLS dialing
and handshaking, for example `net.Dialer.DialContext` or `tls.Dialer.DialContext`.

`Request` gains `Owner`, `ID`, `Lineage` and `WrittenAt`; `Response` gains `Err`.
`ErrSequenceInUse`, `ErrSubmitQueueFull` and `ErrRequestCleanupFull` identify
synchronous submission rejection.

Library-worker callbacks receive `ErrSubmitQueueFull` synchronously when the
application window is full, their writer queue cannot advance, or a blocking wait
would create a cycle. Rejected submissions do not call `OnSubmitError`: always
check the return from `Submit`/`SubmitContext`. In `OnClosePduRequest` and
`OnRebind`, hand retry work to an application goroutine or application-owned retry
queue, return from the callback, and let that goroutine use `SubmitContext` with
an application deadline. Automatic enquiry responses now use
`OnUnexpectedPduResponse` because automatic enquiries create no window entries.
`WithBindTimeout` and
`SubmitError` are added, and `TXConnector` accepts optional connector options.
