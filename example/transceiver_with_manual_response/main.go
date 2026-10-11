// This example sends UCS2 text and receives deliveries with manual responses.
// Run example/smsc_server first, or change Auth to match your SMSC.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"time"

	gosmpp "github.com/gomaja/go-smpp"
	"github.com/gomaja/go-smpp/data"
	"github.com/gomaja/go-smpp/example/internal/multipart"
	"github.com/gomaja/go-smpp/pdu"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	session, err := gosmpp.NewSession(
		gosmpp.TRXConnector(gosmpp.NonTLSDialer, gosmpp.Auth{
			SMSC: "127.0.0.1:2775", SystemID: "esme", Password: "secret",
		}),
		gosmpp.Settings{
			EnquireLink: time.Minute, ReadTimeout: 2 * time.Minute,
			WriteTimeout:     5 * time.Second,
			OnSubmitError:    func(_ pdu.PDU, err error) { log.Print(err) },
			OnReceivingError: func(err error) { log.Print(err) },
			OnRebindingError: func(err error) { log.Print(err) },
			OnAllPDU:         handlePDU(),
			OnClosed:         func(state gosmpp.State) { log.Print(state.String()) },
		}, 5*time.Second)
	if err != nil {
		log.Print(err)
		return
	}
	defer func() { _ = session.Close() }()
	submit := pdu.NewSubmitSM().(*pdu.SubmitSM)
	submit.SourceAddr, err = pdu.NewAddressWithTonNpiAddr(5, 0, "Example")
	if err != nil {
		log.Print(err)
		return
	}
	submit.DestAddr, err = pdu.NewAddressWithTonNpiAddr(1, 1, "12025550123")
	if err != nil {
		log.Print(err)
		return
	}
	if err = submit.Message.SetMessageWithEncoding("Hello, 世界", data.UCS2); err != nil {
		log.Print(err)
		return
	}
	if err = session.Transceiver().Submit(submit); err != nil {
		log.Print(err)
	}
	<-ctx.Done()
}

func handlePDU() func(pdu.PDU) (pdu.PDU, bool) {
	var receiver multipart.Receiver
	return func(p pdu.PDU) (pdu.PDU, bool) {
		if delivery, ok := p.(*pdu.DeliverSM); ok {
			text, ready, err := receiver.Add(delivery)
			if err != nil {
				log.Print(err)
			} else if ready {
				log.Print(text)
			}
		}
		log.Printf("Received %T", p)
		// OnAllPDU owns replies, including enquire_link and unbind.
		if p.CanResponse() {
			_, unbind := p.(*pdu.Unbind)
			return p.GetResponse(), unbind
		}
		return nil, false
	}
}
