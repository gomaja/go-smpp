# go-smpp

[![Go Report Card](https://goreportcard.com/badge/github.com/gomaja/go-smpp)](https://goreportcard.com/report/github.com/gomaja/go-smpp)
[![pkg.go.dev](https://pkg.go.dev/badge/github.com/gomaja/go-smpp.svg)](https://pkg.go.dev/github.com/gomaja/go-smpp)

SMPP v3.4 client and SMSC server library in pure Go. It manages connections,
bind exchanges, rebinding, request correlation and session callbacks.

## Installation and versioning

```sh
go get github.com/gomaja/go-smpp@main
```

The module requires Go 1.26 or later and is tested on the Go releases that are
still supported upstream (currently 1.26 and 1.27); the minimum moves with them.

This module publishes no releases. `main` is the only supported line, and its
API changes whenever a better design is found, without a deprecation period. Go
records the commit you build against as a pseudo-version in your `go.mod`;
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

Examples are part of the module: the gate builds, vets and tests them with
`./...`, and runs their tests explicitly, with `-race` in full mode.

## Client

### Session and settings

Use `TXConnector`, `RXConnector` or `TRXConnector` with `NewSession`. Set
`Auth.SMSC` to a reachable SMSC endpoint. Full programs are in
[example/](example/).

```go
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"time"

	gosmpp "github.com/gomaja/go-smpp"
	"github.com/gomaja/go-smpp/pdu"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	auth := gosmpp.Auth{SMSC: "127.0.0.1:2775", SystemID: "esme", Password: "secret"}
	session, err := gosmpp.NewSession(
		gosmpp.TRXConnector(gosmpp.NonTLSDialer, auth),
		gosmpp.Settings{
			EnquireLink: time.Minute, ReadTimeout: 2 * time.Minute,
			WriteTimeout:     5 * time.Second,
			OnSubmitError:    func(_ pdu.PDU, err error) { log.Print(err) },
			OnReceivingError: func(err error) { log.Print(err) },
			OnRebindingError: func(err error) { log.Print(err) },
			OnPDU: func(p pdu.PDU, responded bool) {
				log.Printf("Received %T (responded: %t)", p, responded)
			},
			OnClosed: func(state gosmpp.State) { log.Print(state.String()) },
		}, 5*time.Second)
	if err != nil {
		log.Print(err)
		return
	}
	defer func() { _ = session.Close() }()
	<-ctx.Done()
}
```

| Setting | Contract |
| --- | --- |
| `ReadTimeout` | Must exceed both zero and `EnquireLink` for `NewSession`. |
| `WriteTimeout` | Bounds transport writes; see [write deadlines](#write-deadlines). |
| `EnquireLink` | Zero disables automatic keepalives. |

The `rebindingInterval` argument to `NewSession` disables automatic rebinding
when non-positive.

`Session.IsAlive()` reports liveness. `Session.SessionStatus()` returns the
typed `SessionActive` / `SessionClosed` status. `Session.GetSystemIDStatus` also
reports the bind type and connection addresses.

#### Write deadlines

A positive `WriteTimeout` bounds transport writes. A non-positive value leaves
ordinary writes without a deadline; protocol error and close-path responses use
a 30-second fallback. Explicit shutdown attempts unbind with a 50-millisecond
deadline regardless of `WriteTimeout`.

### Callbacks

`OnPDU` observes received PDUs and reports whether a reply was automatic.
`OnAllPDU` takes precedence over `OnPDU` and returns a manual reply and
`closeBind` decision. With window tracking, response callbacks are separate;
inbound requests use `OnReceivedPduRequest`, or these handlers and the default
response path when it is nil. `OnRebind` reports a successful rebind.

The default client path answers enquire_link and unbind. When `OnAllPDU` handles
a request, or `OnReceivedPduRequest` runs without `EnableAutoRespond`, the
callback must return those replies itself. Server housekeeping follows its own
[bind-state rules](#bind-state-matrix).

Every accepted outbound PDU that cannot be written reaches `OnSubmitError`,
including queued PDUs discarded during shutdown. Such discarded submissions
carry `ErrConnectionClosing`. A successful `Submit` means queued, not written;
rejected submissions return their error directly without invoking
`OnSubmitError` or a request-completion callback.

### Close and the callback contract

`Session.Close()` rejects further submissions, interrupts transport I/O, stops
rebinding, and waits for every session worker and callback, including
`OnClosed`. On explicit shutdown it first attempts an unbind (SMPP v3.4 §4.2),
with a 50-millisecond write deadline. It skips unbind when a write is active or
an earlier write left a partial frame or a permanent transport error, then
proceeds with shutdown.

Encoding errors still permit graceful unbind. `Close` does not wait for
unbind_resp. `Connection.WritePDU` rejects writes with `ErrConnectionClosing`
after shutdown begins, a write leaves a partial frame, or a permanent transport
error occurs, including a writer that was waiting for transport access.

This also prevents another worker from appending a PDU to an incomplete frame
while the first write's error callback runs. A zero-byte write timeout leaves
framing intact and still permits a response before shutdown. TLS transport
closure may additionally wait for Go's close_notify timeout.

Concurrent external calls wait for the same completion. Calling `Close()` from
inside a callback belonging to that session initiates shutdown and returns. The
same applies when joining would create a wait cycle between instances, such as
two sessions whose `OnClosed` callbacks close each other.

In either case, the calling callback and other callbacks already running may
finish afterward, and queued submission errors, pending-request completion and
store-error callbacks, and `OnClosed` may still run during cleanup. Other
callbacks due from in-flight processing or rebinding may also begin before
cleanup finishes. The calling callback must return.

A subsequent external call waits for cleanup. A call from another instance's
callback also waits unless it would complete a wait cycle. Cycle detection
considers every worker in the owning Session or Server.

Closing `Session.Transceiver()` or stopping one server account can therefore
return without joining when a different worker in that same Session or Server
closes the cycle, even if the selected connection could finish independently.
Goroutines started by the application are external callers; their own waits are
not tracked. The same contract applies to server `Stop()` and `StopSystemID()`
for the connections they stop.

`OnClosed(ExplicitClosing)` runs once even when automatic rebinding is enabled;
explicit shutdown never rebinds.

### Binding and rebind

Client bind exchanges use `DefaultBindTimeout` (30 seconds), covering both the
bind write and response read. Set `WithBindTimeout(duration)` on TX, RX or TRX
connectors to change it; non-positive values use the default. Successful
exchanges clear this deadline before session I/O.

Built-in connectors also expose `ConnectContext(context.Context)`, which session
rebinding uses to interrupt bind I/O on shutdown. A custom connector without
that method must return from `Connect()` before external `Close()` can finish.
`Dialer.DialContext` accepts a context; the built-in TCP dialer uses
`net.Dialer.DialContext` and shutdown joins that dial.

Custom dialers should honor cancellation. If one ignores it, the connector
abandons that attempt and closes a late connection; its dialing goroutine
remains until the function returns, although session shutdown completes without
waiting for it.

During binding, both the client and server answer enquire_link and continue
waiting under the original bind deadline (SMPP v5 §2.4 Table 2-1 permits it in
Open; v3.4 §2.3 Table 2-1 lists it only in bound states). The server ignores
generic_nack while awaiting the first bind. On the client, a generic_nack with
the bind sequence or sequence zero fails with `BindError` carrying the peer's
status (SMPP v3.4 §4.3.1).

### Request window

Enable `WindowedRequestTracking` for TX/TRX correlation. Set a positive
`StoreAccessTimeOut` and nonzero `MaxWindowSize` (at most 255). With positive
`PduExpireTimeOut`, set a positive `ExpireCheckTimer`; non-positive timers
disable expiry checks, and zero expiry disables expiration. `WithRequestStore`
replaces the default in-memory store.

#### Store contract

| Store method | Required behavior |
| --- | --- |
| `Set(ctx, request)` | Atomically insert only an absent sequence; preserve an existing row and return `ErrSequenceInUse`. A failed `Set` is followed by conditional cleanup in case insertion committed before acknowledgement failed. |
| `Get` / `List` | Preserve reservation metadata. `List` should be complete, but stale or truncated snapshots are supported for uncertain cleanup; recovery confirms identities with `Get`. |
| `Delete(ctx, request)` | Atomically compare sequence, `Owner` and `ID`, then remove. Timestamps do not define identity. `false, nil` means absent or replaced. The local ledger also arbitrates completion; removal alone does not authorize a callback. |
| `MarkWritten(ctx, request, sentAt)` | Set `WrittenAt` only on the matching pending reservation. Return `false, nil` for removed/replaced entries; never recreate one. |
| `WrittenLength(ctx, excludeOwner)` | Count written application requests, excluding enquire_link and the specified nonzero owner; zero includes all owners. Update atomically with `Set`, `MarkWritten`, `Delete` and `Clear`. |
| `Length` | Count all reservations. |
| `Clear` | Administrative operation, only after all owning sessions have closed. |

Store operations must be safe concurrently and respect context cancellation.
Cache eviction must not discard outstanding reservations: entries live until
explicit removal. Preserve `Owner`, `ID`, `Lineage`, `TimeSent` and `WrittenAt`
through storage and serialization. A session keeps its lineage through rebinds.
`Get` returns false only for absence or a canceled context; a canceled `List`
snapshot cannot establish absence. Live completion must use the library arbiter.
Before directly removing or clearing library-owned reservations, close every
session using that store; direct removal otherwise invalidates the completion
and admission ledger. Session cleanup conditionally deletes only reservations it
owns. The writer adds its local written count to `WrittenLength`, so a failed
metadata update cannot erase capacity usage.

A store may be shared by sessions in one process; `Owner`, `Lineage` and `ID`
use counters seeded with a random 64-bit process prefix at startup, with a
time/process-ID fallback if the entropy reader fails. Collisions between
processes or restarts are negligible. Silent foreign cleanup requires an exact
sequence and reservation ID retained in a known, finally closed local owner's
ledger; an unknown process's rows are preserved.

Separate backend namespaces per process remain recommended: capacity and
sequence numbers are shared, and one process cannot reclaim another process's
rows after it exits. For absence recovery across local Sessions, reuse the same
store instance (a pointer implementation or comparable value such as
DefaultStore), rather than separate wrappers around one backend. Comparability
is checked on the dynamic value, including nested interfaces. Non-comparable
decorators are safe to use and retain own-lineage recovery, but cannot establish
backend identity for absence settlement across lineages. Close also settles
uncertain identities whose rows were never inserted; if its reads fail, a later
Session on that store can confirm absence silently.

Settling the final identity of a closed lineage also clears its abandoned
markers and recovery hint. Retries rejected by a retained sequence still trigger
rate-limited recovery, so recovery does not require a successful new submission.

#### Correlation and completion

Window entries are reserved during `Submit`, before queue admission and writing,
and removed independently of response callbacks. Reusing an outstanding sequence
returns `ErrSequenceInUse` without replacing its request or sending another PDU.
`TimeSent` records reservation time and is available even to an immediate
response; `WrittenAt` records completed writing.

A reservation becomes eligible for response correlation immediately before its
transport write begins. A response to a request still waiting for admission goes
to `OnUnexpectedPduResponse` and preserves the reservation. Pending writes have
zero `WrittenAt` and cannot expire; expiry measures age from `WrittenAt`.

`generic_nack` completes its matching entry. Each reservation belongs to its
sending connection and session lineage; exactly one response, expiry or close
operation may complete it. Conditional removal and the local ledger arbitrate
completion. A successful removal must also claim the ledger before invoking a
callback. A response that finds its row absent can claim the local request
before expiry does; only the winning path delivers completion. Failed cleanup
cannot abandon a completion another path has already claimed. Store operations
have bounded deadlines; application callbacks consume none of that deadline.

A successful response-path Get returning a row remains usable even if its
deadline has just elapsed. A missing row without a store deadline instead
completes through the local ledger. A Get deadline or Delete error releases
local written capacity and admission immediately because the response has
arrived. It reaches `OnReceivingError`; an unclaimed reservation becomes
abandoned, retaining its exact identity only for silent cleanup, with no
response/expiry/close callback afterward.

A malformed response is reported through `OnReceivingError` and, when
correlated, through `OnExpectedPduResponse` with `Response.Err` set and a
non-nil response containing the received header. Check `Err` before treating it
as a successful response. A generic_nack rejecting one of our responses can
share a sequence with an unrelated outstanding request and complete that
request: the peer's sequence space overlaps ours, and generic_nack carries no
rejected command ID.

#### Capacity and enquire_link

`SubmitContext` reserves a request under its own store-access budget before
admission. The pre-write capacity check uses a separate budget; marking a
successful write gets a fresh budget after transport I/O. Only written,
unanswered application requests count against `MaxWindowSize`; queued
reservations do not. Tracked enquire_link bypasses that limit and remains
correlated.

The next application or automatic enquiry retires the previous unanswered
application enquiry without a response, expiry or close completion callback,
keeping at most one written application enquiry outstanding per connection. Its
late response is unexpected. Automatic keepalives always bypass the request
store; their responses use `OnUnexpectedPduResponse` when tracking is enabled.
An error retiring a previous enquiry reaches `OnReceivingError` and does not
prevent the keepalive from being sent.

#### Recovery

A live connection may expire reservations left by a dead generation of the same
session. Failed cleanup and a full window trigger asynchronous recovery;
ordinary writes do not scan and the writer never waits for a scan.

At most one scan runs at a time, with attempts limited to once per
`ExpireCheckTimer`, or once per second when that timer is disabled. A joined
helper invokes `OnClosePduRequest` away from the writer when conditional
deletion and the local ledger both grant completion, so the callback can
resubmit. Recovery runs whether or not expiry is enabled.

Each scan bounds backend work by one `StoreAccessTimeOut`, with at most 256 Get
calls for cleanup identities across eligible lineages. Healthy scans start with
own-lineage lookups; when both own and finally closed foreign lineages need
lookups, each receives up to half the count. After a scan exhausts its time
budget, the next rotates its starting phase: own lookups, List cleanup and
closed-lineage lookups each get a fresh budget within three such scans. Every
operation has a fresh deadline limited by the remaining scan budget; List
cleanup uses up to eight concurrent operations.

Identity, List-row, phase and background-lineage cursors survive rebinds,
letting later scans proceed past unavailable rows. Periodic checks find finally
closed lineages on the same comparable store even without a new submission or an
own-lineage hint. Their local registry supplies a shared cleanup hint until the
final identity is settled.

Recovery confirms each uncertain identity with Get and conditionally deletes a
matching row even when List omitted it. Outstanding-generation accounting
preserves the recovery hint when List fails quickly with no rows or reaches its
deadline. Expiry checks shutdown before each backend operation and cancels
operations in progress, leaving unfinished requests for close or recovery
instead of making Close wait one timeout per expired row.

Sessions sharing a store share window capacity, but each session reports only
its own requests, including across rebinds. Once another session has completed
shutdown, its unrecoverable store entries may be removed silently. Capacity
checks are advisory across concurrent writers and while a backend metadata
update is unavailable.

A fully written PDU whose `MarkWritten` update fails is reported through
`OnReceivingError`; the connection stays usable and recovery retries the marker
conditionally.

Failed admissions are resolved when a bounded Get confirms their identity is
absent or has been replaced, so a Set that never committed cannot retain a
permanent cleanup hint. The most recent 1024 rejected identities remain as small
tombstones after absence settlement, so a late-committing Set is removed
silently during close or recovery. This retention is bounded: older rejections
and rows committed after final cleanup may require administrative removal.

Each session lineage retains at most 1024 uncertain cleanup identities. Healthy
queued and admission-waiting requests, successful cancellation rollbacks and
identities retained after a close completion do not count toward that bound.
Abandonment suppresses completion while its local cleanup identity remains, even
after a replay tombstone is evicted. Further tracked submissions return
`ErrRequestCleanupFull` and trigger throttled recovery until space is reclaimed;
automatic keepalives remain independent of this bound.

#### Close and store flush

Close bounds backend cleanup by one `StoreAccessTimeOut`, with up to eight
concurrent deletions. Every operation gets a fresh deadline limited by the
remaining budget; final Close has no background-scan Get-count cap. The budget
excludes local bookkeeping, store-error reporting and callbacks.

Every locally owned transmitted request that has not completed or been abandoned
reaches `OnClosePduRequest`, even if List omits its row or the backend budget
expires. Store errors and callbacks run after the backend phase; an external
Close joins them. Unfinished cleanup retains its exact identity for later silent
recovery without occupying local window capacity or counting toward the
uncertain-identity limit. Completed or abandoned requests never gain completion
ownership again.

#### Queue admission and permits

Reservations also include queued submissions and callers waiting for queue
admission. Cancellation attempts conditional deletion and produces no completion
callback; only a failed rollback consumes uncertain cleanup capacity. Calling
`Submit` or `SubmitContext` from the connection's own writer callback returns
`ErrSubmitQueueFull` when its queue is full.

A blocking admission also returns that error if it would complete a callback
wait cycle with another instance; otherwise callers retain queue backpressure.
Queue waits register their dependency until admission, cancellation or shutdown
releases it. Queue admission commits under the shutdown lock.

A producer still waiting when shutdown begins returns `ErrConnectionClosing`,
even if the writer frees a queue slot during cleanup. Canceling a producer that
was woken for an available slot passes that notification to another waiter, so
cancellation cannot strand a live producer. Application submissions also wait
for a window admission permit before queueing; at most `MaxWindowSize`
application requests are admitted until completion.

Reserved requests still waiting for admission consume no permit. This keeps
producers from outrunning responses and makes a full window exert backpressure.
Enquire_link bypasses admission permits.

A callback on any library worker cannot wait for a full application window and
receives `ErrSubmitQueueFull`, including when it forwards into another Session.
Waiting for a silent peer from a callback could otherwise prevent shutdown from
completing. Application goroutines spawned by callbacks retain ordinary
admission backpressure.

`SubmitContext` cancellation also bounds waiting for a window permit.
`GetWindowSize` reports written, unanswered application requests, excluding
queued reservations and enquire_link.

In `OnClosePduRequest` and `OnRebind`, hand retry work to an application
goroutine or application-owned queue and return. That goroutine can call
`SubmitContext` with an application deadline. The returned
`Transceiver`/`Transmitter` interface exposes `Submit`; obtain context
submission with a type assertion to `interface { SubmitContext(context.Context,
pdu.PDU) error }`. Always check the return value.

### Sending national-language text

Choose tables explicitly with `data.NewGSM7`; no language detection or
best-table search runs. `LockingShift` replaces the main alphabet and
`SingleShift` replaces the extension table, independently (TS 23.038
§6.2.1.2 and Annex A). The integer identifiers are available as constants in
`github.com/gomaja/go-sms/encoding/gsm7/charset`.

This example selects Turkish locking and Spanish single shift, so `ıÁ` is
representable. Pass `Packed: true` only when the SMSC expects packed GSM7.

```go
package example

import (
	gosmpp "github.com/gomaja/go-smpp"
	"github.com/gomaja/go-smpp/data"
	"github.com/gomaja/go-smpp/pdu"
	"github.com/gomaja/go-sms/encoding/gsm7/charset"
)

func sendNationalText(session *gosmpp.Session, source, destination pdu.Address, text string) error {
	codec, err := data.NewGSM7(data.GSM7Options{
		Packed:       false,
		LockingShift: charset.Turkish,
		SingleShift:  charset.Spanish,
	})
	if err != nil {
		return err
	}
	message := pdu.NewSubmitSM().(*pdu.SubmitSM)
	message.SourceAddr, message.DestAddr = source, destination
	if err := message.Message.SetLongMessageWithEnc(text, codec); err != nil {
		return err
	}
	parts, err := message.Split()
	if err != nil {
		return err
	}
	for _, part := range parts {
		if err := session.Transceiver().Submit(part); err != nil {
			return err
		}
	}
	return nil
}
```

For a message that fits the construction limit, use
`Message.SetMessageWithEncoding(text, codec)` directly. Both text setters add
single-shift IE `0x24` and locking-shift IE `0x25` for the selected nonzero
identifiers, retaining caller-supplied IEs. Existing matching NLI IEs are kept;
a conflicting or malformed manual NLI IE makes the setter fail without
replacing the message. The submit_sm, deliver_sm and submit_multi marshalers
set wire UDHI when NLI IEs are present (TS 23.040 §§9.2.3.24.15–16).

Selector zero means the default table and omits that IE. NLI identifiers 1–13
provide thirteen single-shift tables and twelve locking-shift tables; Spanish
has no locking table. The constructor rejects Spanish locking and unknown or
reserved selectors. For manual header construction,
`pdu.NewIENationalLanguageSingleShift(int)` and
`pdu.NewIENationalLanguageLockingShift(int)` return `(pdu.InfoElement, error)`;
these reject zero because it is a reserved wire NLI (TS 23.038
§§6.2.1.2.4–5, Annex A).

Use PDU splitting for national-language messages: it budgets the complete UDH,
including caller IEs, NLI IEs and concatenation metadata. If a 16-bit
concatenation IE was supplied with `SetUDH`, splitting retains that reference
width while generating the new reference, count and part numbers (TS 23.040
§§9.2.3.24.1, 9.2.3.24.8). Direct packed `codec.(data.Splitter).EncodeSplit`
retains the legacy one-fill-bit convention for a six-octet concatenation
header; it does not infer the layout of an NLI header.

### Receiving national-language text

For received GSM7, `delivery.Message.GetMessage()` applies each segment's NLI
IEs through go-sms `tpdu.DecodeUserData` with `tpdu.WithAllCharsets`. There is no
need to pick a receive language or carry a previous part's table forward.
Each missing IE selects its corresponding default table for that segment.
Repeated IEs use the last applicable occurrence; a later reserved or
unsupported identifier is ignored and does not reset an earlier applicable
selection. Received Spanish locking selects the default main alphabet
(TS 23.038 §§6.2.1.2.4–5; TS 23.040 §§9.2.3.24.15–16).

SMPP has no packed/unpacked flag. If the peer sends packed GSM7, select that
representation explicitly; NLI selection still comes from the received UDH:

```go
package example

import (
	"github.com/gomaja/go-smpp/data"
	"github.com/gomaja/go-smpp/pdu"
)

func receiveText(delivery *pdu.DeliverSM, packed bool) (string, error) {
	if packed {
		return delivery.Message.GetMessageWithEncoding(data.GSM7BITPACKED)
	}
	return delivery.Message.GetMessage()
}
```

Non-GSM codecs, including UCS2, ignore NLI IEs when decoding. `Message.UDH()`
retains the original parsed IEs, including ignored selectors; `IgnoredUDH()`
exposes a bounded header ignored because its final IE was truncated. Decoding
does not rewrite those headers. SMPP `data_coding` keeps its own namespace
(v3.4 §5.2.19); an NLI does not change that wire byte.

### TLS

Supply a context-aware TLS dialer through `DialerFunc`, for example:

```go
package main

import (
	"context"
	"crypto/tls"
	"net"

	gosmpp "github.com/gomaja/go-smpp"
)

func main() {
	dialer := gosmpp.DialerFunc(func(ctx context.Context, addr string) (net.Conn, error) {
		return (&tls.Dialer{Config: &tls.Config{ServerName: "smsc.example"}}).DialContext(ctx, "tcp", addr)
	})
	_ = gosmpp.TRXConnector(dialer, gosmpp.Auth{SMSC: "smsc.example:2775"})
}
```

## Server

### Settings, accounts and handler

Register ESME accounts and start a TCP listener. Empty `BindingTypes` permits
TX, RX and TRX; zero `MaxSessions` is unlimited. A non-positive account
`ReadTimeout` disables its read deadline; a positive value must exceed
`EnquireLink`. Account write timeouts follow [write
deadlines](#write-deadlines). Non-positive bind timeouts use
`DefaultBindTimeout`. Construct the server with `NewServer`; a zero-value server
returns `ErrServerNotInitialized` from operations, exposes empty accessor values
and treats shutdown as a no-op.

```go
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"time"

	gosmpp "github.com/gomaja/go-smpp"
	"github.com/gomaja/go-smpp/pdu"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	server := gosmpp.NewServer(gosmpp.ServerSettings{
		Address: "127.0.0.1:2775", SystemID: "smsc",
		OnError: func(err error) { log.Print(err) },
	})
	defer server.Stop()
	if err := server.StartSystemID(gosmpp.SystemIDConfig{
		SystemID: "esme", Password: "secret", MaxSessions: 4,
		ReadTimeout: 2 * time.Minute, WriteTimeout: 5 * time.Second,
		EnquireLink: time.Minute,
		OnPDU: func(_ gosmpp.ServerSessionInfo, request pdu.PDU) (pdu.PDU, bool) {
			log.Printf("Received %T", request)
			if submit, ok := request.(*pdu.SubmitSM); ok {
				response := submit.GetResponse().(*pdu.SubmitSMResp)
				response.MessageID = "message-1"
				return response, false
			}
			if request.CanResponse() {
				return request.GetResponse(), false
			}
			return nil, false
		},
	}); err != nil {
		log.Print(err)
		return
	}
	if err := server.Start(); err != nil {
		log.Print(err)
		return
	}
	log.Printf("Listening on %s", server.Addr())
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			delivery := pdu.NewDeliverSM().(*pdu.DeliverSM)
			if err := delivery.Message.SetMessageWithEncoding("Hello from SMSC", nil); err != nil {
				log.Print(err)
				continue
			}
			// Success means queued; deliver_sm_resp carries the ESME's result.
			deadline, stop := context.WithTimeout(ctx, time.Second)
			err := server.SubmitToSystemID(deadline, "esme", delivery)
			stop()
			if err != nil {
				log.Print(err)
			}
		}
	}
}
```

`Addr()` reports the listener address, including an assigned ephemeral port. Set
`TLSConfig` to a `*tls.Config` with certificates, `GetCertificate`, or
`GetConfigForClient` to accept TLS connections; the bind timeout also covers TLS
handshaking. `SystemID` is the SMSC identifier returned to peers and may contain
at most 15 octets, excluding the terminator.

ESME credential lengths are left to deployments.

The server permits at most eight pre-bind control PDUs in total: enquire_link,
generic_nack and unknown commands. A ninth closes the connection, and these
exchanges never extend the bind deadline. Unknown commands receive generic_nack
with `ESME_RINVCMDID` before the server continues waiting. Other known requests
received before the bind, such as unbind, close the connection without a
response.

A malformed bind receives its matching header-only bind response with the decode
status, then closes; lost framing receives generic_nack with `ESME_RINVCMDLEN`,
then closes. For interoperability, the server tolerates a nonzero request
command_status; authentication and admission determine the bind response status.
A binding type outside the account allow-list or an exhausted `MaxSessions`
limit returns `ESME_RBINDFAIL`. The same Connection uses raw reads throughout
the handshake and allocates its 128 KiB read buffer only on the first buffered
Read after binding.

Idle unauthenticated connections therefore retain no read buffer.
Unknown-command diagnostics report the first occurrence immediately and one
aggregate when the handshake ends, with counts for enquiries, generic nacks and
unknown commands (including the rejected ninth control). Intentional shutdown
suppresses the final aggregate. `ServerBindError` exposes rejection status and
safe connection metadata through `OnError`, without the bind password.

The handler receives a `ServerSessionInfo` value identifying the account, bind
type, protocol version and addresses. Without a handler, permitted requests
receive `ESME_RSYSERR` and responses are discarded.

A handler returning `closeBind` closes after its response is written.

### SubmitToSystemID

`GetSystemIDStatus` includes the bind type and typed `SessionActive` or
`SessionClosed` status. `SystemIDs` lists accounts with bound sessions.
Submission success means queued, not delivered; track `deliver_sm_resp` to learn
the ESME's result.

Delivery requires a live RX/TRX session; otherwise `SubmitToSystemID` returns
`ErrNoLiveSession`. PDUs with optional parameters also require v3.4 or later.
When live RX/TRX sessions exist but none accepts TLVs, submission returns
`*gosmpp.UnsupportedOptionalParametersError` without sending. The context bounds
waiting for queue capacity, not a later socket write. Handler responses to older
peers have optional parameters stripped, with a diagnostic in `OnError`.

Write failures also reach `OnError` with account and remote address context. Use
`errors.As(err, &submitError)` with `*gosmpp.SubmitError` to identify the PDU
that failed; its `Err` unwraps to the underlying error. This also covers queued
PDUs discarded when a connection closes.

Submitting a delivery receipt inside `OnPDU` for the message being handled can
put that receipt on the wire before the handler's `submit_sm_resp`; applications
requiring response-first ordering must coordinate it themselves.

### Bind-state matrix

The server checks the ESME-issued operation before invoking the handler (SMPP
v3.4 §2.3 Table 2-1, §§2.5–2.7; v5 §2.4 Table 2-1).

| Inbound operation | TX | RX | TRX |
| --- | --- | --- | --- |
| submit_sm, submit_multi, query_sm, cancel_sm, replace_sm, data_sm | Yes | No | Yes |
| deliver_sm_resp, data_sm_resp | No | Yes | Yes |
| generic_nack, enquire_link_resp, unbind_resp | Yes | Yes | Yes |
| enquire_link, unbind, repeated binds | Library handles | Library handles | Library handles |
| Other ESME-issued PDUs | No | No | No |

Server handlers cannot override the operations marked "Library handles".
Disallowed requests receive a matching header-only `ESME_RINVBNDSTS` response;
disallowed responses are dropped.

### Shutdown

`StopSystemID` removes an account and closes its connections, while `Stop()`
closes all connections. External shutdown calls wait for handlers and cleanup;
calls inside callbacks initiate shutdown as described above. A stopped server
cannot restart.

`Stop` also closes the listener and handshaking connections. See the [callback
contract](#close-and-the-callback-contract) for reentrant calls and wait cycles.

## PDUs and codings

### Receive and send policy

Marshal and transports check wire representability, allowing over-limit received
fields to be forwarded. Call `pdu.Validate(p)` for all SMPP v3.4 mandatory-field
maxima; its joined `*pdu.ValidationError` values identify fields and suggested
response statuses. Setters enforce their construction limits.

See [Policies](docs/spec-compliance.md#policies) for the full receive/send
table.

Address does not know its containing PDU; call `pdu.Validate` before sending to
enforce the per-PDU 21/65-octet limits (including NUL). Address setters accept
up to 64 content octets for every PDU, and Marshal preserves them. An address
accepted by the setter can exceed submit_sm's maximum and be rejected by a
strict SMSC.

This allows a transit SMSC to forward accepted traffic.

`pdu.Validate` does not enforce time syntax or the exact 1-or-17-octet time size
rule. Receive-side C-Octet fields are bounded by their frame; embedded NUL
cannot be encoded because it would terminate a field early. Non-ASCII address
octets are preserved for interoperability.

### DecodeError and EncodeError

An intact malformed frame returns `*pdu.DecodeError` with its received header,
suggested status and cause. The session reports it and continues at the next
PDU; framing failures instead close the connection after a generic_nack.
Incomplete reads wrap `pdu.ErrIncompletePDU` and the reader error; invalid
command lengths wrap `pdu.ErrInvalidCommandLength`. Correlated malformed
responses are described in the request-window contract above.

See the [encoding error contract](docs/spec-compliance.md#encodeerror) for
`ByteBuffer.Err()`, `pdu.ErrEncode` and transport `*pdu.EncodeError` handling.

An unencodable client handler response reaches `OnSubmitError` and is replaced
with a matching, header-only `ESME_RSYSERR` response using the request sequence;
the handler's `closeBind` decision still applies. Malformed alert_notification
body fields are reported without a reply, since that command has no response
(v3.4 §2.8).

### Optional parameters

`PDU.OptionalParameters` is an ordered `[]pdu.Field` that retains repeated TLVs.
Use `p.GetOptionalParam(tag)` for the first occurrence, or
`p.GetOptionalParams(tag)` for all occurrences. Indexing by a tag still compiles
but uses a slice index and may panic.

In `for index, field := range p.OptionalParameters`, the first value is an
index; read the tag from `field.Tag`. `RegisterOptionalParam` replaces the first
occurrence; `AppendOptionalParam` adds another in wire order.

`ClearOptionalParameters` detaches the slice; later appends do not overwrite an
earlier slice returned by `GetOptionalParameters`.

The server advertises `pdu.TagScInterfaceVersion` on successful v3.4-or-later
binds; older peers do not receive that TLV.

### User data headers

Complete UDH parsing and marshalling use go-sms's `tpdu.UserDataHeader`
(TS 23.040 §9.2.3.24). The `pdu.UDH` and `pdu.InfoElement` API, constructors,
first-occurrence pointer lookup and concatenation lookup remain available.
`UDHL()` includes the length octet, returns zero without IEs and returns -1
when the body exceeds 255 octets. Empty headers are omitted on send.

The public `UDH.UnmarshalBinary` facade requires at least one well-formed IE;
an empty or ignored header returns an error without changing the receiver.
Parsed IE data is copied by go-sms and does not alias the input. Zero-length
IE data may be nil. Standalone `InfoElement.UnmarshalBinary` delegates to
`tpdu.InformationElement.UnmarshalBinary` under the same clause. Its data is
also copied and is nil for a zero-length IE. Errors return zero consumed
bytes, leave the receiver unchanged and use go-sms's typed decode errors.

`UDH.GetConcatInfo` returns the last concatenation IE that has the right
length and values in range, and ignores the others (TS 23.040 §§9.2.3.24,
9.2.3.24.1, 9.2.3.24.8). To read the values of an IE ignored for its values,
decode it with go-sms, for example
`tpdu.InformationElement{ID: ie.ID, Data: ie.Data}.ConcatInfo()`, which does
not check their ranges and returns an error for the wrong length;
`tpdu.ConcatInfo.Valid` reports whether the values are in range.

SMPP framing stays in go-smpp. With UDHI set and a nonempty message field,
`ShortMessage` parses only the declared header span. It preserves `00` and
headers with a malformed final IE byte for byte in `IgnoredUDH()`, skips
their declared span and exposes no parsed `UDH()`. A span beyond the message
field remains an error. The result is committed only after framing succeeds,
and preserved header bytes take precedence when forwarding the message.

SMS capacity comes from `tpdu.UserDataCapacity` using the actual complete
marshalled header, including UDHL, caller IEs, NLI and the selected
concatenation width (TS 23.040 §§9.2.3.16, 9.2.3.24). Ordinary encoded chunks
come from `tpdu.SplitUserData`: unpacked GSM septets, UCS2/UTF-16 and octet
alphabets retain whole characters (TS 23.040 §§9.2.3.24.1, 9.2.3.24.8;
TS 23.038 §§6.2.1.1, 6.2.3). The selected codec still converts text first.

### Long messages and splitting

Use `SetLongMessageWithEnc` followed by `SubmitSM.Split`, or
`NewLongMessageWithEncoding` for message parts. Submit each resulting PDU.
Splitting assigns independent sequence numbers, preserves unrelated TLVs,
replaces UDH concatenation metadata while retaining its 8-bit or 16-bit
reference width, and drops stale SAR TLVs (TS 23.040 §§9.2.3.24.1, 9.2.3.24.8). `ShortMessage.IgnoredUDH` exposes a preserved header whose truncated
final IE prevents parsing; its declared bytes are skipped before the payload.
`data.CustomEncoding.BaseEncoding` exposes the underlying codec; `ShouldSplit`
and `EncodeSplit` delegate to its splitter when available. GSM text uses
escape-safe septet budgets reduced by the complete header; 160/153 applies to
no UDH or an 8-bit concatenation IE alone. NLI and other caller IEs consume
additional space. `data.UCS2` counts encoded UTF-16 octets, including surrogate
pairs, and `data.UCS2Strict` counts two octets per BMP character. Construction
also checks SMPP's octet-length field (v3.4 §§5.2.21–22).

For raw data, optionally stage a UDH with `SetUDH` first, then call
`SetLongMessageDataWithEncoding` and `SubmitSM.Split` or `ShortMessage.Split`.
The long setter retains the supplied bytes without copying and accepts any
encoding; split before sending (SMPP v3.4 §§5.2.21–22). When a message fits,
`SubmitSM.Split` returns the source PDU and the marshalers set UDHI only for
[NLI IEs](#sending-national-language-text), so set UDHI in `esm_class` when
staging any other header; multipart parts set it (SMPP v3.4 §5.2.12). For
example, 8-bit data with a 16-bit application port IE (TS 23.040 §9.2.3.24.4):

```go
package main

import (
	"bytes"
	"fmt"
	"log"

	"github.com/gomaja/go-smpp/data"
	"github.com/gomaja/go-smpp/pdu"
)

func main() {
	submit := pdu.NewSubmitSM().(*pdu.SubmitSM)
	var header pdu.UDH
	// UDHL, 16-bit port IE, destination 5514, source 5515.
	if _, err := header.UnmarshalBinary([]byte{6, 5, 4, 0x15, 0x8a, 0x15, 0x8b}); err != nil {
		log.Fatal(err)
	}
	if err := submit.Message.SetUDH(header); err != nil {
		log.Fatal(err)
	}
	if err := submit.Message.SetLongMessageDataWithEncoding(bytes.Repeat([]byte{0x80}, 269), data.BINARY8BIT2); err != nil {
		log.Fatal(err)
	}
	parts, err := submit.Split()
	if err != nil {
		log.Fatal(err)
	}
	for _, part := range parts {
		payload, err := part.Message.GetMessageData()
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(len(payload)) // 128, 128, 13
	}
}
```

When the data comes from a `message_payload` TLV, the caller removes that
TLV: `short_message` and `message_payload` must not be used together (SMPP
v3.4 §4.4.1 Table 4-10), and `Split` keeps unrelated TLVs, including
`message_payload`. With UDHI set, the payload begins with the UDH (SMPP v3.4
§5.2.12); parse it with `UDH.UnmarshalBinary`, stage it with `SetUDH`, and pass
only the remaining data to `SetLongMessageDataWithEncoding`.

go-smpp retains concatenation width, position, count and reference allocation,
custom splitter dispatch (including nested wrappers), and SMPP PDU assembly.
Unknown text encodings still need a character-aware splitter. Packed GSM's
boundary search remains local because SMPP has no septet count: the CR rule
from TS 23.038 §6.1.2.3.1 must preserve inferred text as well as fit the header.
The legacy packed `EncodeSplit` keeps its one-fill-bit contract; raw ambiguity
and byte-identical re-encoding guards also remain. Custom codecs without
segmentation keep their local 254-octet SMPP field budget.

Raw data that already fits retains its existing acceptance, including odd
UCS2 within the unrounded octet budget. Multipart raw NLI GSM now rejects
septets above `0x7f` rather than forwarding them (TS 23.038 §6.2.1).
Dangling ESC, odd UCS2 and final high-surrogate errors remain unchanged when
splitting is required. Newly built multipart segments are independent of the
caller's supplied buffer. A message that fits is returned as the source and
keeps the raw setter's buffer alias; callers needing ownership clone the input.

### data_coding

`data.SMSCDefaultCoding` is `0x00`; `GSM7BITCoding` is its compatibility alias.
The local default remains GSM7; a peer with another SMSC-default alphabet needs
a custom codec. [Explicit national-language tables](#sending-national-language-text)
use `data.NewGSM7(data.GSM7Options{...})` with the same wire coding.
Default-alphabet and extension-table conversion uses
`github.com/gomaja/go-sms/encoding/gsm7` (TS 23.038 §§6.2.1–6.2.1.1).
`data.GSM7BIT.Decode` follows its permissive receiver policy: an unassigned
extension falls back to the main table, a repeated or trailing ESC displays a
space, and a byte above `0x7f` becomes a space. Encoding rejects malformed UTF-8
and unrepresentable characters with the dependency's typed errors.

`FromDataCoding` keeps the SMPP v3.4 §5.2.19 meanings for `0x00`–`0x0f`, including
ASCII, Latin-1, binary and opaque alphabets. For all other bytes it uses
`tpdu.DCS` from go-sms as a lenient TP-DCS extension under TS 23.038 §4, preserving
the original `DataCoding()` byte. This is an interoperability policy: the SMPP
table reserves some of these values. Reserved TP-DCS codings fall back to GSM;
defined compressed alphabets remain raw binary until TS 23.042 support exists.
Reserved bits do not change a defined TP-DCS alphabet: `0xe4`–`0xe7` and
`0xec`–`0xef` select UCS2, while `0xfc`–`0xff` select binary.

SMPP `0x05`, `0x09`, `0x0a`, `0x0d` and `0x0e` are opaque text alphabets with
140-octet budgets: they remain unsplit when they fit, or fail without a
character-aware codec. Custom codecs without a splitter accept at most 254
octets including UDH; received 255-octet fields can still be forwarded.

`data.UCS2` retains UTF-16BE interoperability, including supplementary characters
as surrogate pairs; `data.UCS2Strict` encodes only BMP Unicode scalar values.
Both send `data_coding=0x08` and reject malformed string UTF-8. They use go-sms's
`ucs2.EncodeUTF16` and `ucs2.Encode` respectively, and share `ucs2.Decode` on
receive (TS 23.038 §6.2.3; RFC 2781 §§2.1–2.2). Odd byte lengths return the
text decoded from complete 16-bit units together with `ucs2.ErrInvalidLength`.
The final octet is neither decoded nor carried into the next segment
(TS 23.040 §§9.2.3.24.1, 9.2.3.24.8). A high surrogate before that octet
becomes U+FFFD, with only `ucs2.ErrInvalidLength`. An even-length payload
ending in a high surrogate returns the decoded prefix and
`ucs2.ErrDanglingSurrogate`. Other unpaired surrogates become U+FFFD. Neither
decoder strips a BOM or changes the fixed big-endian byte order.

ASCII encoding rejects unrepresentable characters. Latin-1 preserves C1
controls. Cyrillic and Hebrew codecs replace C1 controls on decode and reject
them on encode; see [Known
limitations](docs/spec-compliance.md#known-limitations).

### Packed GSM

Legacy packed PDU setters and `data.DecodeGSM7Packed` share the replaceable
`data.GSM7BIT` alphabet, including through `CustomEncoding` wrappers. Only
its alphabet is borrowed: a packed replacement is resolved with packing off,
retaining any `NewGSM7` locking and single-shift tables. Packing and unpacking
happen once, after or before alphabet conversion. A custom unpacked alphabet
continues to supply its own conversion.

`data.FromDataCoding(0)` uses the current `data.GSM7BIT`, so assigning
`data.GSM7BIT = data.GSM7BITPACKED` makes received data_coding 0 packed.
A configured packed replacement has the same effect and supplies NLI IEs
when constructing text messages. Set the variable before concurrent use;
reads and writes are not synchronized. Received NLI IEs select the tables for
each segment. In PDU decoding, a configured national-language codec uses the
default tables when no NLI IEs are received.

`data.DefaultGSM7()` returns the effective default. If `data.GSM7BIT` is nil,
it returns the built-in unpacked codec; coding lookup, encoding selection,
default text construction and packed alphabet resolution use the same policy.
Raw setters keep a nil encoding as nil and marshal data_coding 0, even if the
default codec advertises another coding. `GetMessageWithEncoding(nil)` decodes
using the effective default without changing the stored encoding.
`Split()` resolves nil on every returned part, including an unchanged single
part, and retains the effective codec's data_coding. For example, an ASCII
default gives split results coding 1. A single part updates the source in
place; a multipart result leaves the source encoding unchanged.

Standalone `data.GSM7BITPACKED.Encode` / `Decode` use the fixed default GSM
tables. Sending with `data.NewGSM7` keeps its explicitly selected tables.

Use packed GSM only when the peer expects it. Built-in GSM alphabet conversion
and all bit packing use go-sms, including UDH fill alignment and national-language
tables.
`gsm7.Pack7BitCR` supplies CR padding; `gsm7.Unpack7BitCR` supplies inferred
receive decoding. SMPP carries an octet length rather than a septet count, so
this borrows the USSD CR convention (TS 23.038 §6.1.2.3.1); SMS zero padding is
specified separately in §6.1.2.1.1.

Inferred decoding keeps a genuine final `@`, removes a final CR only when that
septet ends exactly at the end of the octet payload, and retains doubled CRs. Zero-filled spare
septets therefore appear as `@` in the default alphabet. This changes the earlier
padding inference.
Use `data.DecodeGSM7Packed(payload, fillBits, septetCount)` when the exact count
is known; `-1` selects CR inference. For raw septets, use go-sms's
`gsm7.Unpack7Bit` and slice to the known count, or `gsm7.Unpack7BitCR` for CR
inference. Getters can retain exact original text when its encoding still
matches the stored bytes and all configured NLI selectors are present in the
current UDH.

Received NLI messages retain their original septets and IEs when split;
ambiguous raw CR tails still require retained original text. Other raw packed
text must also round-trip byte-identically before splitting. go-sms supplies
SMS budgets and ordinary escape-safe chunks; go-smpp retains the packed
CR-sensitive boundary search. See
[packed GSM conventions](docs/spec-compliance.md#packed-gsm-padding).

## Breaking changes on main

The migration baseline is the retracted `v1.0.1` tag. Migrate callers and custom
implementations as follows. The contracts above describe the resulting behavior.

### PDUs and encoding

| Change | Replacement |
| --- | --- |
| Optional parameters are an ordered slice; tag indexing can compile and panic. | Use `GetOptionalParam` / `GetOptionalParams`; read `field.Tag` when ranging. Use `RegisterOptionalParam` to replace or `AppendOptionalParam` to append. |
| Clearing optional parameters detaches the slice. | Use `ClearOptionalParameters`; earlier slices are independent of later appends. |
| Marshal enforces representation, rather than every v3.4 maximum. | Call `pdu.Validate` for strict field maxima, including per-PDU address lengths. |
| Encode failures are reported explicitly. | Check `ByteBuffer.Err()` and `errors.Is(err, pdu.ErrEncode)`; handle `*pdu.EncodeError`. |
| `ShortMessage.SetUDH`, `DestinationAddresses.Add`, `ShortMessage.Marshal` and `DestinationAddresses.Marshal` return errors. | Check each result; construction and marshaling can fail. |
| `pdu.PDU` requires six additional methods. | Custom implementations must add `AppendOptionalParam`, `ClearOptionalParameters`, `GetOptionalParam`, `GetOptionalParameters`, `GetOptionalParams` and `SetCommandStatus`. |
| `data.SMPP_V33` changes from `-0x33` to `0x33`; `OPT_PAR_MSG_WAIT` from `0x0002` to `0x0030`; `OPT_PAR_QOS_TIME_TO_LIVE_MIN` from `1` to `4`. | Use the named constants and update literal comparisons or lengths to the corrected values. |
| Outside `0x00`–`0x0f`, `FromDataCoding` delegates TP-DCS classification, including reserved bits, to go-sms. | Keep SMPP direct values separate from TP-DCS. `0xe4`–`0xe7` / `0xec`–`0xef` now select UCS2; `0xfc`–`0xff` now select binary. Reserved codings fall back to GSM and defined compressed alphabets remain binary. |
| `UDH.GetConcatInfo` ignores invalid concatenation IEs and returns the last applicable valid IE. | A wrong-length IE, zero total or sequence, or sequence greater than total no longer hides an earlier valid IE. Receive examples use the same selection for reference width (TS 23.040 §§9.2.3.24, 9.2.3.24.1, 9.2.3.24.8). |
| `UDH.UnmarshalBinary` and standalone `InfoElement.UnmarshalBinary` take copied IE data from go-sms; zero-length standalone IE data is nil. | Mutating the parse input no longer changes decoded IE data. Treat nil and empty IE data alike. Standalone underflows now use go-sms's `DecodeError` wrapping `tpdu.ErrUnderflow`; avoid matching the old error text. Errors consume zero bytes and leave the receiver unchanged. Empty or ignored headers still fail the strict UDH facade; malformed-final-IE errors use a generic message (TS 23.040 §9.2.3.24). |
| Multipart raw NLI GSM rejects septets above `0x7f` during chunking. | Validate raw septets or supply text encoded with the selected GSM7 tables (TS 23.038 §§6.2.1–6.2.1.1; TS 23.040 §§9.2.3.24.1, 9.2.3.24.8). Payloads that already fit retain their prior acceptance. |
| `data.FindEncoding` selects GSM7 by encodability in the default alphabet and default extension table, rather than ASCII membership. | `£`, `é`, `Δ` and `€` now select GSM7; backtick, ESC and controls outside the alphabet select UCS2. Extension characters consume two septets. If `GSM7BIT` has been replaced, that codec must also encode the text; for example, replacing it with `ASCII` makes `£` select UCS2. No national language tables are selected because the helper cannot return their IEs. With the initial codec, empty text selects GSM7; invalid UTF-8 selects UCS2, whose encoder still rejects it. 80 `£` characters now fit one segment instead of two (TS 23.038 §§6.2.1–6.2.1.1). |
| GSM7 text setters reject caller-supplied NLI IEs that conflict with the selected tables. | Select tables with `NewGSM7` and matching NLI IEs; rebuild the message when changing a previously selected table. |
| GSM7 `ShortMessage.GetMessage` now honors received NLI IEs. | Decode each part using its own UDH; select packed representation explicitly for peers that use it. |
| Replacing `GSM7BIT` with a packed codec no longer packs or unpacks twice in PDU construction, decoding or splitting. | The packed PDU path and `DecodeGSM7Packed` borrow only the replacement's alphabet, retaining configured tables with packing off. Custom unpacked alphabets still work. Standalone packed codecs retain their fixed default tables. |
| A nil `GSM7BIT` consistently selects the built-in unpacked default codec. | `FromDataCoding(0)`, `FindEncoding`, default text constructors and packed paths use `data.DefaultGSM7()`; nil no longer causes a codec lookup or construction panic. Raw setters retain nil and wire coding 0. `Split()` resolves nil on returned messages, preserving the effective codec's data_coding as before. |
| `GetMessageWithEncoding(nil)` uses the effective default. | Explicit nil decoding no longer panics and leaves the stored encoding unchanged. |
| `FromDataCoding(0)` now uses the current `GSM7BIT` instead of the initial codec. | A packed replacement makes received data_coding 0 packed; a custom replacement supplies its alphabet. The received coding remains 0 even if the replacement advertises another value. Configure before concurrent use. |
| Legacy packed PDU construction inherits the configured replacement's NLI tables; explicitly configured packed codecs keep their own alphabet during decoding and splitting. | Selected NLI IEs accompany every outgoing part. Received NLI IEs take precedence; a configured national-language codec without received NLI IEs uses the default tables (TS 23.038 §§6.2.1.2.4–5; TS 23.040 §§9.2.3.24.15–16). A custom unpacked alphabet need not implement `Splitter` when used by the legacy packed PDU path. Nil text encoding arguments resolve the effective default before table selection. Raw setters retain nil and marshal coding 0 until `Split()` resolves the returned messages' encodings. |
| Multipart packed segmentation may end a segment on a zero septet, changing part counts and segment bytes. | Recalculate multipart estimates and wire expectations: 300 `@` with caller UDH `02 70 00` now splits into 150/150 septets rather than 149/149/2. Single-message encodings are unchanged; boundaries follow the CR receive rule (TS 23.038 §6.1.2.3.1). |
| Splitting retains a caller-selected 16-bit concatenation reference width. | Account for the complete header size; generated references, counts and part numbers still replace the old values. |
| `data.GSM7(packed bool)` and its public x/text streaming adapter are removed. | Use `data.GSM7BIT` / `data.GSM7BITPACKED` for complete SMPP messages, or `gsm7.Encode` / `gsm7.Decode` for unpacked septets. Buffer complete messages instead of relying on the removed streaming transformer. x/text remains for the non-GSM codecs. |
| `ValidateGSM7String`, `ValidateGSM7Buffer`, `GetEscapeChars` and `IsEscapeChar` are removed. | Encode with `gsm7.Encode` and inspect its typed error; derive septet costs from the encoded bytes. For stricter receive diagnostics use `gsm7.Strict`, which also rejects unassigned-extension fallback and is not equivalent to the old buffer validator. |
| Packed inferred decoding follows go-sms's CR rule: final `@` and doubled CRs are retained; zero-filled spare septets are no longer stripped. A whole final CR septet is removed even after ESC. | Pass an explicit septet count to `data.DecodeGSM7Packed` to preserve exact content; `-1` uses CR inference (TS 23.038 §6.1.2.3.1). |
| `GetSeptetCount` on the packed codec's concrete type is removed. | Use the length of `gsm7.Encode` output to count septets before packing; extension characters consume two. |
| `data.ErrInvalidCharacter` and `data.ErrInvalidByte` are removed. | GSM encoding returns `gsm7.ErrUnencodable` or `gsm7.ErrInvalidUTF8`; inspect them with `errors.As`. Unpacked decoding now follows the permissive policy above. Invalid packed fill/count arguments return ordinary descriptive errors. |
| `data.UCS2` uses go-sms's UTF-16 codec; `data.UCS2Strict` adds strict UCS2 encoding with the same wire value `0x08`. | Keep `UCS2` for supplementary-character interoperability or explicitly select `UCS2Strict`; handle `ucs2.ErrUnencodable` for supplementary characters in strict mode. |
| Malformed string UTF-8 is rejected; UCS2/UTF-16 odd lengths return decoded complete units with `ucs2.ErrInvalidLength` (TS 23.038 §6.2.3). | Check errors from setters and getters with `errors.Is` / `errors.As` and retain returned text. Never carry an odd final octet into another segment (TS 23.040 §§9.2.3.24.1, 9.2.3.24.8). A high surrogate before that octet becomes U+FFFD with only `ucs2.ErrInvalidLength`; only an even-length final high surrogate returns `ucs2.ErrDanglingSurrogate` with bytes that a later segment may complete. |

### Request stores and callbacks

| Change | Replacement |
| --- | --- |
| `RequestStore.Delete` and `DefaultStore.Delete` take a `Request` and return `(bool, error)`. | Compare and remove sequence/Owner/ID atomically; let the library ledger arbitrate completion under the [correlation contract](#correlation-and-completion). |
| `Set` reserves only absent sequences. | Return `ErrSequenceInUse` without overwriting. |
| `RequestStore` requires `MarkWritten` and `WrittenLength`; `DefaultStore` implements them. | Implement conditional metadata updates and atomic counts under the [store contract](#store-contract). |
| `Submit` waits for an application-window permit; v1.0.1 failed the PDU through `OnSubmitError` with `ErrWindowsFull`. | Expect backpressure; use `SubmitContext` to bound waiting. A library callback receives `ErrSubmitQueueFull` when it would wait for a full window. |
| `GetWindowSize` counts written, unanswered application requests, excluding enquire_link; v1.0.1 used store `Length`. | Use it for written window usage; use store `Length` when every reservation is needed. |
| The example `CustomStore` is no longer comparable, and its constructor returns an error. | Share a pointer to one store instance and check `NewCustomStore` errors. |
| `Request` gains `Owner`, `ID`, `Lineage`, `WrittenAt`; `Response` gains `Err`. | Preserve metadata through serialization and check `Response.Err` before interpreting a response. |
| Rejections can return `ErrSequenceInUse`, `ErrSubmitQueueFull`, `ErrRequestCleanupFull` synchronously. | Check `Submit` / `SubmitContext`. Move callback retries to application goroutines with deadlines. |
| Automatic enquiry responses are untracked; retiring a tracked enquiry emits no completion callback. | Observe automatic and late enquiry responses through `OnUnexpectedPduResponse`. |
| Completion also depends on the local ledger; a missing response row can complete, and Close completes still-owned transmitted requests despite backend cleanup failure. | Follow the [completion](#correlation-and-completion) and [close/flush](#close-and-store-flush) contracts; callbacks run outside bounded store cleanup. |
| `ErrRequestCleanupFull` counts uncertain cleanup identities, rather than healthy pending admissions or completed-close cleanup. | Handle the rejection and allow bounded recovery to progress; preserve conditional store operations and the [cleanup limits](#recovery). |
| Stale or truncated List snapshots and non-comparable decorators are supported for recovery. | Keep List complete where possible; use Get to confirm uncertain identities and a stable comparable store instance for cross-lineage absence settlement. |

### Connections

| Change | Replacement |
| --- | --- |
| `Dialer` requires `DialContext(context.Context, string) (net.Conn, error)`. | Adapt with `DialerFunc` and pass the context to TCP/TLS dialing and handshaking. |
| `NonTLSDialer` is a `Dialer` value, rather than a callable function. | Pass it to a connector, or call its `DialContext` method. |
| `WithBindTimeout` and connector options are available, including on `TXConnector`. | Pass options when constructing connectors. |
| Server write errors carry `SubmitError`. | Use `errors.As` to find the failed PDU and unwrap its cause. |
| Shutdown joins workers except for reentrant/cyclic calls. | Return from the calling callback and use a subsequent external shutdown to join cleanup. |

### Toolchain

The minimum Go version is 1.26. Upgrade the toolchain before building main.

## Specification compliance

See [docs/spec-compliance.md](docs/spec-compliance.md) for authorities, exact
clauses, interoperability policies and known limitations.

## Supported PDUs

Command names follow SMPP v3.4 §5.1.2, Table 5-1.

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
- [x] submit_multi
- [x] submit_multi_resp
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

## License and NOTICE

Licensed under [Apache License 2.0](LICENSE). See [NOTICE](NOTICE) and
[LICENSES/](LICENSES/) for retained license notices.
