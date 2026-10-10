# Specification compliance

This module currently implements SMPP v3.4 PDU and session behavior. The
standards baseline below was checked on 2026-08-02 from the publishing
authorities. The field codec and text-coding sections were rechecked on
2026-10-10 against SMPP v3.4 Issue 1.2, v5, and ETSI TS 23.038/23.040 V19.0.0.

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
  cleanup. Existing asynchronous I/O closure can allow an already-running
  handler to outlive Stop/StopSystemID; lifecycle hardening remains separate.
- `SubmitToSystemID(ctx, systemID, p)` honours cancellation while waiting for
  queue capacity. Success means queued, not delivered; the application must
  track deliver_sm_resp (§4.6.2). A receipt submitted inside OnPDU can precede
  the submit_sm_resp for the message being handled, because the outbound daemon
  runs concurrently with the handler. Applications must coordinate ordering.
- OnError and ServerPDUHandler callbacks must not call Stop synchronously;
  request shutdown from a separate goroutine to avoid waiting on themselves.
  Intentional Stop/StopSystemID/unbind transport errors are not reported.

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
The handler's closeBind decision still applies: when true, the server closes
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
