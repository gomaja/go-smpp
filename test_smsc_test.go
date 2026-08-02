package gosmpp

import (
	"fmt"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gomaja/go-smpp/data"
	"github.com/gomaja/go-smpp/pdu"
)

const testSMSCSystemID = "go-smpp-test-smsc"

var testSMSCMessageID int32

type testSMSC struct {
	ln     net.Listener
	closed chan struct{}
	wg     sync.WaitGroup
	conns  sync.Map
}

func TestMain(m *testing.M) {
	srv, err := startTestSMSC()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "start test SMSC: %v\n", err)
		os.Exit(1)
	}

	smscAddr = srv.Addr()
	code := m.Run()
	srv.Close()
	os.Exit(code)
}

func startTestSMSC() (*testSMSC, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}

	srv := &testSMSC{
		ln:     ln,
		closed: make(chan struct{}),
	}
	srv.wg.Add(1)
	go srv.serve()

	return srv, nil
}

func (s *testSMSC) Addr() string {
	return s.ln.Addr().String()
}

func (s *testSMSC) Close() {
	close(s.closed)
	_ = s.ln.Close()
	s.conns.Range(func(key, _ any) bool {
		_ = key.(net.Conn).Close()
		return true
	})
	s.wg.Wait()
}

func (s *testSMSC) serve() {
	defer s.wg.Done()

	for {
		conn, err := s.ln.Accept()
		if err != nil {
			select {
			case <-s.closed:
				return
			default:
				return
			}
		}

		s.conns.Store(conn, struct{}{})
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer s.conns.Delete(conn)
			s.handle(conn)
		}()
	}
}

func (s *testSMSC) handle(conn net.Conn) {
	defer func() {
		_ = conn.Close()
	}()

	if !s.readBind(conn) {
		return
	}

	for {
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		req, err := pdu.Parse(conn)
		if err != nil {
			return
		}

		switch pd := req.(type) {
		case *pdu.SubmitSM:
			resp := pdu.NewSubmitSMRespFromReq(pd).(*pdu.SubmitSMResp)
			resp.MessageID = fmt.Sprintf("go-smpp-%d", atomic.AddInt32(&testSMSCMessageID, 1))
			if !writeTestPDU(conn, resp) {
				return
			}

		case *pdu.EnquireLink:
			if !writeTestPDU(conn, pd.GetResponse()) {
				return
			}

		case *pdu.Unbind:
			_ = writeTestPDU(conn, pd.GetResponse())
			return

		default:
			if req.CanResponse() && !writeTestPDU(conn, req.GetResponse()) {
				return
			}
		}
	}
}

func (s *testSMSC) readBind(conn net.Conn) bool {
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	req, err := pdu.Parse(conn)
	if err != nil {
		return false
	}

	bindReq, ok := req.(*pdu.BindRequest)
	if !ok {
		return false
	}

	resp := bindReq.GetResponse().(*pdu.BindResp)
	if bindReq.SystemID == "invalid" {
		resp.CommandStatus = data.ESME_RINVSYSID
		_ = writeTestPDU(conn, resp)
		return false
	}

	resp.SystemID = testSMSCSystemID
	return writeTestPDU(conn, resp)
}

func writeTestPDU(conn net.Conn, p pdu.PDU) bool {
	if p == nil {
		return true
	}
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	buf := pdu.NewBuffer(make([]byte, 0, 64))
	p.Marshal(buf)
	_, err := conn.Write(buf.Bytes())
	return err == nil
}
