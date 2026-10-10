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
written. Never call `Stop()` synchronously from `OnError` or `OnPDU`: it can
wait for the calling goroutine. Request shutdown in a separate goroutine.
Callbacks must return; shutdown may return before an already-running handler
finishes because I/O closure is asynchronous. `StopSystemID` removes an account
and closes its connections, while `Stop()` closes all connections and waits for
cleanup. A stopped server cannot
restart.

`GetSystemIDStatus` includes the bind type and typed `SessionActive` or
`SessionClosed` status. `SystemIDs` lists accounts with bound sessions.
Submission success means queued, not delivered; track `deliver_sm_resp` to learn
the ESME's result. The context bounds waiting for queue capacity, not a later
socket write. PDUs with optional parameters require a live v3.4-or-later RX/TRX
session; otherwise `SubmitToSystemID` returns
`*gosmpp.UnsupportedOptionalParametersError` without sending. Handler responses
to older peers have optional parameters stripped, with a diagnostic in `OnError`.
Write failures also reach `OnError` with account and remote address context.
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
