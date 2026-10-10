package gosmpp

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync/atomic"
	"testing"

	"github.com/gomaja/go-smpp/pdu"

	"github.com/stretchr/testify/require"
)

var currentAuth int32

var auths = [][2]string{
	{"689528", "1a97ae"},
}

var smscAddr string

const mess = "Thử nghiệm: chuẩn bị nế mễ"

func nextAuth() Auth {
	pair := int(atomic.AddInt32(&currentAuth, 1)) % len(auths)
	return Auth{
		SMSC:       smscAddr,
		SystemID:   auths[pair][0],
		Password:   auths[pair][1],
		SystemType: "",
	}
}

func TestBindingSMSC(t *testing.T) {
	checker := func(t *testing.T, c Connector) {
		conn, err := c.Connect()
		require.Nil(t, err)
		require.NotNil(t, conn)
		_ = conn.Close()
	}

	t.Run("TX", func(t *testing.T) {
		checker(t, TXConnector(NonTLSDialer, nextAuth()))
	})

	t.Run("RX", func(t *testing.T) {
		checker(t, RXConnector(NonTLSDialer, nextAuth()))
	})
	t.Run("RX", func(t *testing.T) {
		addrRange := pdu.AddressRange{}
		addrRange.AddressRange = "31218"
		checker(t, RXConnector(NonTLSDialer, nextAuth(), WithAddressRange(addrRange)))
	})

	t.Run("TRX", func(t *testing.T) {
		checker(t, TRXConnector(NonTLSDialer, nextAuth()))
	})

	t.Run("TRX", func(t *testing.T) {
		addrRange := pdu.AddressRange{}
		addrRange.AddressRange = "31218"
		checker(t, TRXConnector(NonTLSDialer, nextAuth(), WithAddressRange(addrRange)))
	})
}

func TestBindingSMSC_Error(t *testing.T) {
	auth := Auth{SMSC: smscAddr, SystemID: "invalid"}
	checker := func(t *testing.T, c Connector) {
		conn, err := c.Connect()
		require.ErrorContains(t, err, "Invalid System ID")
		_ = conn.Close()
	}

	t.Run("TX", func(t *testing.T) {
		checker(t, TXConnector(NonTLSDialer, auth))
	})

	t.Run("RX", func(t *testing.T) {
		checker(t, RXConnector(NonTLSDialer, auth))
	})

	t.Run("TRX", func(t *testing.T) {
		checker(t, TRXConnector(NonTLSDialer, auth))
	})
}

func TestBindingType(t *testing.T) {
	auth := Auth{SMSC: smscAddr, SystemID: "invalid"}

	t.Run("TX", func(t *testing.T) {
		c := TXConnector(NonTLSDialer, auth)
		require.Equal(t, c.GetBindType(), pdu.Transmitter)
	})

	t.Run("RX", func(t *testing.T) {
		c := RXConnector(NonTLSDialer, auth)
		require.Equal(t, c.GetBindType(), pdu.Receiver)
	})

	t.Run("TRX", func(t *testing.T) {
		c := TRXConnector(NonTLSDialer, auth)
		require.Equal(t, c.GetBindType(), pdu.Transceiver)
	})
}

// A bind request that cannot be encoded must not come back inside the error:
// it carries the password, and logging the error with any verb (or encoding it
// as JSON) would print the credential.
func TestBindEncodeErrorOmitsPassword(t *testing.T) {
	const password = "pa\x00ss-secret"
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = listener.Close() }()
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr == nil {
			_ = conn.Close()
		}
	}()

	_, err = TRXConnector(NonTLSDialer, Auth{SMSC: listener.Addr().String(), SystemID: "id", Password: password}).Connect()
	require.Error(t, err)
	require.ErrorIs(t, err, pdu.ErrEncode)
	var encodeErr *pdu.EncodeError
	require.False(t, errors.As(err, &encodeErr), "the error must not carry the bind request")
	for _, format := range []string{"%v", "%+v", "%#v", "%s"} {
		require.NotContains(t, fmt.Sprintf(format, err), "secret", format)
	}
	encoded, marshalErr := json.Marshal(err)
	require.NoError(t, marshalErr)
	require.NotContains(t, string(encoded), "secret")
}
