# Specification compliance

## Authorities

The API implements SMPP v3.4 PDU and session behavior. The authorities below
were checked on 2026-10-11. ETSI V19.0.0 and 3GPP V20.0.0 were checked for TS
23.038 and TS 23.040: TS 23.040 §§9.2.3.24–9.2.3.24.8 are unchanged, while TS
23.038 §4 and §6.2.1.1 contain only editorial changes, with no normative change
from V19.0.0.

| Authority | Document / version checked | Source |
| --- | --- | --- |
| SMS Forum | v3.4 Issue 1.2, 1999-10-12, incorporating SMPPV3.4-05Oct99-01 | [Specification](https://smpp.org/SMPP_v3_4_Issue1_2.pdf) |
| SMS Forum | v5, 2003-02-19 | [Specification](https://smpp.org/SMPP_v5.pdf) |
| 3GPP / ETSI | TS 23.038: ETSI V19.0.0; 3GPP V20.0.0 (2026-06) | [ETSI V19](https://www.etsi.org/deliver/etsi_ts/123000_123099/123038/19.00.00_60/ts_123038v190000p.pdf), [23038-k00.zip](https://www.3gpp.org/ftp/Specs/archive/23_series/23.038/23038-k00.zip) |
| 3GPP / ETSI | TS 23.040: ETSI V19.0.0; 3GPP V20.0.0 (archive published 2026-09-22) | [ETSI V19](https://www.etsi.org/deliver/etsi_ts/123000_123099/123040/19.00.00_60/ts_123040v190000p.pdf), [23040-j00.zip](https://www.3gpp.org/ftp/Specs/archive/23_series/23.040/23040-j00.zip), [23040-k00.zip](https://www.3gpp.org/ftp/Specs/archive/23_series/23.040/23040-k00.zip) |
| 3GPP | TS 23.041 archive | [23041-k00.zip](https://www.3gpp.org/ftp/Specs/archive/23_series/23.041/23041-k00.zip) |
| Unicode | Official ISO-8859 mapping tables | [8859-1.TXT](https://www.unicode.org/Public/MAPPINGS/ISO8859/8859-1.TXT), [8859-5.TXT](https://www.unicode.org/Public/MAPPINGS/ISO8859/8859-5.TXT), [8859-8.TXT](https://www.unicode.org/Public/MAPPINGS/ISO8859/8859-8.TXT) |
| ITU-T | Numbering and network identification | [E.164](https://www.itu.int/rec/T-REC-E.164), [E.212](https://www.itu.int/rec/T-REC-E.212) |

No IETF RFC governs SMPP binary PDUs or bind/session behavior. The [RFC Editor
index](https://www.rfc-editor.org/rfc-index.xml) and [Datatracker SMPP document
query](https://datatracker.ietf.org/api/v1/doc/document/?name__contains=smpp&format=json)
agree: the listed drafts concern CPIM or URI/ENUM mapping and are expired or
replaced. Messaging URI, ENUM, SIP, email-header and PASSporT RFCs do not amend
this implementation unless those features are implemented directly.

## Baseline

- SMPP v3.4 Issue 1.2, 1999-10-12 is the active protocol baseline for the
  current API. Optional-parameter TLV framing is in section 3.2.4.1, optional
  tag definitions are in section 5.3.2, `interface_version` is section 5.2.4,
  `data_coding` is section 5.2.19, and `short_message` is section 5.2.22.

- GSM 7-bit default alphabet behavior follows 3GPP TS 23.038 section 6.2.1 and
  the extension table in section 6.2.1.1. National-language single and locking
  shift tables are in sections 6.2.1.2 and Annex A.

- UDH and concatenated SMS behavior follows 3GPP TS 23.040 section 9.2.3.24, the
  8-bit concatenation IE in section 9.2.3.24.1, and the 16-bit concatenation IE
  in section 9.2.3.24.8.

- 3GPP TS 23.041 is relevant to SMPP v5 broadcast-message support. The current
  v3.4 API does not expose broadcast PDUs.

- ITU-T E.164 and E.212 govern numbering and network identifiers around SMPP
  deployments. This library exposes SMPP TON/NPI/address fields and should not
  reject deployment-specific values unless an explicit validator is requested.

SMPP v5 is the latest published SMPP specification. The bind, housekeeping and
error-handling sections identify the v5 clarifications used by this v3.4 API;
v5-only PDUs remain outside its version boundary. The GSM extension table
(§6.2.1.1) and coding groups (§4) use the V19/V20 relationship recorded above.
ISO-8859 mappings use the Unicode tables above.

## Policies

SMPP v3.4 §3.3 permits supporting longer variable-length parameters. Receive and
marshal preserve representable fields, allowing a transit SMSC to forward
accepted traffic. Strict specification maxima are available through Validate.

| Field | Receive | Marshal/send | Construction / Validate | Reason |
| --- | --- | --- | --- | --- |
| C-Octet strings, including credentials | Any NUL-terminated frame-bounded octets | Any length within MAX_PDU_LEN; embedded NUL rejected | Address setter: 64 content octets; list-name setter: 20. Validate: per-PDU v3.4 maxima | Preserve long credentials and forwarded peer fields |
| sm_length / number_of_dests | 0–255 with all bytes/entries present | At most 255 | Message setters / destination Add: 254; Validate: 254 | One-octet wire fields permit 255 |
| Segment count / TLV value length | Structurally bounded | At most 255 segments / 65535 TLV octets | Construction bounds enforced | Wire representation limits |
| Response body | Missing body tolerated; bind/submit/data/deliver trailing unterminated string tolerated | Normal body emission, or header-only error normalization | Validate examines supplied fields | Avoid dropped binds and duplicate submission retries |
| Message, TLV, UDH and mandatory-body framing | Structural errors return DecodeError | Framing and MAX_PDU_LEN enforced | Structural bounds enforced | Keep field boundaries intact |

The server validates its own `ServerSettings.SystemID` at `Start`. Accepted
terminated fields re-marshal byte-identically. A tolerated missing terminator is
added on send; that addition can exceed `MAX_PDU_LEN` and return `ErrEncode`.

## Framing and headers

Public `Unmarshal` bounds mandatory fields and TLVs by `command_length`, even
when its buffer contains subsequent frames (§§3.2.1, 5.1.1). It rejects a
`command_id` belonging to a different concrete PDU type with `DecodeError` and
`ESME_RINVCMDID` (§5.1.2, Table 5-1). BindRequest and BindResp each accept all
three bind variants; decoding a BindRequest derives its BindingType from the
incoming command.

Reusing a PDU replaces optional parameters and clears omitted response-body
fields.

Incomplete frame reads wrap `pdu.ErrIncompletePDU` and the reader error. Invalid
command lengths wrap `pdu.ErrInvalidCommandLength`, corresponding to
ESME_RINVCMDLEN. These framing failures do not return a DecodeError and do not
establish a safe next-PDU boundary.

## Bind and session states

SMPP v3.4 Issue 1.2 incorporates erratum SMPPV3.4-05Oct99-01: a bind_transceiver
must carry `interface_version` (§4.1.5).

Client binds require the corresponding bind response and original sequence
number (§§4.1.2, 4.1.4, 4.1.6, 5.1.4). A nonzero status fails the exchange. A
generic_nack with that sequence or NULL preserves its status in BindError
(§4.3.1).

Client and server bind exchanges answer enquire_link and continue waiting under
the same absolute deadline. The server ignores generic_nack before binding. SMPP
v5 §2.4 Table 2-1 permits these PDUs in Open; v3.4 §2.3 Table 2-1 lists them
only in bound states.

Open enquiry support applies this current specification clarification without
negotiating v5-only PDUs. The server reuses one Connection for the bind exchange
and bound I/O. Raw handshake reads allocate no buffered reader before any input
arrives; the read buffer is created lazily when bound I/O first reads.

Unknown-command diagnostics report the first occurrence and one handshake-end
aggregate with counts by control kind. Intentional shutdown suppresses the
aggregate. The server accepts up to eight pre-bind control PDUs in total
(enquiries, nacks and unknown commands), then closes on the ninth; the absolute
bind deadline is never extended.

This cap is a resource policy, not a limit specified by SMPP. An intact unknown
command receives generic_nack/ESME_RINVCMDID and leaves the server waiting for a
bind (v3.4 §§3.3, 4.3; v5 §2.8.2). A malformed bind receives its matching
header-only bind response with the suggested status before closure (§2.8). Other
known requests received before the bind, such as unbind, close the connection
without a response; this is the current server handshake behavior.

Lost framing receives generic_nack/ESME_RINVCMDLEN before closure (§4.3).
Although requests carry NULL command_status (§5.1.3), bind reception tolerates a
nonzero value for interoperability; the response status comes from
authentication and admission. An account binding-type allow-list rejection or an
exhausted MaxSessions limit returns ESME_RBINDFAIL (§5.1.3).

Bind responses echo request sequence numbers and identify the SMSC, using a
maximum 16-octet C-string including the terminator (§§4.1.2, 4.1.4, 4.1.6;
Tables 4-2, 4-4, 4-6). Failed binds omit the body, with `command_length = 16`.

Successful binds advertise `sc_interface_version` (tag `0x0210`, value `0x34`)
only when the ESME's `interface_version` is at least `0x34` (§3.4, §5.2.4,
§5.3.2.25).

The server advertises v3.4, including when a peer requests a later version; it
does not implement v5-only PDUs. For pre-v3.4 peers, application code must also
keep generated message IDs within eight octets (§3.4).

Client bind exchanges use `DefaultBindTimeout` (30 seconds) for the write and
response read. `WithBindTimeout` changes it; non-positive values select the
default. Successful exchanges clear the deadline before session I/O. Built-in
connectors implement `ConnectContext`, and rebind shutdown cancels bind I/O. See
[shutdown](#shutdown-contract) for custom connector and dialer ownership.

Server-originated delivery selects only live RX/TRX sessions (v3.4 §§2.2, 4.6);
account allow-lists may include TX, RX and TRX. SubmitToSystemID returns
ErrNoLiveSession when none is live. Optional parameters additionally require
v3.4 or later: if live RX/TRX sessions exist but none accepts TLVs, submission
returns UnsupportedOptionalParametersError without sending.

`SubmitToSystemID(ctx, systemID, p)` honours cancellation while waiting for
queue capacity. Success means queued, not delivered; the application must track
deliver_sm_resp (§4.6.2). A receipt submitted inside OnPDU can precede the
submit_sm_resp for the message being handled, because the outbound daemon runs
concurrently with the handler.

Applications must coordinate ordering.

Inbound PDUs follow the ESME-issued column of §2.3 Table 2-1 before the handler
is called, as shown below. For data_sm and its response, §§2.5–2.6 clarify the
message direction despite the broader entries in the v3.4 table; v5 §2.4 Table
2-1 confirms these restrictions. For replace_sm, §2.7 explicitly permits TRX
despite its omission in Table 2-1; v5 §2.4 confirms TX/TRX.

Disallowed requests receive the matching response with `ESME_RINVBNDSTS`
(§5.1.3) and a 16-octet header; disallowed responses are dropped. Header-only
error responses also follow v5 §3.2.1.3. Handlers receive `ServerSessionInfo`
and remain responsible for message processing and validating request fields.

Account admission, reservations and session registration share one lock.
Reservations retain their original account object until cleanup, including
account removal and replacement. A session is visible only after the bind
response has been written and its transceivable has started.

The slot count includes handshakes with reservations until their goroutines
exit.

| Inbound ESME operation | TX | RX | TRX |
| --- | --- | --- | --- |
| submit_sm, submit_multi, query_sm, cancel_sm, replace_sm, data_sm | Yes | No | Yes |
| deliver_sm_resp, data_sm_resp | No | Yes | Yes |
| generic_nack, enquire_link_resp, unbind_resp | Yes | Yes | Yes |
| enquire_link, unbind, repeated binds | Library handles | Library handles | Library handles |
| Other ESME-issued PDUs | No | No | No |

## Housekeeping

Bound server sessions always answer enquire_link (§4.11) and unbind (§4.2),
echoing the request sequence number (§5.1.4). Unbind responses are written
before closing. A repeated bind receives the matching bind response with
`ESME_RALYBND` (§5.1.3).

Server handlers cannot override housekeeping. The default client path also
answers enquire_link and unbind. When OnAllPDU handles a request, or
OnReceivedPduRequest runs without EnableAutoRespond, the client callback must
return the reply itself (§§4.2, 4.11).

During Open, enquire_link and pre-bind generic_nack follow the rules in [Bind
and session states](#bind-and-session-states). After binding, generic_nack
correlates by sequence as a response (v3.4 §4.3.1). Its ambiguous sequence space
is described in [Known limitations](#known-limitations).

## Error handling

### DecodeError and response status

SMPP v3.4 §2.8 requires an error response for an invalid request, while §4.3
uses generic_nack for header errors. When `pdu.Parse` has read a valid-length
header and the entire declared body, invalid content returns `*pdu.DecodeError`
with the original header, a suggested command_status from §5.1.3, and an
unwrap-compatible cause. The reader is positioned at the next PDU.

The session layer chooses the appropriate response; the codec does not close
connections.

| Invalid content in a fully received frame | Suggested status (v3.4 §5.1.3) | Clause |
| --- | --- | --- |
| Missing short_message bytes; declared UDH overflow | `ESME_RINVMSGLEN` | §§5.2.21–22; TS 23.040 §9.2.3.24 |
| TLV framing | `ESME_RINVOPTPARSTREAM` | §3.2.4.1 |
| Frame too short for mandatory body | `ESME_RINVCMDLEN` | §4.3 |
| Unknown command | `ESME_RINVCMDID` after consuming body | §§3.3, 4.3 |
| Distribution-list name | `ESME_RINVDLNAME` | §5.1.3 |
| Destination flag | `ESME_RINVDESTFLAG` | §5.1.3 |
| Other invalid parameter length | `ESME_RINVPARLEN` | §3.3 |

Overlong C-Octet fields, `sm_length = 255` and `number_of_dests = 255` are
accepted when their bytes fit the frame. Section 3.3 also specifies invalid
parameter length for a variable-length parameter exceeding the supported
maximum.

Both client and server receive loops report intact-frame decode errors and
continue reading. Invalid request fields receive the matching header-only
response with the suggested status (§2.8); header command errors receive
generic_nack (§§3.3, 4.3). Unknown commands receive ESME_RINVCMDID without a
disconnect, also clarified by v5 §2.8.2.

A malformed response retires a matching request with its decode error and a
non-nil header-only PDU retaining the received command ID, status and sequence.
Invalid alert_notification body fields are reported without a response (§2.8);
header errors still receive generic_nack (§4.3). Lost framing receives
generic_nack with ESME_RINVCMDLEN and the sequence from a complete header, then
closes (§4.3).

Protocol error and close-path response writes use the [write deadline
policy](../README.md#write-deadlines), including a bounded fallback when
WriteTimeout is non-positive.

### EncodeError

Encoding failures are available through `ByteBuffer.Err()` and `errors.Is(err,
pdu.ErrEncode)`. `Connection.WritePDU` returns `*pdu.EncodeError` with the
offending PDU; `OnSubmitError` receives it while the session stays connected.
Check buffer errors before writing marshalled bytes.

SubmitToSystemID checks encoding synchronously before queuing. Encoding errors
reported asynchronously keep the bind. An invalid handler response reports
OnError and gets a header-only ESME_RSYSERR reply with the request sequence.

Client handlers follow the same fallback, reporting through OnSubmitError (v3.4
§§2.8, 5.1.3; v5 §3.2.1.3). The handler's closeBind decision still applies: when
true, the session closes after writing the fallback; when false, the bind
remains open.

### Validation

`pdu.Validate(p)` is an opt-in check of all mandatory-field maxima from SMPP
v3.4 Tables 4-1–4-30 and §§5.2.21–24. It joins `*pdu.ValidationError` values,
with a field name, suggested command_status (§5.1.3), and an underlying cause.
It examines supplied fields even if an error response omits its body.

Transports do not call it automatically. The public constants advertise v3.3 as
`0x33`, `ms_msg_wait_facilities` as tag `0x0030`, and the QoS time-to-live value
as exactly four octets (§5.2.4, Table 5-7, §5.3.2.9).

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
DataSM and AlertNotification addresses allow 65 (Tables 4-20/4-30); other SME
addresses allow 21. In strict v3.4 validation, unused deliver_sm times and
deliver_sm_resp message_id must be NULL (Tables 4-18/4-19).

Time syntax and the exact 1-or-17 size rule are not checked by this maximum
validator.

SMPP v3.4 §3.1 defines C-Octet strings as ASCII followed by NUL, but the field
codecs preserve non-ASCII non-NUL octets used in Latin-1/UTF-8 alphanumeric
addresses. Charset differences do not affect framing. Embedded NUL is rejected
because it would terminate the field early. Send maxima include NUL and count
octets; receive lengths are bounded by the frame.

Address does not know its containing PDU; call `pdu.Validate` before sending to
enforce the per-PDU 21/65-octet limits, including NUL. Address setters accept up
to 64 content octets for every PDU, and Marshal preserves them. An application
can therefore construct and send a submit_sm address exceeding its 21-octet
field maximum; a strict SMSC may reject it.

This tolerance lets a transit SMSC forward accepted traffic.

### Response bodies

`BindResp.Marshal` and `SubmitSMResp.Marshal` omit error bodies when no TLVs are
explicitly registered (§§4.1.2, 4.1.4, 4.1.6, §4.4.2). Explicit error TLVs
retain mandatory-field framing for parsing, including diagnostic
`additional_status_info_text` (v3.4 §5.3.2.11; v5 §4.8.4.1). Decoders accept
header-only errors and the legacy body form.

Every response accepts an omitted body, with empty fields, on success or
failure. Body omission on failure follows SMPP v5 §3.2.1.3; successful omission
is an interop tolerance, especially for the unused deliver_sm_resp message_id
(v3.4 §4.6.2). Full bodies remain readable.

BindResp, SubmitSMResp, DataSMResp and DeliverSMResp also accept a final
unterminated system_id/message_id up to the frame end. This is an
interoperability tolerance; the specification still requires NUL. After a NUL,
remaining bytes must form valid TLVs.

Nonzero-status bind/submit responses without TLVs normalize to a header-only
response on marshal.

## Request window

### Store contract and completion arbiter

Window tracking requires a positive StoreAccessTimeOut and nonzero MaxWindowSize
(at most 255). With positive PduExpireTimeOut, use a positive ExpireCheckTimer;
non-positive timers disable expiry checks.

`RequestStore` operations are concurrent and context-bounded. Reservations
remain until explicit removal; cache eviction cannot remove outstanding work.
Preserve sequence, `Owner`, `ID`, `Lineage`, `TimeSent` and `WrittenAt` through
`Set`, `Get`, `List` and serialization. List should be complete, but may return
stale or truncated snapshots. Recovery confirms each uncertain identity with Get
and conditionally deletes its exact matching row even if List omitted it. A
canceled List snapshot cannot establish absence.

The [store-contract table](../README.md#store-contract) defines each operation.
Get returns false only for absence or cancellation; an uncanceled absent read
can settle an uncertain admission. Before direct removal or Clear of
library-owned reservations, close every session using that store. Session
cleanup uses conditional deletion of its own reservations.

The request window reserves correlation before writing; housekeeping defines
generic_nack correlation (v3.4 §4.3.1). Conditional removal and the local ledger
decide which response, expiry or close operation owns each completion; a
successful removal must also claim that ledger. A response finding the row
absent can claim it before expiry does. A previous connection cannot complete a
later connection's reservations. Reservation in Submit atomically rejects an
existing sequence with ErrSequenceInUse before queueing or writing.

TimeSent records reservation time and is nonzero even if a response precedes
MarkWritten. Reservation alone does not authorize correlation: the local ledger
marks transmission immediately before transport writing; an unsolicited response
to a producer still waiting for admission is unexpected and cannot remove its
reservation. WrittenAt is zero until a successful transport write, excludes
pending writes from expiry, and supplies the expiry age.

Capacity counts written, unanswered application requests; pending reservations
and tracked enquire_link do not consume the limit.

WrittenLength excludes the current owner, whose local ledger remains
authoritative even when persisting the written marker fails. Such failures reach
OnReceivingError and trigger conditional metadata repair without closing the
transport or reporting a written PDU as unsent. A successful response-path Get
with a row is usable even if its deadline has just elapsed. A missing response
row without a deadline error lets the local ledger arbitrate response versus
expiry; only its winner invokes a completion callback. A Get deadline or Delete
error settles local permits and written count immediately, reports
OnReceivingError, and retains an unclaimed request's exact identity only for
silent cleanup. Failed cleanup never abandons a completion another path has
already claimed.

A failed Set whose cleanup also fails is resolved by a successful Get confirming
absence or replacement; failed Gets preserve the retry obligation. A lineage
retains at most 1024 uncertain cleanup identities, excluding healthy queued and
admission-waiting requests, successful cancellation rollbacks and identities
retained after close completion. Further tracked work returns
ErrRequestCleanupFull while triggering rate-limited recovery.

The most recent 1024 rejected identities also retain small tombstones after
absence settlement, so a late-committing Set is removed silently during close or
recovery, without a close or expiry callback for rejected work. The [known
limitations](#request-ownership-and-generic_nack) describe late-commit cleanup
beyond that retained history. Abandonment suppresses completion for as long as
its local cleanup identity remains, even after its replay tombstone is evicted.

Automatic keepalives never enter the store.

A new automatic or application enquiry retires the previous unanswered written
application enquiry silently, keeping at most one such live request per
connection; retirement produces no response, expiry or close completion
callback. Cleanup errors are reported without suppressing the keepalive.
Automatic enquiry responses therefore use OnUnexpectedPduResponse with tracking
enabled.

### Ownership and lineage

Dead-generation reservations retain their session lineage across rebinds, and
only that lineage receives their completion callbacks. After a foreign session
finishes shutdown, scans and expiry may remove its leftover entries silently.
Sessions sharing a store also share window capacity; checks are advisory across
concurrent writers and backend metadata failures.

Owner, Lineage and ID counters start from a random 64-bit process prefix, with a
time/process-ID fallback if the entropy reader fails. Collisions between
processes or restarts are negligible. Silent removal of foreign entries requires
their exact ID and sequence in a known, finally closed local owner's ledger, so
unknown-process entries remain protected.

Absence cleanup across local Sessions requires the same comparable store
instance (use a pointer for custom stores), rather than independent wrappers
around one backend. Comparability is checked on the dynamic value, including
nested interfaces. Non-comparable decorators are safe to use but cannot
establish backend identity for absence settlement across lineages; their
own-lineage recovery is unaffected. Close and a later Session on that store also
confirm absence for uncertain admissions that never inserted rows. Rejected
admission retries trigger rate-limited recovery, including a sequence retained
by an ambiguous Set.

Settling a closed lineage's final orphan clears its abandoned markers and hint;
active lineages retain markers until the completion arbiter consumes them.

### Recovery

Failed cleanup or a full window triggers asynchronous, single-flight recovery;
ordinary writes do not list the store and the writer never waits for a scan.
Attempts are limited to one per ExpireCheckTimer, or one per second when the
timer is disabled. Per-generation outstanding accounting preserves the recovery
hint when a backend List fails quickly with no rows or reaches its deadline.

Each scan bounds backend work by one StoreAccessTimeOut and limits cleanup
identity Get calls to 256 across eligible lineages. Healthy scans start with
own-lineage resolution; when both own and finally closed foreign lineages need
lookups, each gets up to half the count. A budget-exhausted scan rotates the
starting phase for the next scan, so own resolution, List cleanup and foreign
resolution each start with the full budget within three such scans. Every store
operation gets a fresh deadline limited by the remaining scan budget. List
cleanup runs with at most eight workers. Identity, List-row, phase and
background-lineage cursors persist across rebinds, letting later scans proceed
past unavailable rows.

Finally closed owners retained in the store's local registry supply a shared
cleanup hint for periodic recovery, including when expiry is disabled and there
are no submissions or own-lineage hints.

A joined helper performs recovery away from the writer and invokes
OnClosePduRequest only when conditional deletion wins and the local ledger still
owns completion. Recovery runs whether or not expiry is enabled. A canceled
queue admission attempts conditional deletion without a completion callback;
only a failed rollback consumes uncertain cleanup capacity.

A Set error also attempts conditional deletion in case the backend inserted the
entry before its acknowledgement failed. SubmitContext reserves under its own
store-access budget before admission. Pre-write counting uses a separate budget,
and MarkWritten gets a fresh budget after transport I/O. Response and expiry
operations have independent deadlines; callbacks run outside store budgets.
Expiry checks shutdown before every backend operation and cancels operations in
progress, leaving unfinished requests for close or recovery instead of extending
Close by one timeout per row.

Window response tracking does not suppress request callbacks or automatic
responses when OnReceivedPduRequest is absent (§2.8).

### Admission and permits

Submit and SubmitContext first try queue admission without blocking. If a full
queue would make the connection's own writer wait for itself, they return
ErrSubmitQueueFull. Otherwise queue admission records its wait edge and rejects
with the same error if an existing shutdown or queue wait would close a cycle.

The edge is removed after enqueueing, cancellation or shutdown. A reader or
cleanup callback may wait for its own writer when no dependency cycle exists;
other callers retain normal backpressure. Reclamation callbacks run on a joined
helper, while OnSubmitError may run on the writer.

Queue admission and shutdown share a lock. Capacity notifications wake waiting
producers to retry admission; a freed slot cannot admit a producer after
shutdown has already stopped the writer. That rejection returns
ErrConnectionClosing without a completion callback.

If a notified producer cancels before enqueueing, it relays the notification
without filling the slot, preserving progress for other waiting producers.
Application admission also uses up to MaxWindowSize permits, held from queue
admission through completion and released before callbacks. Waiting producers'
reservations do not consume permits.

Enquire_link bypasses this admission gate. A callback on any library worker
never waits for a full application window: it returns ErrSubmitQueueFull,
including when forwarding into another Session. That wait requires a remote
response and could outlive a shutdown-cycle escape.

Application goroutines spawned by callbacks retain ordinary backpressure. A
count of registered workers is maintained at entry and exit; only a blocking
admission inspects it; callback dispatch does not inspect that count. Context
cancellation and shutdown release admission waits and reservations.

GetWindowSize counts written application requests, excluding pending requests
and enquire_link. Live completion must pass through the library's arbiter and
permit ledger.

Rejected submissions return errors directly without `OnSubmitError`. Accepted
queued PDUs discarded at shutdown reach that callback with
`ErrConnectionClosing`; success means queued rather than written.

## Shutdown contract

Explicit Close attempts unbind before dropping the transport (v3.4 §4.2; v5
§§2.3.5–2.3.6). The attempt uses a 50-millisecond write deadline and skips an
active writer, a partial write, or a permanent transport error, then interrupts
I/O and joins workers. An encoding error leaves the transport intact and still
permits this graceful unbind.

Writers waiting for transport access are rejected with ErrConnectionClosing, so
application traffic cannot be appended after unbind. After a partial write or
permanent transport error, both write paths reject further PDUs immediately,
preventing another worker from appending to a partial frame while the error
callback is still running. Encoding errors leave the transport writable.

A zero-byte timeout also leaves framing intact, so a response can still be
written before shutdown. No unbind_resp wait is required by this shutdown API.
TLS Connection.Close delegates to crypto/tls and may also block until its
close_notify write timeout.

Windowed Close bounds backend cleanup by one StoreAccessTimeOut, with up to
eight concurrent deletions and a fresh deadline for each operation, limited by
the remaining budget. Final Close has no background-scan Get-count cap. Local
bookkeeping, store-error reporting and callbacks are outside the backend budget.

Every still-owned transmitted request that has not completed or been abandoned
completes through OnClosePduRequest, even if List omitted its row or backend
cleanup ran out of time. This releases local capacity and keeps the exact
identity for later silent recovery, outside the uncertain-identity limit. Store
errors and callbacks run after the backend phase, and an external Close joins
them. Completed or abandoned requests never regain completion ownership,
including after replay tombstone eviction.

Callback reentrancy is scoped to the owning session, server or standalone
connection. Library workers register their goroutine ID once at entry and remove
it on exit. Child connections share their instance's membership.

Worker callback dispatch performs no stack inspection or registry locking.
Synchronous callbacks on caller goroutines register only for their duration.
Goroutine IDs come from runtime.Stack's header; no stack symbols or function
names are inspected.

At join time, one mutex protects both membership and a graph of goroutines
waiting for instances. Shutdown skips a join when the caller belongs to the
target, or when following the target's members' waits reaches an instance that
contains the caller. This prevents mutual or longer callback wait cycles.

The graph groups all workers of a Session or Server, so a connection Close or
StopSystemID may conservatively escape a join because a different worker of its
parent instance participates in a cycle. One-way calls from another instance
still join. Application goroutines have no inherited membership, so external
calls always join; waits made by the application outside these shutdown APIs are
not tracked.

Shutdown coordinators aggregate their child workers before publishing
completion. OnClosed runs once for explicit shutdown even with automatic
rebinding enabled. Callback-origin shutdown returns before workers finish:
callbacks due from in-flight processing or rebinding, queued-submission errors,
request completion, store cleanup and final OnClosed may still begin or finish.

The calling callback must return, and a subsequent external shutdown joins all
of them.

An index of each root's members with active waits makes wait-cycle traversal
independent of the number of idle workers; registration and wait changes update
that index atomically with the global wait graph.

A non-positive server account ReadTimeout disables its read deadline; a positive
value must exceed its keepalive interval. Client NewSession instead requires
ReadTimeout greater than both zero and EnquireLink. WriteTimeout follows the
[write deadline policy](../README.md#write-deadlines). The bind deadline is
cleared before bound I/O starts.

Stop closes the listener and all handshaking and bound connections and waits for
server connection cleanup, including application callbacks. StopSystemID joins
the workers and callbacks of its affected connections, including an
already-started shutdown.

OnError and ServerPDUHandler callbacks may call Stop or StopSystemID to initiate
shutdown. The calling callback must return for cleanup to finish; other
callbacks already running, discarded-submission errors, pending-request close
callbacks and final OnClosed notifications may follow that return. External
shutdown calls join all of them.

Intentional shutdown receive errors are suppressed; each accepted submission
that cannot be written is still reported with its PDU through SubmitError.

Custom connectors without `ConnectContext` must return from `Connect` before
external `Close` can finish. `Dialer.DialContext` accepts cancellation; the
built-in TCP dialer uses `net.Dialer.DialContext` and shutdown joins it. If a
custom dialer ignores cancellation, the connector abandons the attempt and
closes a late connection, but the dialing goroutine remains until it returns.
Session shutdown does not wait for that abandoned goroutine.

Optional TLS is delegated to Go's `crypto/tls` with the caller's configuration.
No independent TLS protocol implementation or TLS conformance claim is made.

## Optional parameters and TLVs

TLV framing follows v3.4 §3.2.4.1 and tags §5.3.2. Optional-parameter lookups
consistently return the first occurrence; all occurrences remain available in
wire order. `RegisterOptionalParam` replaces the first and `AppendOptionalParam`
adds another. Clearing detaches the slice.

Server delivery follows the [session selection rules](#bind-and-session-states).
Application responses omit optional parameters for older peers and report the
removal through `OnError`; caller PDUs retain their fields.

## Short messages

### UDH and concatenation

A bounded UDH with a truncated final information element is ignored as required
by TS 23.040 §9.2.3.24. `ShortMessage.UDH()` exposes no parsed elements;
`IgnoredUDH()` exposes the preserved raw header. Its declared header octets are
skipped before returning payload and retained on marshal.

A declared header that exceeds the short_message field still produces a
message-length error.

UDH concatenation uses the last concatenation IE (TS 23.040 §9.2.3.24).
Receivers must ignore concatenation information when total or sequence is zero,
or sequence exceeds total (§9.2.3.24.1; 16-bit references in §9.2.3.24.8). The
receive examples enforce these bounds before indexing reassembly buffers.

### Splitting budgets

GSM-alphabet custom encodings use escape-safe 160/153 septet budgets. UCS2 uses
encoded UTF-16 octets.

A caller-supplied custom codec without a splitter constructs at most 254 octets
including UDH (SMPP v3.4 §§5.2.21–22); longer text returns an error. Parsed
255-octet fields re-marshal byte-identically.

Splitting replaces UDH concatenation metadata and drops stale SAR TLVs.
`SubmitSM.Split` assigns fresh sequence numbers to generated parts and preserves
unrelated TLVs. Each UDH reduces the payload budget; uncompressed octet codings
use 140 octets minus header size (TS 23.040 §9.2.3.24).

### Packed GSM padding

SMPP supplies an octet length without a GSM septet count. Packed encoding uses
CR in a spare full septet, borrowing TS 23.038 §6.1.2.3.1's USSD convention; SMS
packing's normative fill rule is zero padding (§6.1.2.1.1). Decoding supports
both conventions and calculates septet capacity after UDH fill bits.

The USSD CR-padding rule also doubles a terminal CR on an octet boundary (TS
23.038 §6.1.2.3.1).

The inferred decoder follows the padding convention; `data.DecodeGSM7Packed`
accepts an explicit count when available. Raw packed messages are resegmented
only when decode and re-encode preserve their bytes, with ambiguous tails
additionally requiring retained original source text. Packed splitting moves
boundaries to avoid ambiguous tails in generated parts.

UDH fill bits have no receiver meaning (TS 23.040 §9.2.3.24) and are ignored. An
escaped final septet is retained rather than interpreted as padding.

The unavoidable text collisions are listed under [Known
limitations](#known-limitations).

## Codings

### GSM 7-bit

The default alphabet and extension table use TS 23.038 §§6.2.1–6.2.1.1 and the
versions in the baseline above. National-language single/locking shift tables
are specified in §6.2.1.2 and Annex A.

### ISO-8859

Latin-1, Cyrillic and Hebrew use the Unicode Consortium's official ISO-8859
tables listed under [Authorities](#authorities). Latin-1 preserves C1 controls;
a dedicated test signals upstream behavior changes. Undefined Hebrew octets use
x/text's replacement-character decoding. The Cyrillic/Hebrew C1 limitation and
related issue are recorded under [Known limitations](#known-limitations).

### UCS2 and ASCII

UCS2 uses encoded UTF-16 octets for splitting, preserving surrogate pairs.
`data.ASCII` rejects unrepresentable text.

### data_coding classification

Compressed data-coding groups with a defined alphabet require TS 23.042 support
and are retained as raw binary rather than encoded as uncompressed text.
Reserved alphabet selections use the GSM default fallback from TS 23.038 §4,
including when the compression bit is set. SMPP data_coding 05/09/0A/0D/0E are
distinct opaque text alphabets (§5.2.19), with 140-octet budgets; without a
character-aware codec they remain unsplit if they fit and otherwise return an
error.

Coding groups use TS 23.038 §4, with the V19/V20 relationship recorded in the
baseline. `SMSCDefaultCoding` (`0x00`) names the SMSC default; `GSM7BITCoding`
is its compatibility alias.

## Hardening inputs

Public Go SMPP libraries and their issue trackers repeatedly expose the same
failure classes: duplicated multipart sequence numbers, incorrect GSM 7-bit
escape accounting, UDH parsing mistakes, non-idempotent close paths, request
window races, dropped non-OK responses, optional-parameter gaps, and malformed
PDU parser crashes. Fixes in this repository should keep those classes covered
by focused unit, race, and fuzz tests before changing protocol behavior.

## Known limitations

### Protocol versions and future v5 support

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

### Validation and interoperability

The [Policies](#policies) table distinguishes framing checks from strict field
maxima. `Validate` does not check time syntax or the exact 1-or-17 size rule.
Application handlers remain responsible for processing and validating request
fields. Pre-v3.4 message IDs and optional parameters require the version rules
in [Bind and session states](#bind-and-session-states) and
[TLVs](#optional-parameters-and-tlvs).

### Request ownership and generic_nack

Sequence numbers belong to the sending peer. A generic_nack rejecting our
response can therefore have the same sequence as an unrelated outstanding
request. Its header does not identify the rejected command, so this case cannot
be distinguished from a nack for that outstanding request.

Shared-store capacity is advisory across concurrent writers and unavailable
backend metadata. Separate process namespaces remain recommended because
remote-process death cannot be inferred; the ownership rules above preserve
unknown-process entries. Non-comparable decorators preserve own-lineage recovery
but cannot establish cross-lineage backend identity.

Rejected-submission tombstones retain only the most recent 1024 identities.
Older late commits and rows committed after final cleanup may require
administrative removal; they cannot be inferred from an expired local record.

### Packed GSM ambiguity

With no septet count, `AAAAAAA` with zero padding and `AAAAAAA@` have identical
bytes; `AAAAAAA\r` with doubled padding and literal `AAAAAAA\r\r` also have
identical bytes. Single-part messages cannot resolve these collisions without
changing their text or adding an external count.

When a septet count is available, use `data.DecodeGSM7Packed`; otherwise follow
the inferred padding convention described above.

### ISO-8859 C1 controls

x/text decodes Cyrillic and Hebrew C1 octets `0x80`–`0x9F` as U+FFFD and refuses
to encode U+0080–U+009F, despite the Unicode tables. No local workaround is
applied. [golang/go#23706](https://github.com/golang/go/issues/23706) requests
the inverse change, replacing Latin-1 C1 controls; it does not track
preservation of Cyrillic/Hebrew controls.

### Unsupported codings

National-language shift-table selection and TS 23.042 compression are not
implemented. Opaque SMPP alphabets need a character-aware codec to split; custom
codecs without a splitter use the construction limit in [Splitting
budgets](#splitting-budgets).
