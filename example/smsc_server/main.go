// This example accepts an ESME account, handles submissions, sends deliveries,
// and joins connection cleanup on an interrupt. It does not persist messages.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"time"

	gosmpp "github.com/gomaja/go-smpp"
	"github.com/gomaja/go-smpp/pdu"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	server := gosmpp.NewServer(gosmpp.ServerSettings{
		Address: "127.0.0.1:2775", SystemID: "smsc",
		OnError: func(err error) { log.Print(err) },
	})
	defer server.Stop()
	if err := server.StartSystemID(gosmpp.SystemIDConfig{
		SystemID: "esme", Password: "secret", MaxSessions: 4,
		ReadTimeout: 2 * time.Minute, WriteTimeout: 5 * time.Second,
		EnquireLink: time.Minute,
		OnPDU: func(_ gosmpp.ServerSessionInfo, request pdu.PDU) (pdu.PDU, bool) {
			log.Printf("Received %T", request)
			if submit, ok := request.(*pdu.SubmitSM); ok {
				response := submit.GetResponse().(*pdu.SubmitSMResp)
				response.MessageID = "message-1"
				return response, false
			}
			if request.CanResponse() {
				return request.GetResponse(), false
			}
			return nil, false
		},
	}); err != nil {
		log.Print(err)
		return
	}
	if err := server.Start(); err != nil {
		log.Print(err)
		return
	}
	log.Printf("Listening on %s", server.Addr())
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			delivery := pdu.NewDeliverSM().(*pdu.DeliverSM)
			if err := delivery.Message.SetMessageWithEncoding("Hello from SMSC", nil); err != nil {
				log.Print(err)
				continue
			}
			// Success means queued; deliver_sm_resp carries the ESME's result.
			deadline, stop := context.WithTimeout(ctx, time.Second)
			err := server.SubmitToSystemID(deadline, "esme", delivery)
			stop()
			if err != nil {
				log.Print(err)
			}
		}
	}
}
