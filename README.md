# go-smpp

[![ci](https://github.com/gomaja/go-smpp/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/gomaja/go-smpp/actions/workflows/ci.yml)
[![security](https://github.com/gomaja/go-smpp/actions/workflows/security.yml/badge.svg?branch=main)](https://github.com/gomaja/go-smpp/actions/workflows/security.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/gomaja/go-smpp)](https://goreportcard.com/report/github.com/gomaja/go-smpp)
[![pkg.go.dev](https://pkg.go.dev/badge/github.com/gomaja/go-smpp.svg)](https://pkg.go.dev/github.com/gomaja/go-smpp)

SMPP (3.4) client and SMSC server library in pure Go.

## Installation
```
go get github.com/gomaja/go-smpp@main
```

## Versioning

This module publishes no releases. `main` is the only supported line, and its
API changes whenever a better design is found, without a deprecation period.
Go records the commit you build against as a pseudo-version in your `go.mod`;
upgrade by running the command above again.

The former tags `v1.0.0` and `v1.0.1` are retracted, so `go get` warns about
them and `@latest` resolves to the tip of `main`.

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
