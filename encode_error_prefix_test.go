package gosmpp

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/gomaja/go-smpp/data"
	"github.com/gomaja/go-smpp/pdu"
	"github.com/stretchr/testify/require"
)

func TestEncodeErrorPrefixConnection(t *testing.T) {
	local, peer := net.Pipe()
	t.Cleanup(func() { _ = local.Close() })
	t.Cleanup(func() { _ = peer.Close() })
	require.NoError(t, local.SetWriteDeadline(time.Now().Add(time.Second)))
	p := pdu.NewSubmitSM().(*pdu.SubmitSM)
	p.ServiceType = "synthetic\x00type"
	n, err := NewConnection(local).WritePDU(p)
	require.Zero(t, n)
	require.ErrorIs(t, err, pdu.ErrEncode)
	var encodeErr *pdu.EncodeError
	require.ErrorAs(t, err, &encodeErr)
	require.Same(t, p, encodeErr.PDU)
	require.ErrorIs(t, encodeErr.Unwrap(), pdu.ErrEncode)
	require.EqualError(t, err, "cannot encode PDU (*pdu.SubmitSM): C-Octet string contains an embedded terminator")
}

func TestEncodeErrorPrefixBind(t *testing.T) {
	const password = "synthetic\x00password"
	local, peer := net.Pipe()
	t.Cleanup(func() { _ = local.Close() })
	t.Cleanup(func() { _ = peer.Close() })
	dialer := DialerFunc(func(context.Context, string) (net.Conn, error) { return local, nil })
	session, err := NewSession(TRXConnector(dialer, Auth{SystemID: "id", Password: password}), Settings{ReadTimeout: time.Second}, 0)
	if session != nil {
		t.Cleanup(func() { _ = session.Close() })
	}
	require.Nil(t, session)
	require.ErrorIs(t, err, pdu.ErrEncode)
	var encodeErr *pdu.EncodeError
	require.NotErrorAs(t, err, &encodeErr)
	for _, format := range []string{"%v", "%+v", "%#v", "%s"} {
		require.NotContains(t, fmt.Sprintf(format, err), "password", format)
	}
	encoded, marshalErr := json.Marshal(err)
	require.NoError(t, marshalErr)
	require.NotContains(t, string(encoded), "password")
	require.EqualError(t, err, "cannot encode PDU: bind request: C-Octet string contains an embedded terminator")
}

func TestEncodeErrorPrefixSubmitToSystemID(t *testing.T) {
	srv, addr := startTestServer(t, ServerSettings{})
	require.NoError(t, srv.StartSystemID(SystemIDConfig{SystemID: "id", Password: "synthetic"}))
	_, resp := bind(t, addr, "id", "synthetic", pdu.Transceiver)
	require.Equal(t, data.ESME_ROK, resp.CommandStatus)
	waitForSessions(t, srv, "id", 1)
	p := pdu.NewDeliverSM().(*pdu.DeliverSM)
	p.ServiceType = "synthetic\x00type"
	err := srv.SubmitToSystemID(context.Background(), "id", p)
	require.ErrorIs(t, err, pdu.ErrEncode)
	var encodeErr *pdu.EncodeError
	require.ErrorAs(t, err, &encodeErr)
	require.Same(t, p, encodeErr.PDU)
	require.ErrorIs(t, encodeErr.Unwrap(), pdu.ErrEncode)
	require.EqualError(t, err, "submit to system ID \"id\": cannot encode PDU (*pdu.DeliverSM): C-Octet string contains an embedded terminator")
}
