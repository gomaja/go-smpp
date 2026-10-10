package gosmpp

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/gomaja/go-smpp/data"
	"github.com/gomaja/go-smpp/pdu"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	countSubmitSMResp, countDeliverSM int32
)

func handlePDU(t *testing.T) func(pdu.PDU, bool) {
	return func(p pdu.PDU, responded bool) {
		switch pd := p.(type) {
		case *pdu.SubmitSMResp:
			require.False(t, responded)
			require.EqualValues(t, data.ESME_ROK, pd.CommandStatus)
			require.NotZero(t, len(pd.MessageID))
			atomic.AddInt32(&countSubmitSMResp, 1)

		case *pdu.GenericNack:
			require.False(t, responded)
			t.Fatal(pd)

		case *pdu.DataSM:
			require.True(t, responded)
			t.Logf("%+v\n", pd)

		case *pdu.DeliverSM:
			require.True(t, responded)
			require.EqualValues(t, data.ESME_ROK, pd.CommandStatus)

			_mess, err := pd.Message.GetMessageWithEncoding(data.UCS2)
			assert.Nil(t, err)
			if mess == _mess {
				atomic.AddInt32(&countDeliverSM, 1)
			}

		}
	}
}

func transceivableHandleAllPDU(t *testing.T) func(pdu.PDU) (pdu.PDU, bool) {
	return func(p pdu.PDU) (pdu.PDU, bool) {
		switch pd := p.(type) {
		case *pdu.SubmitSMResp:

			require.EqualValues(t, data.ESME_ROK, pd.CommandStatus)
			require.NotZero(t, len(pd.MessageID))
			atomic.AddInt32(&countSubmitSMResp, 1)
			return nil, false

		case *pdu.GenericNack:
			t.Fatal(pd)
			return nil, false
		case *pdu.DataSM:
			t.Logf("%+v\n", pd)
			return p.GetResponse(), false
		case *pdu.Unbind:
			t.Logf("%+v\n", pd)
			return p.GetResponse(), true

		case *pdu.DeliverSM:
			require.EqualValues(t, data.ESME_ROK, pd.CommandStatus)

			_mess, err := pd.Message.GetMessageWithEncoding(data.UCS2)
			assert.Nil(t, err)
			if mess == _mess {
				atomic.AddInt32(&countDeliverSM, 1)
			}
			return p.GetResponse(), false
		}
		return nil, false
	}
}

func TestTRXSubmitSM(t *testing.T) {
	baseline := atomic.LoadInt32(&countSubmitSMResp)
	responses := newTRXTestResponses()
	auth := nextAuth()
	trans, err := NewSession(
		TRXConnector(NonTLSDialer, auth),
		Settings{
			ReadTimeout: 2 * time.Second,

			WriteTimeout: 3 * time.Second,

			EnquireLink: 200 * time.Millisecond,

			OnSubmitError: func(_ pdu.PDU, err error) {
				t.Fatal(err)
			},

			OnReceivingError: func(err error) {
				t.Log(err)
			},

			OnRebindingError: func(err error) {
				t.Log(err)
			},

			OnPDU: func(p pdu.PDU, responded bool) {
				handlePDU(t)(p, responded)
				responses.received(p)
			},

			OnClosed: func(state State) {
				t.Log(state)
			},
		}, 5*time.Second)
	require.Nil(t, err)
	require.NotNil(t, trans)
	defer func() {
		_ = trans.Close()
	}()

	require.Equal(t, testSMSCSystemID, trans.Transceiver().SystemID())

	for i := 0; i < 20; i++ {
		request := newSubmitSM(auth.SystemID)
		require.NoError(t, trans.Transceiver().Submit(request))
		responses.waitSubmit(t, request.GetSequenceNumber())
	}
	require.True(t, atomic.LoadInt32(&countSubmitSMResp)-baseline >= 20)
	responses.waitEnquireLink(t)

	// Rebinding must restart both directions before accepting another request.
	trans.rebind()
	request := newSubmitSM(auth.SystemID)
	require.NoError(t, trans.Transceiver().Submit(request))
	responses.waitSubmit(t, request.GetSequenceNumber())
	require.True(t, atomic.LoadInt32(&countSubmitSMResp)-baseline >= 21)
}

func TestTRXSubmitSM_with_OnAllPDU(t *testing.T) {
	baseline := atomic.LoadInt32(&countSubmitSMResp)
	responses := newTRXTestResponses()
	auth := nextAuth()
	trans, err := NewSession(
		TRXConnector(NonTLSDialer, auth),
		Settings{
			ReadTimeout: 2 * time.Second,

			WriteTimeout: 3 * time.Second,

			EnquireLink: 200 * time.Millisecond,

			OnSubmitError: func(_ pdu.PDU, err error) {
				t.Fatal(err)
			},

			OnReceivingError: func(err error) {
				t.Log(err)
			},

			OnRebindingError: func(err error) {
				t.Log(err)
			},

			OnAllPDU: func(p pdu.PDU) (pdu.PDU, bool) {
				response, closeBind := transceivableHandleAllPDU(t)(p)
				responses.received(p)
				return response, closeBind
			},

			OnClosed: func(state State) {
				t.Log(state)
			},
		}, 5*time.Second)
	require.Nil(t, err)
	require.NotNil(t, trans)
	defer func() {
		_ = trans.Close()
	}()

	require.Equal(t, testSMSCSystemID, trans.Transceiver().SystemID())

	for i := 0; i < 20; i++ {
		request := newSubmitSM(auth.SystemID)
		require.NoError(t, trans.Transceiver().Submit(request))
		responses.waitSubmit(t, request.GetSequenceNumber())
	}
	require.True(t, atomic.LoadInt32(&countSubmitSMResp)-baseline >= 20)
	responses.waitEnquireLink(t)

	// Rebinding must restart both directions before accepting another request.
	trans.rebind()
	request := newSubmitSM(auth.SystemID)
	require.NoError(t, trans.Transceiver().Submit(request))
	responses.waitSubmit(t, request.GetSequenceNumber())
	require.True(t, atomic.LoadInt32(&countSubmitSMResp)-baseline >= 21)
}

type trxTestResponses struct {
	submit      chan int32
	enquireLink chan struct{}
}

func newTRXTestResponses() *trxTestResponses {
	return &trxTestResponses{submit: make(chan int32, 1), enquireLink: make(chan struct{}, 1)}
}

func (responses *trxTestResponses) received(p pdu.PDU) {
	switch p.(type) {
	case *pdu.SubmitSMResp:
		responses.submit <- p.GetSequenceNumber()
	case *pdu.EnquireLinkResp:
		select {
		case responses.enquireLink <- struct{}{}:
		default:
		}
	}
}

func (responses *trxTestResponses) waitSubmit(t *testing.T, sequence int32) {
	t.Helper()
	select {
	case got := <-responses.submit:
		require.Equal(t, sequence, got, "response must complete this test's request")
	case <-time.After(3 * time.Second):
		t.Fatalf("no submit_sm_resp for sequence %d", sequence)
	}
}

func (responses *trxTestResponses) waitEnquireLink(t *testing.T) {
	t.Helper()
	select {
	case <-responses.enquireLink:
	case <-time.After(3 * time.Second):
		t.Fatal("keep-alive did not receive enquire_link_resp")
	}
}

func newSubmitSM(systemID string) *pdu.SubmitSM {
	// build up submitSM
	srcAddr := pdu.NewAddress()
	srcAddr.SetTon(5)
	srcAddr.SetNpi(0)
	_ = srcAddr.SetAddress(systemID)

	destAddr := pdu.NewAddress()
	destAddr.SetTon(1)
	destAddr.SetNpi(1)
	_ = destAddr.SetAddress("12" + systemID)

	submitSM := pdu.NewSubmitSM().(*pdu.SubmitSM)
	submitSM.SourceAddr = srcAddr
	submitSM.DestAddr = destAddr
	_ = submitSM.Message.SetMessageWithEncoding(mess, data.UCS2)
	submitSM.ProtocolID = 0
	submitSM.RegisteredDelivery = 1
	submitSM.ReplaceIfPresentFlag = 0
	submitSM.EsmClass = 0

	return submitSM
}

func TestTRXSubmitSM_with_WindowConfig(t *testing.T) {
	baseline := atomic.LoadInt32(&countSubmitSMResp)
	responses := newTRXTestResponses()
	auth := nextAuth()
	trans, err := NewSession(
		TRXConnector(NonTLSDialer, auth),
		Settings{
			ReadTimeout: 2 * time.Second,

			WriteTimeout: 3 * time.Second,

			EnquireLink: 200 * time.Millisecond,

			OnSubmitError: func(_ pdu.PDU, err error) {
				t.Fatal(err)
			},

			OnReceivingError: func(err error) {
				t.Log(err)
			},

			OnRebindingError: func(err error) {
				t.Log(err)
			},

			WindowedRequestTracking: &WindowedRequestTracking{
				OnReceivedPduRequest: handleReceivedPduRequest(t),
				OnExpectedPduResponse: func(response Response) {
					handleExpectedPduResponse(t)(response)
					responses.received(response.PDU)
				},
				OnUnexpectedPduResponse: func(response pdu.PDU) {
					// Automatic keepalives bypass the store and have no window entry.
					if _, enquiry := response.(*pdu.EnquireLinkResp); enquiry {
						responses.received(response)
					}
				},
				OnExpiredPduRequest: nil,
				PduExpireTimeOut:    30 * time.Second,
				ExpireCheckTimer:    10 * time.Second,
				MaxWindowSize:       30,
				EnableAutoRespond:   false,
				StoreAccessTimeOut:  100 * time.Millisecond,
			},

			OnClosed: func(state State) {
				t.Log(state)
			},
		}, 5*time.Second)
	require.Nil(t, err)
	require.NotNil(t, trans)
	defer func() {
		_ = trans.Close()
	}()

	require.Equal(t, testSMSCSystemID, trans.Transceiver().SystemID())

	for i := 0; i < 50; i++ {
		request := newSubmitSM(auth.SystemID)
		require.NoError(t, trans.Transceiver().Submit(request))
		responses.waitSubmit(t, request.GetSequenceNumber())
	}
	require.True(t, atomic.LoadInt32(&countSubmitSMResp)-baseline >= 50)
	responses.waitEnquireLink(t)

	// Rebinding must restart both directions before accepting another request.
	trans.rebind()
	request := newSubmitSM(auth.SystemID)
	require.NoError(t, trans.Transceiver().Submit(request))
	responses.waitSubmit(t, request.GetSequenceNumber())
	require.True(t, atomic.LoadInt32(&countSubmitSMResp)-baseline >= 51)
}

func TestTRXSubmitSM_with_WindowConfig_and_AutoRespond(t *testing.T) {
	baseline := atomic.LoadInt32(&countSubmitSMResp)
	responses := newTRXTestResponses()
	auth := nextAuth()
	trans, err := NewSession(
		TRXConnector(NonTLSDialer, auth),
		Settings{
			ReadTimeout: 2 * time.Second,

			WriteTimeout: 3 * time.Second,

			EnquireLink: 200 * time.Millisecond,

			OnSubmitError: func(_ pdu.PDU, err error) {
				t.Fatal(err)
			},

			OnReceivingError: func(err error) {
				t.Log(err)
			},

			OnRebindingError: func(err error) {
				t.Log(err)
			},

			WindowedRequestTracking: &WindowedRequestTracking{
				OnReceivedPduRequest: handleReceivedPduRequest(t),
				OnExpectedPduResponse: func(response Response) {
					handleExpectedPduResponse(t)(response)
					responses.received(response.PDU)
				},
				OnUnexpectedPduResponse: func(response pdu.PDU) {
					// Automatic keepalives bypass the store and have no window entry.
					if _, enquiry := response.(*pdu.EnquireLinkResp); enquiry {
						responses.received(response)
					}
				},
				OnExpiredPduRequest: nil,
				PduExpireTimeOut:    30 * time.Second,
				ExpireCheckTimer:    10 * time.Second,
				MaxWindowSize:       30,
				EnableAutoRespond:   true,
				StoreAccessTimeOut:  100 * time.Millisecond,
			},

			OnClosed: func(state State) {
				t.Log("rebinded")
			},
		}, 5*time.Second)
	require.Nil(t, err)
	require.NotNil(t, trans)
	defer func() {
		_ = trans.Close()
	}()

	require.Equal(t, testSMSCSystemID, trans.Transceiver().SystemID())

	for i := 0; i < 20; i++ {
		request := newSubmitSM(auth.SystemID)
		require.NoError(t, trans.Transceiver().Submit(request))
		responses.waitSubmit(t, request.GetSequenceNumber())
	}
	require.True(t, atomic.LoadInt32(&countSubmitSMResp)-baseline >= 20)
	responses.waitEnquireLink(t)

	// Rebinding must restart both directions before accepting another request.
	trans.rebind()
	request := newSubmitSM(auth.SystemID)
	require.NoError(t, trans.Transceiver().Submit(request))
	responses.waitSubmit(t, request.GetSequenceNumber())
	require.True(t, atomic.LoadInt32(&countSubmitSMResp)-baseline >= 21)
}

func handleReceivedPduRequest(t *testing.T) func(pdu.PDU) (pdu.PDU, bool) {
	return func(p pdu.PDU) (pdu.PDU, bool) {
		switch pd := p.(type) {
		case *pdu.Unbind:
			return pd.GetResponse(), true

		case *pdu.GenericNack:
			t.Fatal(pd)

		case *pdu.EnquireLink:
			return pd.GetResponse(), false

		case *pdu.DataSM:
			t.Logf("%+v\n", pd)
			return pd.GetResponse(), false

		case *pdu.DeliverSM:
			require.EqualValues(t, data.ESME_ROK, pd.CommandStatus)

			_mess, err := pd.Message.GetMessageWithEncoding(data.UCS2)
			assert.Nil(t, err)
			if mess == _mess {
				atomic.AddInt32(&countDeliverSM, 1)
			}
			return pd.GetResponse(), false
		}
		return nil, false
	}
}

func handleExpectedPduResponse(t *testing.T) func(response Response) {
	return func(response Response) {

		switch pp := response.PDU.(type) {
		case *pdu.UnbindResp:
			//t.Logf("%+v\n", pp)

		case *pdu.SubmitSMResp:
			require.NotZero(t, len(pp.MessageID))
			atomic.AddInt32(&countSubmitSMResp, 1)
			t.Logf("%+v with original %+v\n", pp, response.OriginalRequest.PDU)

		case *pdu.EnquireLinkResp:
			t.Logf("%+v\n", pp)
		}
	}
}

func Test_newTransceivable(t *testing.T) {
	t.Run("always receive a non nil response", func(t *testing.T) {
		trans := newTransceivable(nil, Settings{}, nil)
		assert.NotNil(t, trans.in.settings.response)
	})
}
