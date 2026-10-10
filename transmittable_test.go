package gosmpp

import (
	"fmt"
	"github.com/gomaja/go-smpp/data"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gomaja/go-smpp/pdu"

	"github.com/stretchr/testify/require"
)

func TestTransmit(t *testing.T) {
	t.Run("Binding", func(t *testing.T) {
		auth := nextAuth()
		transmitter, err := NewSession(
			TXConnector(NonTLSDialer, auth),
			Settings{
				ReadTimeout: 2 * time.Second,

				OnPDU: func(p pdu.PDU, _ bool) {
					t.Logf("%+v\n", p)
				},

				OnSubmitError: func(_ pdu.PDU, err error) {
					t.Fatal(err)
				},

				OnRebindingError: func(err error) {
					t.Fatal(err)
				},

				OnClosed: func(state State) {
					t.Log(state)
				},
			}, -1)
		require.Nil(t, err)
		require.NotNil(t, transmitter)
		defer func() {
			_ = transmitter.Close()
		}()

		require.Equal(t, testSMSCSystemID, transmitter.Transmitter().SystemID())

		err = transmitter.Transmitter().Submit(newSubmitSM(auth.SystemID))
		require.Nil(t, err)

		time.Sleep(400 * time.Millisecond)

		transmitter.rebind()
		err = transmitter.Transmitter().Submit(newSubmitSM(auth.SystemID))
		require.Nil(t, err)
	})

	errorHandling := func(t *testing.T, trigger func(*transmittable)) {
		conn, err := net.Dial("tcp", smscAddr)
		require.NoError(t, err)

		c := NewConnection(conn)
		tr := newTransmittable(c, Settings{}, nil)
		defer func() {
			_ = c.Close()

			// write on closed conn?
			n, err := tr.write(pdu.NewEnquireLink())
			require.NotNil(t, err)
			require.Zero(t, n)
		}()

		var count int32
		tr.settings.OnClosed = func(State) {
			atomic.AddInt32(&count, 1)
		}

		tr.settings.OnSubmitError = func(p pdu.PDU, err error) {
			require.NotNil(t, err)
			_, ok := p.(*pdu.CancelSM)
			require.True(t, ok)
		}

		// do trigger
		trigger(tr)

		time.Sleep(300 * time.Millisecond)
		require.NotZero(t, atomic.LoadInt32(&count))
	}

	t.Run("ErrorHandling", func(t *testing.T) {
		errorHandling(t, func(tr *transmittable) {
			var p pdu.CancelSM
			tr.check(&p, 100, fmt.Errorf("fake error"))
		})

		errorHandling(t, func(tr *transmittable) {
			var p pdu.CancelSM
			tr.check(&p, 0, fmt.Errorf("fake error"))
		})
	})

	t.Run("SubmitErr", func(t *testing.T) {
		var tr transmittable
		tr.input = make(chan pdu.PDU, 1)

		tr.aliveState = 1
		err := tr.Submit(nil)
		require.Error(t, err)

		tr.aliveState = 0
		err = tr.Submit(nil)
		require.NoError(t, err)
	})
}

func TestConcurrentSubmitClose(t *testing.T) {
	auth := nextAuth()
	transmitter, err := NewSession(
		TXConnector(NonTLSDialer, auth),
		Settings{
			ReadTimeout: 2 * time.Second,

			OnPDU: func(p pdu.PDU, _ bool) {
				t.Logf("%+v\n", p)
			},

			OnSubmitError: func(_ pdu.PDU, err error) {
				t.Fatal(err)
			},

			OnRebindingError: func(err error) {
				t.Fatal(err)
			},

			OnClosed: func(state State) {
				t.Log(state)
			},
		}, -1)
	require.Nil(t, err)
	require.NotNil(t, transmitter)
	defer func() {
		_ = transmitter.Close()
	}()

	require.Equal(t, testSMSCSystemID, transmitter.Transmitter().SystemID())

	var wg sync.WaitGroup

	wg.Add(1)

	toStart := make(chan struct{}, 1)
	go func() {
		defer wg.Done()

		time.Sleep(time.Millisecond)
		for i := 0; i < 100; i++ {
			err := transmitter.Transmitter().Submit(newSubmitSM(auth.SystemID))
			require.True(t, err == nil || err == ErrConnectionClosing)

			if i == 10 {
				toStart <- struct{}{}
			}
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()

		<-toStart
		_ = transmitter.close()
	}()

	wg.Wait()
}

func TestSessionSurvivesEncodeFailure(t *testing.T) {
	for _, kind := range []string{"terminator", "message_length", "pdu_length"} {
		t.Run(kind, func(t *testing.T) {
			auth := nextAuth()
			submitErrors := make(chan error, 2)
			responses := make(chan pdu.PDU, 4)
			closed := make(chan State, 4)
			session, err := NewSession(TRXConnector(NonTLSDialer, auth), Settings{ReadTimeout: 3 * time.Second, OnSubmitError: func(_ pdu.PDU, err error) { submitErrors <- err }, OnPDU: func(p pdu.PDU, _ bool) { responses <- p }, OnClosed: func(s State) { closed <- s }}, -1)
			require.NoError(t, err)
			defer func() { _ = session.Close() }()
			invalid := newSubmitSM(auth.SystemID)
			switch kind {
			case "terminator":
				invalid.ServiceType = "A\x00B"
			case "message_length":
				require.NoError(t, invalid.Message.SetLongMessageWithEnc(strings.Repeat("A", 300), data.GSM7BIT))
			case "pdu_length":
				invalid.AppendOptionalParam(pdu.NewField(pdu.TagMessagePayload, make([]byte, 65535)))
			}
			require.NoError(t, session.Transceiver().Submit(invalid))
			select {
			case err := <-submitErrors:
				require.ErrorIs(t, err, pdu.ErrEncode)
				var encode *pdu.EncodeError
				require.ErrorAs(t, err, &encode)
				require.Same(t, invalid, encode.PDU)
				require.NotNil(t, encode.Unwrap())
			case <-time.After(3 * time.Second):
				t.Fatal("missing submit error")
			}
			valid := newSubmitSM(auth.SystemID)
			require.NoError(t, session.Transceiver().Submit(valid))
			select {
			case got := <-responses:
				require.Equal(t, data.SUBMIT_SM_RESP, got.GetHeader().CommandID)
				require.Equal(t, valid.GetSequenceNumber(), got.GetSequenceNumber())
			case state := <-closed:
				t.Fatalf("session closed after encode failure: %v", state)
			case <-time.After(3 * time.Second):
				t.Fatal("next valid submit did not receive a response")
			}
		})
	}
}
