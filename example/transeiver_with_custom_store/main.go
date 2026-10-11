// This example correlates requests using a serialized, conditional RequestStore.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"time"

	gosmpp "github.com/gomaja/go-smpp"
	"github.com/gomaja/go-smpp/data"
	"github.com/gomaja/go-smpp/pdu"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	auth := gosmpp.Auth{
		SMSC:       "127.0.0.1:2775",
		SystemID:   "esme",
		Password:   "secret",
		SystemType: "",
	}

	store, err := NewCustomStore()
	if err != nil {
		log.Print(err)
		return
	}
	trans, err := gosmpp.NewSession(
		gosmpp.TRXConnector(gosmpp.NonTLSDialer, auth),
		gosmpp.Settings{
			EnquireLink: time.Minute,

			ReadTimeout:  2 * time.Minute,
			WriteTimeout: 5 * time.Second,

			OnSubmitError: func(_ pdu.PDU, err error) { log.Print("SubmitPDU error: ", err) },

			OnReceivingError: func(err error) {
				log.Print("Receiving PDU/Network error:", err)
			},

			OnRebindingError: func(err error) {
				log.Print("Rebinding but error:", err)
			},

			OnClosed: func(state gosmpp.State) {
				log.Print(state.String())
			},

			WindowedRequestTracking: &gosmpp.WindowedRequestTracking{
				OnReceivedPduRequest:  handleReceivedPduRequest(),
				OnExpectedPduResponse: handleExpectedPduResponse(),
				OnExpiredPduRequest:   handleExpirePduRequest(),
				OnClosePduRequest:     handleOnClosePduRequest(),
				PduExpireTimeOut:      30 * time.Second,
				ExpireCheckTimer:      10 * time.Second,
				MaxWindowSize:         30,
				StoreAccessTimeOut:    time.Second,
				EnableAutoRespond:     false,
			},
		},
		5*time.Second,
		gosmpp.WithRequestStore(&store),
	)
	if err != nil {
		log.Print(err)
		return
	}
	defer func() {
		_ = trans.Close()
	}()

	submit, err := newSubmitSM()
	if err != nil {
		log.Print(err)
		return
	}
	if err = trans.Transceiver().Submit(submit); err != nil {
		log.Print(err)
	}
	<-ctx.Done()
}

func handleExpirePduRequest() func(pdu.PDU) bool {
	return func(p pdu.PDU) bool { log.Printf("Expired %T", p); return false }
}

func handleOnClosePduRequest() func(pdu.PDU) {
	return func(p pdu.PDU) { log.Printf("Closed with pending %T", p) }
}

func handleExpectedPduResponse() func(gosmpp.Response) {
	return func(response gosmpp.Response) {
		if response.Err != nil {
			log.Print(response.Err)
			return
		}
		log.Printf("Response %T, status %v, original %T", response.PDU,
			response.GetHeader().CommandStatus, response.OriginalRequest.PDU)
	}
}

func handleReceivedPduRequest() func(pdu.PDU) (pdu.PDU, bool) {
	return func(p pdu.PDU) (pdu.PDU, bool) {
		if p.CanResponse() {
			_, unbind := p.(*pdu.Unbind)
			return p.GetResponse(), unbind
		}
		return nil, false
	}
}

func newSubmitSM() (*pdu.SubmitSM, error) {
	submit := pdu.NewSubmitSM().(*pdu.SubmitSM)
	var err error
	submit.SourceAddr, err = pdu.NewAddressWithTonNpiAddr(5, 0, "Example")
	if err != nil {
		return nil, err
	}
	submit.DestAddr, err = pdu.NewAddressWithTonNpiAddr(1, 1, "12025550123")
	if err != nil {
		return nil, err
	}
	if err = submit.Message.SetMessageWithEncoding("Hello, 世界", data.UCS2); err != nil {
		return nil, err
	}
	return submit, nil
}
