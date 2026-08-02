# go-smpp

[![ci](https://github.com/gomaja/go-smpp/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/gomaja/go-smpp/actions/workflows/ci.yml)
[![security](https://github.com/gomaja/go-smpp/actions/workflows/security.yml/badge.svg?branch=main)](https://github.com/gomaja/go-smpp/actions/workflows/security.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/gomaja/go-smpp)](https://goreportcard.com/report/github.com/gomaja/go-smpp)
[![pkg.go.dev](https://pkg.go.dev/badge/github.com/gomaja/go-smpp.svg)](https://pkg.go.dev/github.com/gomaja/go-smpp)

SMPP (3.4) Client Library in pure Go.

This library is well tested with SMSC simulators:
- [Melroselabs SMSC](https://melroselabs.com/services/smsc-simulator/#smsc-simulator-try)

## Installation
```
go get -u github.com/gomaja/go-smpp
```

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

### Version (0.1.4.RC+)

- Full examples are available under [example](https://github.com/gomaja/go-smpp/blob/main/example)
  - In this example, you should run smsc first:
    - Build and run SMSC Simulator:
	```bash
	g++ -std=c++11 example/smsc_simulator/smsc.cpp -o smsc
	./smsc &
	```
    - Run an SMPP client example:
    ```bash
	go run example/main.go
	```

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
