package pdu

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/gomaja/go-smpp/data"

	"github.com/stretchr/testify/require"
)

func TestBindRequest(t *testing.T) {
	t.Run("receiver", func(t *testing.T) {
		req := NewBindReceiver().(*BindRequest)
		require.True(t, req.CanResponse())
		req.SequenceNumber = 13

		validate(t, req.GetResponse(), "0000001180000001000000000000000d00", data.BIND_RECEIVER_RESP)

		req.SystemID = "system_id_fake"
		req.Password = "password"
		req.SystemType = "only"
		req.InterfaceVersion = 44
		req.AddressRange.AddressRange = "emptY"
		req.AddressRange.Ton = 23
		req.AddressRange.Npi = 101

		validate(t,
			req,
			"0000003600000001000000000000000d73797374656d5f69645f66616b650070617373776f7264006f6e6c79002c1765656d70745900",
			data.BIND_RECEIVER,
		)
	})

	t.Run("transmitter", func(t *testing.T) {
		req := NewBindTransmitter().(*BindRequest)
		require.True(t, req.CanResponse())
		req.SequenceNumber = 13

		validate(t, req.GetResponse(), "0000001180000002000000000000000d00", data.BIND_TRANSMITTER_RESP)

		req.SystemID = "system_id_fake"
		req.Password = "password"
		req.SystemType = "only"
		req.InterfaceVersion = 44
		req.AddressRange.AddressRange = "emptY"
		req.AddressRange.Ton = 23
		req.AddressRange.Npi = 101

		validate(t,
			req,
			"0000003600000002000000000000000d73797374656d5f69645f66616b650070617373776f7264006f6e6c79002c1765656d70745900",
			data.BIND_TRANSMITTER,
		)
	})

	t.Run("transceiver", func(t *testing.T) {
		req := NewBindTransceiver().(*BindRequest)
		require.True(t, req.CanResponse())
		req.SequenceNumber = 13

		validate(t, req.GetResponse(), "0000001180000009000000000000000d00", data.BIND_TRANSCEIVER_RESP)

		req.SystemID = "system_id_fake"
		req.Password = "password"
		req.SystemType = "only"
		req.InterfaceVersion = 44
		req.AddressRange.AddressRange = "emptY"
		req.AddressRange.Ton = 23
		req.AddressRange.Npi = 101

		validate(t,
			req,
			"0000003600000009000000000000000d73797374656d5f69645f66616b650070617373776f7264006f6e6c79002c1765656d70745900",
			data.BIND_TRANSCEIVER,
		)
	})
}

func TestBindingTypeString(t *testing.T) {
	tests := []struct {
		name string
		typ  BindingType
		want string
	}{
		{name: "receiver", typ: Receiver, want: "receiver"},
		{name: "transmitter", typ: Transmitter, want: "transmitter"},
		{name: "transceiver", typ: Transceiver, want: "transceiver"},
		{name: "unknown", typ: BindingType(99), want: "BindingType(99)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, tt.typ.String())

			var formatted bytes.Buffer
			_, err := fmt.Fprintf(&formatted, "%s", tt.typ)
			require.NoError(t, err)
			require.Equal(t, tt.want, formatted.String())
		})
	}
}
