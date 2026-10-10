# Specification compliance

This module currently implements SMPP v3.4 PDU and session behavior. The
standards baseline below was checked on 2026-08-02 from the publishing
authorities.

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
