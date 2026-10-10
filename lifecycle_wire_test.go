package gosmpp

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/gomaja/go-smpp/data"
	"github.com/gomaja/go-smpp/pdu"
)

func TestSessionLifecycleRequestResponsesTCP(t *testing.T) {
	for _, mode := range []string{"window_fallback", "encode_fallback", "alert_without_response"} {
		t.Run(mode, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = listener.Close() }()
			client, err := net.Dial("tcp", listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			peer, err := listener.Accept()
			if err != nil {
				_ = client.Close()
				t.Fatal(err)
			}
			defer func() { _ = peer.Close() }()
			errs := make(chan error, 4)
			settings := lifecycleSettings()
			settings.OnReceivingError = func(err error) { errs <- err }
			settings.OnSubmitError = func(_ pdu.PDU, err error) { errs <- err }
			if mode == "encode_fallback" {
				settings.OnAllPDU = func(request pdu.PDU) (pdu.PDU, bool) {
					response := request.GetResponse()
					if deliver, ok := response.(*pdu.DeliverSMResp); ok {
						deliver.MessageID = "invalid\x00id"
					}
					return response, false
				}
			}
			session, err := NewSession(lifecycleConnector{connect: func() (*Connection, error) { return NewConnection(client), nil }}, settings, 0)
			if err != nil {
				_ = client.Close()
				t.Fatal(err)
			}
			defer func() { _ = session.Close() }()
			if mode == "alert_without_response" {
				request := pdu.NewAlertNotification()
				request.SetSequenceNumber(73)
				buffer := pdu.NewBuffer(nil)
				request.Marshal(buffer)
				frame := append(buffer.Bytes(), 0x04, 0x24, 0, 2, 1)
				binary.BigEndian.PutUint32(frame[:4], uint32(len(frame)))
				if _, err := peer.Write(frame); err != nil {
					t.Fatal(err)
				}
			} else {
				request := pdu.NewDeliverSM()
				request.SetSequenceNumber(73)
				writeWire(t, peer, request)
				if err := peer.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
					t.Fatal(err)
				}
				response, err := pdu.Parse(peer)
				if err != nil {
					t.Fatalf("delivery has no response: %v", err)
				}
				status := data.ESME_ROK
				if mode == "encode_fallback" {
					status = data.ESME_RSYSERR
					if response.GetHeader().CommandLength != 16 {
						t.Error("error fallback includes a body")
					}
				}
				if response.GetHeader().CommandID != data.DELIVER_SM_RESP || response.GetSequenceNumber() != 73 || response.GetHeader().CommandStatus != status {
					t.Fatalf("delivery response header: %+v", response.GetHeader())
				}
			}
			request := pdu.NewEnquireLink()
			request.SetSequenceNumber(91)
			writeWire(t, peer, request)
			if err := peer.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			response, err := pdu.Parse(peer)
			if err != nil || response.GetHeader().CommandID != data.ENQUIRE_LINK_RESP || response.GetSequenceNumber() != 91 {
				t.Fatalf("follow-up enquire_link: %v, %v", response, err)
			}
			// The response can reach the peer before its writer releases the lock.
			// Make the explicit shutdown attempt start with an idle transport.
			conn := session.bound().conn
			conn.writeMu.Lock()
			closing := conn.closing.Load()
			conn.writeMu.Unlock()
			if closing {
				t.Fatal("session closed before explicit shutdown")
			}
			if err := session.Close(); err != nil {
				t.Fatal(err)
			}
			response, err = pdu.Parse(peer)
			if err != nil || response.GetHeader().CommandID != data.UNBIND {
				t.Fatalf("explicit close: %v, %v; want unbind", response, err)
			}
			if _, err := pdu.Parse(peer); !errors.Is(err, io.EOF) {
				t.Fatalf("transport after unbind: %v", err)
			}
			if mode != "window_fallback" {
				select {
				case err := <-errs:
					if mode == "encode_fallback" && !errors.Is(err, pdu.ErrEncode) {
						t.Fatalf("response error: %v", err)
					}
				case <-time.After(time.Second):
					t.Fatal("invalid PDU error was not reported")
				}
			}
		})
	}
}
