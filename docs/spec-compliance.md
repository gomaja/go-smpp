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
