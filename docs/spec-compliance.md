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

The coding authorities were rechecked on **2026-10-15**. The current 3GPP
portals still list TS 23.038 V20.0.0 and TS 23.040 V20.0.0, and the sources
linked above were unchanged. The [23.038 correction
register](https://portal.3gpp.org/ChangeRequests.aspx?q=1&specnumber=23.038) and
[23.040 correction
register](https://portal.3gpp.org/ChangeRequests.aspx?q=1&specnumber=23.040) showed
no newer approved replacement affecting this coding work. Approved 23.038
CR 0243 (CBS language indication, §5) and 23.040 CR 0169 revision 3
(emergency-SMS routing) are incorporated in V20. Unapproved proposals are not
applied as corrections. The SMPP v3.4 Issue 1.2 source was unchanged.

National-language behavior was also rechecked against the current V20 sources
on **2026-10-15**: TS 23.038 §§6.2.1.2, 6.2.1.2.4–5 and Annex A, and TS 23.040
§§9.2.3.24.15–16. No newer approved correction affecting these clauses was
found. Table 6.2.1.2.4.1 assigns identifiers 1–13; zero and 14–255 are reserved.
Annex A.2 has thirteen single-shift tables; Annex A.3 has twelve locking-shift
tables because Spanish A.3.2 is void.

UTF-8 validation follows RFC 3629 §3; UTF-16 conversion follows RFC 2781
§§2.1–2.2 and the fixed UTF-16BE form in §3.3. The RFC Editor records for
[RFC 3629](https://www.rfc-editor.org/rfc/rfc3629.json) and
[RFC 2781](https://www.rfc-editor.org/rfc/rfc2781.json) agree with their
Datatracker records ([3629](https://datatracker.ietf.org/doc/rfc3629/),
[2781](https://datatracker.ietf.org/doc/rfc2781/)): RFC 3629 is an Internet
Standard; RFC 2781 is Informational. Neither has an incoming update or
obsoletion, including an updating or obsoleting draft in the filtered
Datatracker relationships. The errata searches for
[3629](https://errata.rfc-editor.org/search/?rfc_number=3629&presentation=records)
and [2781](https://errata.rfc-editor.org/search/?rfc_number=2781&presentation=records)
returned no matching records. These encoding RFCs have no current sponsoring
working group; the active mailmaint documents concern email-address syntax,
not replacement UTF transformations.

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
  8-bit concatenation IE in section 9.2.3.24.1, the 16-bit concatenation IE in
  section 9.2.3.24.8, and the national-language single/locking IEs in sections
  9.2.3.24.15–16.

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

Complete-header parsing and marshalling delegate to go-sms's
`tpdu.UserDataHeader` under TS 23.040 V20.0.0 §9.2.3.24. The public `pdu.UDH`
and `pdu.InfoElement` types, constructors and first-occurrence pointer lookup
remain in go-smpp. Its `UDHL()` convention includes the length octet: zero
without IEs, -1 for a body above 255 octets, otherwise body length plus one.
Nil and empty headers are omitted on send; over-long headers retain the
existing error.

The [publication archive](https://www.3gpp.org/ftp/Specs/archive/23_series/23.040/)
and [specification record](https://portal.3gpp.org/desktopmodules/Specifications/SpecificationDetails.aspx?specificationId=747)
were rechecked on **2026-10-15** for this delegation. Both still identify
V20.0.0 as current. The [correction register](https://portal.3gpp.org/ChangeRequests.aspx?specnumber=23.040&q=1)
showed no newer approved correction to §9.2.3.24; proposals without approval
were not applied.

The strict `UDH.UnmarshalBinary` facade succeeds only for a nonempty,
well-formed header. Empty (`00`) and ignored headers return an error, consume
zero bytes and leave the receiver unchanged. Malformed-final-IE errors now
use a generic message because the dependency reports the whole header as
ignored. Parsed IE data is go-sms's copy, independent of the source; a
zero-length IE may have nil data. Standalone `InfoElement.UnmarshalBinary`
still borrows its input and stays local pending
[go-sms issue 26](https://github.com/gomaja/go-sms/issues/26), since a standalone
IE can exceed the representable complete-header size.

SMPP coding, length and UDHI framing stay local (v3.4 §§5.2.19, 5.2.21–22).
`ShortMessage` attempts header parsing only with UDHI set and a nonempty
message field, checks the declared span first, then commits the result only
after framing succeeds. Unlike the strict facade, it preserves `00` as an
ignored raw header for compatibility.

A bounded UDH with a truncated final information element is ignored as required
by TS 23.040 §9.2.3.24. `ShortMessage.UDH()` exposes no parsed elements;
`IgnoredUDH()` exposes the preserved raw header. Its declared header octets are
skipped before returning payload and retained on marshal.

A declared header that exceeds the short_message field still produces a
message-length error. Raw ignored bytes take precedence over parsed IEs for
both header length and marshalling, preserving the received wire bytes.

`UDH.GetConcatInfo` delegates interpretation to go-sms's
`tpdu.UserDataHeader.ConcatInfo`. It ignores concatenation IEs with the wrong
length, zero total or sequence, or sequence greater than total (TS 23.040
§§9.2.3.24.1, 9.2.3.24.8), then selects the last applicable valid 8-bit or
16-bit IE (§9.2.3.24). An invalid final IE no longer hides an earlier valid
one. An 8-bit reference is returned as that octet in `mref`; a 16-bit reference
uses the full `uint16`. With no valid IE, all values are zero and `found=false`.
The receive examples use the same result, including its reference width, for
reassembly. Splitting still strips all caller concatenation IEs before
creating new metadata; it does not interpret them as received information.

### Splitting budgets

Header-capacity arithmetic stays local pending
[go-sms issue 24](https://github.com/gomaja/go-sms/issues/24); chunking stays
local pending [issue 25](https://github.com/gomaja/go-sms/issues/25). These
field-level APIs avoid introducing TPDU envelope and segmentation policy into
SMPP. Segmentation behavior is unchanged by complete-header delegation.

GSM-alphabet encodings use escape-safe septet budgets. An unpacked GSM7 part
holds `160 - ceil(headerOctets * 8 / 7)` septets; extension characters consume
two. The complete header includes caller IEs, NLI IEs and concatenation
metadata (TS 23.038 §6.2.1.2; TS 23.040 §9.2.3.24). The familiar 160/153 limits
apply to no header or an 8-bit concatenation IE alone; a 16-bit reference and
additional IEs reduce the available payload further. Packed GSM7 uses the
actual header fill alignment and accounts for the chosen CR-padding convention.

`data.UCS2.ShouldSplit` compares the selected codec's encoded length and
returns false on an encoding error, including malformed UTF-8.
`data.UCS2` uses encoded UTF-16 octets, preserving surrogate pairs;
`data.UCS2Strict` uses two octets per BMP scalar. The wire coding remains
`0x08` for both.

A caller-supplied custom codec without a splitter constructs at most 254 octets
including UDH (SMPP v3.4 §§5.2.21–22); longer text returns an error. Parsed
255-octet fields re-marshal byte-identically.

Splitting replaces UDH concatenation references, counts and part numbers while
retaining the reference width selected by the last caller-supplied
concatenation IE. Thus a 16-bit IE staged through `SetUDH` remains 16-bit in
generated parts (TS 23.040 §§9.2.3.24.1, 9.2.3.24.8). It retains other caller IEs,
including the NLI IEs on every part, and drops stale SAR TLVs. `SubmitSM.Split`
assigns fresh sequence numbers to generated parts and preserves unrelated TLVs.
Uncompressed octet codings use 140 octets minus header size; construction also
checks SMPP's octet-length field (v3.4 §§5.2.21–22).

Received GSM7 messages with NLI IEs are split over their original septets,
preserving those IEs rather than decoding text and choosing tables again. The
raw packed ambiguity guard still applies: without retained source text,
ambiguous tails cannot be safely resegmented.

### Packed GSM padding

SMPP supplies an octet length without a GSM septet count. Built-in GSM alphabet
conversion and all bit packing delegate to go-sms, including national-language
messages with UDH alignment. `gsm7.Pack7BitCR` uses CR in seven spare bits and
adds a second CR when a wanted terminal CR ends on an octet boundary, borrowing
TS 23.038 §6.1.2.3.1's USSD convention. SMS packing's normative fill rule is
zero padding (§6.1.2.1.1).

Inferred receiving uses `gsm7.Unpack7BitCR`: after UDH fill alignment, a final
CR is removed only when the available bits end with a complete septet (TS
23.038 §6.1.2.3.1). A genuine final zero septet (`@` in the default alphabet) is retained; doubled CRs
are retained doubled. Zero-filled seven-spare-bit payloads therefore produce
an extra `@` in the default alphabet. The rule applies before alphabet conversion, including after
ESC. These differ from the former local inference.

`data.DecodeGSM7Packed` accepts an explicit septet count for default-alphabet
text: it extracts with `gsm7.Unpack7Bit`, rejects counts beyond the available
payload, then decodes exactly that many septets. Count `-1` selects inferred
CR decoding. Raw septets are available directly through these go-sms APIs.
Legacy packed PDU setters share the public decoder's replaceable `GSM7BIT`
alphabet, including through custom wrappers. Alphabet resolution always
removes packing: a configured replacement keeps its locking and single-shift
tables with `Packed: false`; a legacy packed replacement resolves to the
built-in unpacked alphabet without consulting the variable again. Custom
unpacked alphabets retain their conversion and need not implement `Splitter`
for the legacy packed PDU path. PDU construction, decoding and splitting apply
packing or unpacking exactly once. Legacy packed construction includes the
replacement's configured NLI IEs on every segment (TS 23.040
§§9.2.3.24.15–16). Explicitly configured default-table packed codecs retain
their default alphabet during PDU decoding. Standalone configured codec
`Decode` uses its selected tables and has no UDH to interpret. PDU receive
decoding instead selects tables from received NLI IEs; absent IEs select the
corresponding default tables, including when the receive codec was configured
with national-language tables. Received NLI IEs take precedence over codec
table options.

`FromDataCoding(0)` resolves the current `GSM7BIT`, rather than a saved initial
codec. Assigning `GSM7BIT = GSM7BITPACKED` therefore makes data_coding 0 packed;
a configured packed replacement does likewise. A custom replacement supplies
its selected conversion. The returned codec preserves wire coding 0 even if
the replacement advertises a different coding. Configure the variable before
concurrent use: access is not synchronized. `DefaultGSM7` is the effective
default lookup: a nil `GSM7BIT` selects the built-in unpacked default codec.
Coding lookup, encoding selection, text constructors and packed alphabet
resolution all use that policy. Nil text encoding arguments resolve the
effective default before selecting NLI IEs. Raw setters retain nil in
`Encoding()` and marshal data_coding 0 regardless of the replacement's coding;
codec operations and segmentation budgets resolve the effective default
locally without changing that stored selection, including on split parts.
These corrections are listed in
the [behavior-change table](../README.md#pdus-and-encoding).

Standalone packed codecs remain independent of that variable, and `NewGSM7`
sending keeps its explicit tables.
Getters retain known original text only while its encoding matches the stored
bytes and the current UDH contains every configured selector with its selected
value (TS 23.040 §9.2.3.24.15–16).

Received NLI messages preserve original septets when splitting; other raw
packed messages must decode and re-encode byte-identically. Ambiguous CR tails
still require retained original source text. Packed splitting uses the same
go-sms pack/unpack policy to choose boundaries that preserve septets, checks
the actual octet budget, and keeps escape pairs intact. Segmentation and UDH
framing remain in go-smpp; complete-header parsing and marshalling use go-sms
(TS 23.040 §§9.2.3.24, 9.2.3.24.1).

Multipart packed segmentation may now end a segment on a zero septet because
the CR receive rule retains it (TS 23.038 §6.1.2.3.1). Part counts and segment
bytes can therefore differ from the previous boundary policy: 300 `@` with
caller UDH `02 70 00` now produces two 150-septet parts instead of three parts
of 149/149/2 septets. Single-message encodings remain unchanged; byte identity
for a fixed text/header encoding does not imply identical multipart output.

UDH fill bits have no receiver meaning (TS 23.040 §9.2.3.24) and are ignored.

The unavoidable text collisions are listed under [Known
limitations](#known-limitations).

## Codings

### GSM 7-bit

The default alphabet and extension table use TS 23.038 §§6.2.1–6.2.1.1 and the
versions in the baseline above. National-language single/locking shift tables
are specified in §6.2.1.2 and Annex A.

`data.FindEncoding` tries go-sms's `gsm7.Encode` with its default alphabet and
default extension table (TS 23.038 §§6.2.1–6.2.1.1). It selects `GSM7BIT` only
if both that default-table check and the actual `GSM7BIT` codec accept the text;
otherwise it selects `UCS2`. This also applies when the public `GSM7BIT`
variable has been replaced: replacing it with `ASCII` makes `£` select UCS2,
so the returned codec can encode and split it. With the initial codec, `£`,
`é`, `Δ` and `€` select GSM7, while backtick, ESC, controls outside the alphabet,
CJK and emoji select UCS2.
Extension characters consume two septets. Empty text selects GSM7 when its
codec accepts it; invalid UTF-8 selects UCS2 but remains invalid input to its encoder. No national
language tables are considered: an `Encoding` alone cannot supply the required
NLI IEs. This corrects the former ASCII test; 80 `£` characters now occupy one
segment instead of two. See the [behavior-change table](../README.md#pdus-and-encoding).

Default-alphabet conversion uses `github.com/gomaja/go-sms/encoding/gsm7`.
Unpacked receive follows its permissive policy: an unassigned extension uses
the main table, ESC followed by ESC or end-of-input displays a space, and bytes
above `0x7f` become spaces. Encoding returns `gsm7.ErrUnencodable` for a
character outside the alphabet and `gsm7.ErrInvalidUTF8` for malformed UTF-8,
with byte offsets. Errors retain their types for `errors.As`.

The public `data.GSM7` x/text factory, its streaming transformers,
`ValidateGSM7String`, `ValidateGSM7Buffer`, `GetEscapeChars`, `IsEscapeChar`,
`ErrInvalidCharacter`, `ErrInvalidByte` and the packed concrete type's
`GetSeptetCount` method are removed. Use the complete-message `Encoding` codecs
or go-sms's unpacked byte APIs; measure septet costs from the encoded output.
`gsm7.Strict` is available for diagnostics but also rejects unassigned-extension
fallback, so it is not a replacement for the previous buffer validator. Invalid
`DecodeGSM7Packed` fill/count arguments return ordinary descriptive errors.
See the [migration table](../README.md#pdus-and-encoding).

### National-language GSM7

`data.NewGSM7` takes `GSM7Options` with `Packed`, `LockingShift` and
`SingleShift` fields to select locking and single-shift tables explicitly.
The integer selectors correspond to go-sms `charset` identifiers; locking
replaces the main table and single shift replaces the extension table independently (TS 23.038 §§6.2.1.2.2–4,
Annex A). There is no best-language search or automatic fallback to another
encoding. Alphabet conversion uses go-sms `gsm7.WithCharset` and
`gsm7.WithExtCharset`; its typed encoder errors and permissive decoder are
preserved. `ShouldSplit` is false on encoding failure, and empty encoding and
split results are nonnil, as for the existing codecs.

API selector zero means the default table and omission of that NLI IE. Send
construction rejects unknown/reserved selectors and Spanish locking shift,
whose table is void (Annex A.3.2). The supported national identifiers provide
thirteen single-shift and twelve locking-shift tables. The configured codec
keeps SMPP `data_coding=0x00`; table selection is carried in the UDH, not a new
SMPP coding value (SMPP v3.4 §5.2.19).

`ShortMessage.SetMessageWithEncoding` and `SetLongMessageWithEnc` merge the
selected single-shift IE `0x24` and locking-shift IE `0x25` into the header.
Matching caller NLI IEs and unrelated IEs are retained. Conflicting selectors,
zero-valued manual NLI IEs, or a manual NLI with the wrong data length fail
atomically. `SetUDH` also preserves a configured selection when replacing
other IEs. The submit_sm, deliver_sm and submit_multi marshalers set wire UDHI
when the header contains NLI IEs (TS 23.040 §§9.2.3.24.15–16).

The manual constructors
`pdu.NewIENationalLanguageSingleShift(int) (pdu.InfoElement, error)` and
`pdu.NewIENationalLanguageLockingShift(int) (pdu.InfoElement, error)` validate
one-octet wire identifiers; unlike the codec's omission sentinel, zero is
rejected as a reserved transmitted NLI (TS 23.038 §6.2.1.2.4).

For GSM7, `ShortMessage.GetMessage` delegates each received segment's UDH table
selection and text decoding to go-sms `tpdu.DecodeUserData` with
`tpdu.WithAllCharsets`. Absence selects the corresponding default table for
that segment; no state carries across concatenated parts. Repeated IEs use the
last applicable occurrence. Reserved or unsupported identifiers are skipped,
so a later ignored IE does not erase an earlier applicable selection; if none
remains, the relevant default applies (TS 23.038 §6.2.1.2.5; TS 23.040
§§9.2.3.24.15–16). Spanish locking is a known identifier with explicit default
fallback, so a received Spanish locking IE selects the default main table.

Non-GSM codecs ignore NLI IEs during text decoding. Received IEs and their raw
data remain accessible through `UDH()` and are retained on marshal;
`IgnoredUDH()` exposes a bounded whole header ignored after a truncated final
IE. Decoding does not rewrite either representation.

SMPP has no packed flag: the peer agreement selects packed versus unpacked
representation. Use `GetMessageWithEncoding(data.GSM7BITPACKED)` for a packed
peer; the received UDH still controls table selection. Direct packed
`NewGSM7` codec splitting keeps the existing one-fill-bit convention for a
six-octet concatenation UDH. Complete NLI header layouts use PDU segmentation,
which budgets and aligns the actual header as described above.

These APIs add explicit table support. The compatibility changes are that
text setters reject conflicting manual NLI IEs, receive getters honor NLI IEs
that were previously ignored, and splitting preserves a caller-selected
16-bit concatenation width. See the [client examples](../README.md#sending-national-language-text)
and [migration table](../README.md#pdus-and-encoding).

### ISO-8859

Latin-1, Cyrillic and Hebrew use the Unicode Consortium's official ISO-8859
tables listed under [Authorities](#authorities). Latin-1 preserves C1 controls;
a dedicated test signals upstream behavior changes. Undefined Hebrew octets use
x/text's replacement-character decoding. The Cyrillic/Hebrew C1 limitation and
related issue are recorded under [Known limitations](#known-limitations).

### UCS2, UTF-16 and ASCII

`data.UCS2` retains its existing UTF-16BE repertoire through go-sms's
`ucs2.EncodeUTF16`, including supplementary characters as surrogate pairs
(RFC 2781 §2.1). `data.UCS2Strict` uses `ucs2.Encode`: only BMP Unicode scalar
values can be represented with the 16 bits per character of TS 23.038 §6.2.3.
Both advertise SMPP `data_coding=0x08` (§5.2.19); that byte cannot distinguish
strict UCS2 from the UTF-16 interoperability extension. Both reject malformed
UTF-8 before converting strings to runes (RFC 3629 §3), and preserve typed
go-sms errors. Strict encoding returns `ucs2.ErrUnencodable` for a supplementary
character.

Both decode with `ucs2.Decode`, which accepts UTF-16 surrogate pairs. An odd
byte length returns the text decoded from complete 16-bit units together with
`ucs2.ErrInvalidLength` (TS 23.038 §6.2.3). The final octet is neither decoded
nor carried into the next segment: a UCS2 character must not be split in the
middle (TS 23.040 §§9.2.3.24.1, 9.2.3.24.8). A high surrogate before that
octet becomes U+FFFD, and only `ucs2.ErrInvalidLength` is returned, never
`ucs2.ErrDanglingSurrogate`. An even-length payload ending in a high surrogate
returns the preceding decoded text and `ucs2.ErrDanglingSurrogate`, whose bytes
may be completed by a later segment. Other unpaired surrogates become U+FFFD
individually. RFC 2781 §2.2 leaves malformed-input recovery
unspecified; the previous x/text replacement behavior is not reproduced for
odd lengths or a final high surrogate. The fixed big-endian path does not
strip a BOM or switch byte order (RFC 2781 §3.3).

`data.ASCII` rejects unrepresentable text. ASCII, ISO-8859 and the explicit
UTF-16 BOM/endian helpers retain their existing implementations; x/text remains
a dependency for these non-GSM codecs.

### data_coding classification

`FromDataCoding` preserves the SMPP v3.4 §5.2.19 mapping for `0x00`–`0x0f`.
SMPP and TP-DCS use different namespaces: SMPP `0x03` is Latin-1, for example,
while TP-DCS `0x03` selects GSM7. SMPP values `0x05`, `0x09`, `0x0a`, `0x0d`
and `0x0e` remain distinct opaque alphabets with 140-octet budgets; without a
character-aware codec they remain unsplit if they fit and otherwise return an
error. The reserved direct values `0x0b`, `0x0c` and `0x0f` retain the existing
GSM fallback.

For every other byte, classification delegates to go-sms's `tpdu.DCS.Alphabet`
and `Compressed` under TS 23.038 §4. This lenient TP-DCS extension is retained
for interoperability; it is not the SMPP table's assignment of values that it
marks reserved, such as `0x10`–`0xbf` and `0xe0`–`0xef`. The original wire byte
is always returned by `DataCoding()`.

Defined compressed alphabets require TS 23.042 support and remain raw binary
rather than being encoded as uncompressed text. Reserved TP-DCS codings use
the GSM default fallback, including reserved alphabet selections with the
compression bit set. Reserved bits within a defined group do not change its
alphabet: `0xe4`–`0xe7` and `0xec`–`0xef` now select UCS2, and `0xfc`–`0xff`
select binary, where the previous classifier selected GSM7. The wire bytes
are unchanged; only the selected codec changes. There is no separate profile
API for this extension.

Coding groups use the V19/V20 relationship recorded in the baseline.
`SMSCDefaultCoding` (`0x00`) names the SMSC default; `GSM7BITCoding` is its
compatibility alias. The local default remains GSM7; callers need a custom
codec for peers whose SMSC default is another alphabet.

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

TS 23.042 compression is not implemented. National-language GSM7 selection is
supported as described above. Opaque SMPP alphabets need a character-aware codec
to split; custom codecs without a splitter use the construction limit in [Splitting
budgets](#splitting-budgets).
