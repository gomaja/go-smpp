package gosmpp

import (
	"testing"
	"time"

	"github.com/gomaja/go-smpp/pdu"
	"github.com/stretchr/testify/require"
)

func TestInvalidSessionSettings(t *testing.T) {
	auth := nextAuth()

	_, err := NewSession(
		TXConnector(NonTLSDialer, auth),
		Settings{}, 2*time.Second)
	require.Error(t, err)

	_, err = NewSession(
		RXConnector(NonTLSDialer, auth),
		Settings{
			ReadTimeout: 200 * time.Millisecond,
			EnquireLink: 333 * time.Millisecond,
		}, 2*time.Second)
	require.Error(t, err)
}

func TestGetWindowSize(t *testing.T) {
	for _, tc := range []struct {
		name    string
		bind    pdu.BindingType
		expire  bool
		wantErr error
	}{
		{name: "TX", bind: pdu.Transmitter},
		{name: "RX", bind: pdu.Receiver, wantErr: ErrWindowSizeNotAvailableOnReceiverBinds},
		{name: "TRX", bind: pdu.Transceiver},
		{name: "TRX with expiry", bind: pdu.Transceiver, expire: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			window := &WindowedRequestTracking{
				MaxWindowSize:      10,
				StoreAccessTimeOut: time.Second,
			}
			if tc.expire {
				window.ExpireCheckTimer = 5 * time.Second
				window.PduExpireTimeOut = 10 * time.Second
			}
			var c Connector
			switch tc.bind {
			case pdu.Transmitter:
				c = TXConnector(NonTLSDialer, nextAuth())
			case pdu.Receiver:
				c = RXConnector(NonTLSDialer, nextAuth())
			case pdu.Transceiver:
				c = TRXConnector(NonTLSDialer, nextAuth())
			}
			s, err := NewSession(c, Settings{
				ReadTimeout:             time.Minute,
				WindowedRequestTracking: window,
			}, 0)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, s.Close()) })

			size, err := s.GetWindowSize()
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
			}
			require.Zero(t, size)
		})
	}
}
