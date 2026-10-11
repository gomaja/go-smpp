# Specification compliance

This module currently implements SMPP v3.4 PDU and session behavior. The
standards baseline below was checked on 2026-08-02 from the publishing
authorities. The field codec and text-coding sections were rechecked on
2026-10-10 against SMPP v3.4 Issue 1.2, v5, and ETSI TS 23.038/23.040 V19.0.0.
Session and request-window behavior was also rechecked on 2026-10-11 against
the published SMPP v3.4 Issue 1.2 and v5 texts. The RFC Editor index and IETF
Datatracker were cross-checked: neither supplies an RFC governing SMPP binary
session behavior. The SMPP mapping drafts listed by Datatracker are expired or
replaced and concern CPIM or URI/ENUM mapping, not this session lifecycle.
Issue 1.2 incorporates erratum SMPPV3.4-05Oct99-01;
its bind_transceiver interface_version requirement remains applicable.

Source links:

- SMPP v3.4 Issue 1.2: https://smpp.org/SMPP_v3_4_Issue1_2.pdf
- SMPP v5: https://smpp.org/SMPP_v5.pdf
- 3GPP TS 23.038 archive file checked: https://www.3gpp.org/ftp/Specs/archive/23_series/23.038/23038-k00.zip
- 3GPP TS 23.040 archive file checked: https://www.3gpp.org/ftp/Specs/archive/23_series/23.040/23040-j00.zip
- 3GPP TS 23.041 archive file checked: https://www.3gpp.org/ftp/Specs/archive/23_series/23.041/23041-k00.zip
- ITU-T E.164: https://www.itu.int/rec/T-REC-E.164
- ITU-T E.212: https://www.itu.int/rec/T-REC-E.212

## Current baseline

- SMPP v3.4 Issue 1.2, 1999-10-12 is the active protocol baseline for the
  current API. Optional-parameter TLV framing is in section 3.2.4.1, optional
  tag definitions are in section 5.3.2, `interface_version` is section 5.2.4,
  `data_coding` is section 5.2.19, and `short_message` is section 5.2.22.
- SMPP v5 is the latest SMS Forum SMPP specification. It belongs in this same
  repository and Go module, but behind explicit version boundaries so existing
  v3.4 behavior does not change silently.
- GSM 7-bit default alphabet behavior follows 3GPP TS 23.038 section 6.2.1 and
  the extension table in section 6.2.1.1. National-language single and locking
  shift tables are in sections 6.2.1.2 and Annex A.
- UDH and concatenated SMS behavior follows 3GPP TS 23.040 section 9.2.3.24,
  the 8-bit concatenation IE in section 9.2.3.24.1, and the 16-bit
  concatenation IE in section 9.2.3.24.8.
- 3GPP TS 23.041 is relevant to SMPP v5 broadcast-message support. The current
  v3.4 API does not expose broadcast PDUs.
- ITU-T E.164 and E.212 govern numbering and network identifiers around SMPP
  deployments. This library exposes SMPP TON/NPI/address fields and should not
  reject deployment-specific values unless an explicit validator is requested.
- No current IETF RFC defines SMPP binary PDU or bind/session behavior. RFC
  work around messaging URIs, ENUM, SIP, email headers, or PASSporT does not
  amend this library unless those features are implemented directly.

## SMPP v5 direction

Future SMPP v5 support should remain in this repository and module because the
wire framing, bind lifecycle, sequence handling, optional-parameter model, and
operator use cases overlap heavily with SMPP v3.4.

The boundary must be version-explicit:

- Keep the existing public API compatible with SMPP v3.4.
- Add v5-only PDUs, fields, constants, and validation behind a separate package
  boundary.
- Share only binary-identical internals such as header framing, TLV encoding,
  connection state, and request-window primitives.
- Negotiate and validate `interface_version` explicitly rather than promoting
  v3.4 sessions into v5 behavior.
- Keep broadcast-message support scoped to the v5 package because it depends on
  SMPP v5 and 3GPP TS 23.041 behavior not present in the current v3.4 API.

## Hardening inputs

### C-Octet interoperability tolerance

SMPP v3.4 §3.1 defines C-Octet strings as ASCII followed by NUL. This module
deliberately accepts and preserves any non-NUL octet in these fields on both
decode and encode. Real peers use Latin-1 and UTF-8 alphanumeric addresses;
charset differences alone do not affect PDU framing. Encoding an embedded NUL
is rejected because it would terminate the field early. Send-side field length limits include the terminator and count octets, not
characters. Receive-side C-Octet lengths are bounded by the frame, not by
the v3.4 field maxima.

This tolerance does not apply to `data.ASCII`: explicitly encoding message
text as ASCII still rejects unrepresentable characters.

### Decode errors and response status

SMPP v3.4 §2.8 requires an error response for an invalid request, while §4.3
uses generic_nack for header errors. When `pdu.Parse` has read a valid-length
header and the entire declared body, invalid content returns `*pdu.DecodeError`
with the original header, a suggested command_status from §5.1.3, and an
unwrap-compatible cause. The reader is positioned at the next PDU. The session
layer chooses the appropriate response; the codec does not close connections.

Missing short-message bytes and declared UDH overflows suggest ESME_RINVMSGLEN;
malformed TLV framing suggests ESME_RINVOPTPARSTREAM. Overlong C-Octet fields,
sm_length 255 and number_of_dests 255 are accepted when their bytes fit the frame. A fully received frame too short for
its mandatory body suggests ESME_RINVCMDLEN (§4.3), rather than a parameter
length error. Section 3.3 also specifies invalid
parameter length for a variable-length parameter exceeding the supported
maximum. Unknown commands suggest ESME_RINVCMDID after consuming their body.
Malformed distribution-list names suggest ESME_RINVDLNAME and invalid
destination flags ESME_RINVDESTFLAG, as distinct fields in §5.1.3.

Incomplete frame reads wrap `pdu.ErrIncompletePDU` and the reader error.
Invalid command lengths wrap `pdu.ErrInvalidCommandLength`, corresponding to
ESME_RINVCMDLEN. These framing failures do not return a DecodeError and do not
establish a safe next-PDU boundary.

Both client and server receive loops report intact-frame decode errors and
continue reading. Invalid request fields receive the matching header-only
response with the suggested status (§2.8); header command errors receive
generic_nack (§§3.3, 4.3). Unknown commands receive ESME_RINVCMDID without a
disconnect, also clarified by v5 §2.8.2. A malformed response retires a matching
request with its decode error and a non-nil header-only PDU retaining the
received command ID, status and sequence. Invalid alert_notification body
fields are reported without a response (§2.8); header errors still receive
generic_nack (§4.3). Lost framing receives generic_nack with
ESME_RINVCMDLEN and the sequence from a complete header, then closes (§4.3).
Protocol error writes are bounded by the configured write timeout, or the
30-second bind timeout default when write timeouts are disabled.

Client binds require the corresponding bind response and original sequence
number (§§4.1.2, 4.1.4, 4.1.6, 5.1.4). A nonzero status fails
the exchange. A generic_nack with that sequence or NULL preserves its status
in BindError (§4.3.1). Client and server bind exchanges answer enquire_link and
continue waiting under the same absolute deadline. The server ignores
generic_nack before binding. SMPP v5 §2.4 Table 2-1 permits these PDUs in Open;
v3.4 §2.3 Table 2-1 lists them only in bound states. Open enquiry support applies
this current specification clarification without negotiating v5-only PDUs.
The server reuses one Connection for the bind exchange and bound I/O. Raw
handshake reads allocate no buffered reader before any input arrives; the read
buffer is created lazily when bound I/O first reads. Unknown-command diagnostics
report the first occurrence and one handshake-end aggregate with counts by
control kind. Intentional shutdown suppresses the aggregate. It accepts
up to eight pre-bind control PDUs in total (enquiries, nacks and unknown commands),
then closes on the ninth; the absolute bind deadline is never extended. This cap
is a resource policy, not a limit specified by SMPP. An intact unknown command
receives generic_nack/ESME_RINVCMDID and leaves the server waiting for a bind
(v3.4 §§3.3, 4.3; v5 §2.8.2). A malformed bind receives its matching header-only
bind response with the suggested status before closure (§2.8). Lost framing
receives generic_nack/ESME_RINVCMDLEN before closure (§4.3). Although requests
carry NULL command_status (§5.1.3), bind reception tolerates a nonzero value for
interoperability; the response status comes from authentication and admission.
The request window reserves correlation before writing, and
generic_nack is correlated as a response (§4.3.1). Conditional removal decides
which response, expiry or close operation owns each completion; a previous
connection cannot complete a later connection's reservations. Reservation in
Submit atomically rejects an existing sequence with ErrSequenceInUse before
queueing or writing. TimeSent records reservation time and is nonzero even if
a response precedes MarkWritten. Reservation alone does not authorize correlation:
the local ledger marks transmission immediately before transport writing; an
unsolicited response to a producer still waiting for admission is unexpected and
cannot remove its reservation. WrittenAt is zero until a successful transport
write, excludes pending writes from expiry, and supplies the expiry age.
MarkWritten never recreates a removed entry. Removal compares sequence, Owner
and ID, independently of either timestamp. Capacity counts written, unanswered
application requests; pending reservations and tracked enquire_link do not consume
the limit. WrittenLength excludes the current owner, whose local ledger remains
authoritative even when persisting the written marker fails. Such failures reach
OnReceivingError and trigger conditional metadata repair without closing the
transport or reporting a written PDU as unsent. A response-path Get deadline
or Delete error settles local permits and written count immediately,
reports OnReceivingError, and retains a silent cleanup identity. A failed Set
whose cleanup also fails is resolved by a successful Get confirming absence or
replacement; failed Gets preserve the retry obligation. A successful Get with a
row is usable even when its deadline has just elapsed. A missing response row
without a deadline error lets the local ledger arbitrate response versus expiry;
only its winner invokes a completion callback. A failed cleanup never abandons
a completion that another path has already claimed.
A lineage retains at most 1024 uncertain cleanup identities, excluding healthy
pending admissions, and rejects further
tracked work with ErrRequestCleanupFull while triggering rate-limited recovery.
The most recent 1024 rejected identities also retain small tombstones after
absence settlement, so a late Set cannot produce a close or expiry callback for
rejected work. Older rejections or commits after final cleanup may require
administrative removal. List should be complete, but truncated snapshots are
supported for uncertain identities: Get confirms absence or permits conditional
deletion of the exact row omitted from List.
Automatic keepalives never enter the store. A new automatic or application
enquiry retires the previous unanswered written application enquiry silently,
keeping at most one such live request per connection; retirement produces no
response, expiry or close completion callback. Cleanup errors are reported
without suppressing the keepalive. Automatic enquiry responses therefore use
OnUnexpectedPduResponse with tracking enabled.
Dead-generation reservations retain their session lineage across rebinds, and
only that lineage receives their completion callbacks. After a foreign session
finishes shutdown, scans and expiry may remove its leftover entries silently.
Sessions sharing a store also share window capacity; checks are advisory across
concurrent writers and backend metadata failures. Request counters start from a
random 64-bit process prefix, with a time/process-ID fallback. Silent removal of
foreign entries requires their exact ID and sequence in a known, finally closed
local owner's ledger, so unknown-process entries remain protected. Separate
process namespaces remain recommended: sequence/capacity limits are shared and
remote process death cannot be inferred. Absence cleanup across local Sessions
requires the same comparable store instance (use a pointer for custom stores).
Comparability is checked on the dynamic value, including nested interfaces.
Non-comparable decorators cannot establish backend identity across lineages;
their own-lineage recovery is unaffected.
Close and a later Session on that store also confirm absence for uncertain
admissions that never inserted rows. Rejected admission retries trigger
rate-limited recovery, including a sequence retained by an ambiguous Set.
Settling a closed lineage's final orphan clears its abandoned markers and hint;
active lineages retain markers until the completion arbiter consumes them.
An index of each root's members with active waits makes wait-cycle traversal
independent of the number of idle workers; registration and wait changes update
that index atomically with the global wait graph.
Failed cleanup or a full window triggers asynchronous, single-flight recovery;
ordinary writes do not list the store and the writer never waits for a scan.
Attempts are limited to one per ExpireCheckTimer, or one per second when the
timer is disabled. Per-generation outstanding accounting preserves the recovery
hint when a backend List fails quickly with no rows or reaches its deadline.
Each scan bounds backend work by one StoreAccessTimeOut and caps
uncertain-identity Gets at 256 across eligible lineages. Healthy scans start with
own-lineage resolution. If both own and closed foreign lineages need lookups,
each receives up to half the lookup count. A budget-exhausted scan advances its
starting phase for the next scan, ensuring each of own resolution, List cleanup
and foreign resolution can start with the full budget within three such scans.
Every store operation has its own deadline, limited by the overall budget's
remaining time. List deletions run with at most eight workers. Identity,
List-row, phase and background-lineage cursors persist across rebinds, so
unavailable rows cannot permanently starve another phase or later identities.
Closed owners retained in the store's local registry supply a shared cleanup hint
for periodic recovery, including when expiry is disabled and no submits occur.
Close bounds backend cleanup by one StoreAccessTimeOut, uses up to eight
parallel deletions and applies no per-scan Get-count cap. Every still-owned
transmitted request completes locally through OnClosePduRequest even if its row
is missing from List or the budget expires. This releases local capacity and
retains the exact identity for later silent cleanup, outside the uncertain
failed-admission cap. Store errors and callbacks run after the backend phase;
an external Close joins them. Abandoned identities never regain completion
ownership when their bounded replay tombstones are evicted. Expiry checks
shutdown before every backend operation and cancels pending operations, leaving
unfinished requests to close/recovery rather than extending Close by one timeout
per row.
A joined helper performs recovery away
from the writer and invokes OnClosePduRequest only when conditional deletion
wins and the local ledger still owns completion. A canceled queue admission
first attempts deletion; only a failed rollback consumes uncertain cleanup
capacity. It releases its reservation without an application completion callback. A Set error
also attempts conditional deletion in case the backend inserted the entry before
its acknowledgement failed. Pre-write counting and reservation share one store
budget; a fresh budget starts after transport I/O for MarkWritten. Response and
expiry operations have independent deadlines; callbacks run outside store budgets.
Window response tracking does not suppress request callbacks or automatic
responses when OnReceivedPduRequest is absent (§2.8).

Sequence numbers belong to the sending peer. A generic_nack rejecting our
response can therefore have the same sequence as an unrelated outstanding
request. Its header does not identify the rejected command, so this case cannot
be distinguished from a nack for that outstanding request.

Explicit Close attempts unbind before dropping the transport (v3.4 §4.2;
v5 §§2.3.5–2.3.6). The attempt uses a 50-millisecond write deadline and skips
an active writer, a partial write, or a permanent transport error, then interrupts
I/O and joins workers. An encoding error leaves
the transport intact and still permits this graceful unbind. Writers waiting for
transport access are rejected with ErrConnectionClosing, so application traffic
cannot be appended after unbind. After a partial write or permanent transport
error, both write paths reject further PDUs immediately, preventing another worker
from appending
to a partial frame while the error callback is still running. Encoding errors
leave the transport writable. A zero-byte timeout also leaves framing intact,
so a response can still be written before shutdown. No unbind_resp wait is
required by this shutdown API. TLS Connection.Close delegates to crypto/tls and
may also block until its close_notify write timeout.

Callback reentrancy is scoped to the owning session, server or standalone
connection. Library workers register their goroutine ID once at entry and remove
it on exit. Child connections share their instance's membership. Worker callback
dispatch performs no stack inspection or registry locking. Synchronous callbacks
on caller goroutines register only for their duration. Goroutine IDs come from
runtime.Stack's header; no stack symbols or function names are inspected.
At join time, one mutex protects both membership and a graph of goroutines
waiting for instances. Shutdown skips a join when the caller belongs to the
target, or when following the target's members' waits reaches an instance that
contains the caller. This prevents mutual or longer callback wait cycles.
The graph groups all workers of a Session or Server, so a connection Close or
StopSystemID may conservatively escape a join because a different worker of its
parent instance participates in a cycle.
One-way calls from another instance still join. Application goroutines have no
inherited membership, so external calls always join; waits made by the application
outside these shutdown APIs are not tracked. Shutdown coordinators aggregate
their child workers before publishing completion. OnClosed runs once for
explicit shutdown even with automatic rebinding enabled.
Callback-origin shutdown returns before workers finish: callbacks due from
in-flight processing or rebinding, queued-submission errors, request completion,
store cleanup and final OnClosed may still begin or finish. The calling callback
must return, and a subsequent external shutdown joins all of them.
Submit and SubmitContext first try queue admission without blocking. If a full
queue would make the connection's own writer wait for itself, they return
ErrSubmitQueueFull. Otherwise queue admission records its wait edge and rejects
with the same error if an existing shutdown or queue wait would close a cycle.
The edge is removed after enqueueing, cancellation or shutdown. A reader or
cleanup callback may wait for its own writer when no dependency cycle exists;
other callers retain normal backpressure. Reclamation
callbacks run on a joined helper, while OnSubmitError may run on the writer.
Queue admission and shutdown share a lock. Capacity notifications wake waiting
producers to retry admission; a freed slot cannot admit a producer after shutdown
has already stopped the writer. That rejection returns ErrConnectionClosing
without a completion callback.
If a notified producer cancels before enqueueing, it relays the notification
without filling the slot, preserving progress for other waiting producers.
Application admission also uses up to MaxWindowSize permits, held from queue
admission through completion and released before callbacks. Waiting producers'
reservations do not consume permits. Enquire_link bypasses this admission gate.
A callback on any library worker never waits for a full application window:
it returns ErrSubmitQueueFull, including when forwarding into another Session.
That wait requires a remote response and could outlive a shutdown-cycle escape.
Application goroutines spawned by callbacks are external and retain ordinary
backpressure. A count of registered workers is maintained at entry and exit;
only a blocking admission inspects it, so callback dispatch remains unchanged.
Context cancellation and shutdown release admission waits and reservations.
GetWindowSize counts written application requests, excluding pending requests
and enquire_link. Administrative Clear or direct deletion must occur only after
all owning sessions have closed; live completion must pass through the library's
arbiter and permit ledger.

Public Go SMPP libraries and their issue trackers repeatedly expose the same
failure classes: duplicated multipart sequence numbers, incorrect GSM 7-bit
escape accounting, UDH parsing mistakes, non-idempotent close paths, request
window races, dropped non-OK responses, optional-parameter gaps, and malformed
PDU parser crashes. Fixes in this repository should keep those classes covered
by focused unit, race, and fuzz tests before changing protocol behavior.

## SMSC server

Server behavior follows the supplied SMPP v3.4 Issue 1.2 text, including its
incorporated erratum SMPPV3.4-05Oct99-01: bind_transceiver carries the mandatory
`interface_version` field (§4.1.5). SMPP v5 remains a separate future protocol
boundary; its §3.2.1.3 also clarifies the header-only error-response convention.

- Bind responses echo request sequence numbers and identify the SMSC, using a
  maximum 16-octet C-string including the terminator (§§4.1.2, 4.1.4, 4.1.6;
  Tables 4-2, 4-4, 4-6). Failed binds omit the body, with `command_length = 16`.
- Successful binds advertise `sc_interface_version` (tag `0x0210`, value
  `0x34`) only when the ESME's `interface_version` is at least `0x34`
  (§3.4, §5.2.4, §5.3.2.25). Server delivery carrying optional parameters selects
  a compatible RX/TRX session or returns `UnsupportedOptionalParametersError` without sending.
  Application responses omit optional parameters for older peers and report
  the removal through `OnError`; caller PDUs retain their fields.
- `BindResp.Marshal` and `SubmitSMResp.Marshal` omit error bodies when no TLVs
  are explicitly registered (§§4.1.2, 4.1.4, 4.1.6, §4.4.2). Explicit error
  TLVs retain mandatory-field framing for parsing, including diagnostic
  `additional_status_info_text` (v3.4 §5.3.2.11; v5 §4.8.4.1). Decoders accept
  header-only errors and the legacy body form.
- Bound sessions always answer enquire_link (§4.11) and unbind (§4.2), echoing
  the request sequence number (§5.1.4). Unbind responses are written before
  closing. A repeated bind receives the matching bind response with
  `ESME_RALYBND` (§5.1.3). Application handlers cannot override housekeeping.
- Server-originated delivery selects only live RX or TRX sessions (§2.2,
  §4.6). Account binding allow-lists may include TX, RX and TRX. Inbound PDUs
  follow the ESME-issued column of §2.3 Table 2-1 before the handler is called:
  submit_sm, submit_multi, query_sm, cancel_sm, replace_sm and data_sm require
  TX/TRX; deliver_sm_resp and data_sm_resp require RX/TRX. For data_sm and its
  response, §§2.5–2.6 clarify the message direction despite the broader entries
  in the v3.4 table; v5 §2.4 Table 2-1 confirms these restrictions. For
  replace_sm, §2.7 explicitly permits TRX despite its omission in Table 2-1;
  v5 §2.4 confirms TX/TRX. Disallowed requests
  receive the matching response with `ESME_RINVBNDSTS` (§5.1.3) and a 16-octet
  header; disallowed responses are dropped. Header-only error responses also
  follow v5 §3.2.1.3. Handlers receive `ServerSessionInfo` and remain responsible
  for message processing and validating request fields.
- Account admission, reservations and session registration share one lock.
  Reservations retain their original account object until cleanup, including
  account removal and replacement. A session is visible only after the bind
  response has been written and its transceivable has started. The slot count
  includes handshakes with reservations until their goroutines exit.
- Non-positive read and write timeouts disable deadlines. A positive read
  timeout must exceed the configured server keep-alive interval. The bind
  deadline is cleared before bound I/O starts. Stop closes the listener and
  all handshaking and bound connections and waits for server connection
  cleanup, including application callbacks. StopSystemID joins the workers and
  callbacks of its affected connections, including an already-started shutdown.
- `SubmitToSystemID(ctx, systemID, p)` honours cancellation while waiting for
  queue capacity. Success means queued, not delivered; the application must
  track deliver_sm_resp (§4.6.2). A receipt submitted inside OnPDU can precede
  the submit_sm_resp for the message being handled, because the outbound daemon
  runs concurrently with the handler. Applications must coordinate ordering.
- OnError and ServerPDUHandler callbacks may call Stop or StopSystemID to
  initiate shutdown. The calling callback must return for cleanup to finish;
  other callbacks already running, discarded-submission errors, pending-request
  close callbacks and final OnClosed notifications may follow that return.
  External shutdown calls join all of them. Intentional shutdown receive errors
  are suppressed; each accepted submission that cannot be written is still
  reported with its PDU through SubmitError.

The server advertises v3.4, including when a peer requests a later version;
it does not implement v5-only PDUs. For pre-v3.4 peers, application code must
also keep generated message IDs within eight octets (§3.4).

Optional TLS is delegated to Go's `crypto/tls` with the caller's configuration.
No independent TLS protocol implementation or TLS conformance claim is made.

### Field maxima and response interoperability

`pdu.Validate(p)` is an opt-in check of all mandatory-field maxima from SMPP
v3.4 Tables 4-1–4-30 and §§5.2.21–24. It joins `*pdu.ValidationError` values,
with a field name, suggested command_status (§5.1.3), and an underlying cause.
It examines supplied fields even if an error response omits its body.
Transports do not call it automatically. The public constants advertise v3.3
as `0x33`, `ms_msg_wait_facilities` as tag `0x0030`, and the QoS time-to-live
value as exactly four octets (§5.2.4, Table 5-7, §5.3.2.9).

Validate suggests these statuses for C-Octet length violations (SMPP v3.4
§5.1.3, Table 5-2):

| Field | Suggested command_status | Value |
| --- | --- | --- |
| system_id | ESME_RINVSYSID | 0x0000000F |
| password | ESME_RINVPASWD | 0x0000000E |
| service_type | ESME_RINVSERTYP | 0x00000015 |
| schedule_delivery_time | ESME_RINVSCHED | 0x00000061 |
| validity_period | ESME_RINVEXPIRY | 0x00000062 |
| message_id | ESME_RINVMSGID | 0x0000000C |
| system_type, address_range, final_date | ESME_RINVPARLEN | 0x000000C2 |

C-Octet maxima include NUL (§3.1.1): bind system IDs/passwords/system types/
address ranges allow 16/9/13/41; service types 6; message IDs 65; times 17.
DataSM and AlertNotification addresses allow 65 (Tables 4-20/4-30); other
SME addresses allow 21. In strict v3.4 validation, unused deliver_sm times
and deliver_sm_resp message_id must be NULL (Tables 4-18/4-19). Time syntax
and the exact 1-or-17 size rule are not checked by this maximum validator.

Address does not know its containing PDU; call `pdu.Validate` before sending
to enforce the per-PDU 21/65-octet limits, including NUL. Address setters
accept up to 64 content octets for every PDU, and Marshal preserves them.
An application can therefore construct and send a submit_sm address exceeding
its 21-octet field maximum; a strict SMSC may reject it. This tolerance lets
a transit SMSC forward accepted traffic.

Every response accepts an omitted body, with empty fields, on success or
failure. Body omission on failure follows SMPP v5 §3.2.1.3; successful omission
is an interop tolerance, especially for the unused deliver_sm_resp message_id
(v3.4 §4.6.2). Full bodies remain readable. BindResp, SubmitSMResp, DataSMResp
and DeliverSMResp also accept a final unterminated system_id/message_id up to
the frame end. This is an interoperability tolerance; the specification still
requires NUL. After a NUL,
remaining bytes must form valid TLVs. Nonzero-status bind/submit responses
without TLVs normalize to a header-only response on marshal.

Public `Unmarshal` bounds mandatory fields and TLVs by `command_length`, even
when its buffer contains subsequent frames (§§3.2.1, 5.1.1). It rejects a
`command_id` belonging to a different concrete PDU type with `DecodeError`
and `ESME_RINVCMDID` (§5.1.2, Table 5-1). BindRequest and BindResp each accept
all three bind variants; decoding a BindRequest derives its BindingType from
the incoming command. Reusing a PDU replaces optional parameters and clears
omitted response-body fields.

### Receive and send length policy

SMPP v3.4 §3.3 permits supporting longer variable-length parameters. Receive
and marshal preserve representable fields, allowing a transit SMSC to forward
accepted traffic. Strict specification maxima are available through Validate.

| Field | Receive | Marshal/send | Construction / Validate | Reason |
| --- | --- | --- | --- | --- |
| C-Octet strings, including credentials | Any NUL-terminated frame-bounded octets | Any length within MAX_PDU_LEN; embedded NUL rejected | Address setter: 64 content octets; list-name setter: 20. Validate: per-PDU v3.4 maxima | Preserve long credentials and forwarded peer fields |
| sm_length / number_of_dests | 0–255 with all bytes/entries present | At most 255 | Message setters / destination Add: 254; Validate: 254 | One-octet wire fields permit 255 |
| Segment count / TLV value length | Structurally bounded | At most 255 segments / 65535 TLV octets | Existing guards retained | Wire representation limits |
| Response body | Missing body tolerated; bind/submit/data/deliver trailing unterminated string tolerated | Normal body emission, or header-only error normalization | Validate examines supplied fields | Avoid dropped binds and duplicate submission retries |
| Message, TLV, UDH and mandatory-body framing | Structural errors return DecodeError | Framing and MAX_PDU_LEN enforced | Structural guards retained | Keep field boundaries intact |

C-Octet strings preserve non-ASCII octets on receive and send despite v3.4
§3.1's ASCII definition: real peers use Latin-1/UTF-8 alphanumeric addresses.
`data.ASCII.Encode` remains strict when callers explicitly request ASCII text.
The server's own ServerSettings.SystemID validation at Start is unchanged.
Accepted terminated fields re-marshal byte-identically. A tolerated missing
terminator is added on send; adding it can exceed MAX_PDU_LEN and return ErrEncode.

SubmitToSystemID checks encoding synchronously before queuing. Encoding errors
reported asynchronously keep the bind. An invalid handler response reports
OnError and gets a header-only ESME_RSYSERR reply with the request sequence.
Client handlers follow the same fallback, reporting through OnSubmitError
(v3.4 §§2.8, 5.1.3; v5 §3.2.1.3). The handler's closeBind decision still applies:
when true, the session closes
after writing the fallback; when false, the bind remains open.

### User data and packed GSM conventions

A bounded UDH with a truncated final information element is ignored as required
by TS 23.040 §9.2.3.24. `ShortMessage.UDH()` exposes no parsed elements;
`IgnoredUDH()` exposes the preserved raw header. Its declared header octets are
skipped before returning payload and retained on marshal. A declared header
that exceeds the short_message field still produces a message-length error.

SMPP supplies an octet length without a GSM septet count. Packed encoding uses
CR in a spare full septet, borrowing TS 23.038 §6.1.2.3.1's USSD convention;
SMS packing's normative fill rule is zero padding (§6.1.2.1.1). Decoding supports
both conventions and calculates septet capacity after UDH fill bits. The second rule doubles a terminal CR on an octet boundary. With no septet
count, `AAAAAAA` with zero padding and `AAAAAAA@` have identical bytes;
`AAAAAAA\r` with doubled padding and literal `AAAAAAA\r\r` also have
identical bytes. Single-part messages cannot resolve these collisions without
changing their text or adding an external count. The inferred decoder follows
the padding convention;
`data.DecodeGSM7Packed` accepts an explicit count when available. Raw packed
messages are resegmented only when decode and re-encode preserve their bytes,
with ambiguous tails additionally requiring retained original source text.
Packed splitting moves boundaries to avoid ambiguous tails in generated parts.
UDH fill bits have no receiver meaning (TS 23.040 §9.2.3.24) and are ignored.
An escaped final septet is retained rather than interpreted as padding.

The GSM default alphabet and extension table follow ETSI TS 23.038 V19.0.0
§§6.2.1–6.2.1.1; §6.2.1.1 is unchanged in 3GPP V20.0.0 (2026-06), apart from
editorial reference and note labels. The newer source is
https://www.3gpp.org/ftp/Specs/archive/23_series/23.038/23038-k00.zip.

Data-coding groups select their alphabet under ETSI TS 23.038 V19.0.0 §4,
unchanged in 3GPP V20.0.0 (2026-06). GSM-alphabet
custom encodings use escape-safe 160/153 septet budgets. UCS2 uses encoded
UTF-16 octets. Compressed data-coding groups with a defined alphabet require
TS 23.042 support and are retained as raw binary rather than encoded as
uncompressed text. Reserved alphabet selections use the GSM default fallback
from TS 23.038 §4, including when the compression bit is set. SMPP data_coding
05/09/0A/0D/0E are distinct opaque text alphabets (§5.2.19), with 140-octet
budgets; without a character-aware codec they remain unsplit if they fit and
otherwise return an error. A caller-supplied custom codec without a splitter
constructs at most 254 octets including UDH (SMPP v3.4 §§5.2.21–22);
longer text returns an error. Parsed 255-octet fields still re-marshal unchanged.

Latin-1, Cyrillic and Hebrew mappings follow the Unicode Consortium's official
`ISO8859/8859-1.TXT`, `8859-5.TXT` and `8859-8.TXT` tables, with an upstream
limitation in the Cyrillic and Hebrew codecs: x/text decodes C1 controls at
`0x80`–`0x9F` as U+FFFD and refuses to encode U+0080–U+009F.
Related discussion that requests the inverse change
(replacing Latin-1 C1 controls) is at https://github.com/golang/go/issues/23706;
it does not track preserving Cyrillic/Hebrew controls. No local workaround is
applied. Latin-1 preserves its C1 controls, with a dedicated test to signal
when upstream changes that behavior. Undefined Hebrew octets retain
x/text's replacement-character decode behavior.

Optional-parameter lookups use the first occurrence consistently; all
occurrences remain accessible in wire order. UDH concatenation information
uses the last concatenation IE under TS 23.040 §9.2.3.24. Splitting replaces
UDH concatenation metadata and drops stale SAR TLVs.
