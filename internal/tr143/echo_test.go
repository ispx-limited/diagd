package tr143

import (
	"encoding/binary"
	"net"
	"net/netip"
	"testing"
	"time"
)

func startEchoServer(t *testing.T, cfg EchoConfig) (*EchoServer, *net.UDPConn) {
	t.Helper()
	srvConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srvConn.Close() })
	s := NewEchoServer(srvConn, cfg)
	go s.Serve()

	cli, err := net.DialUDP("udp", nil, srvConn.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cli.Close() })
	return s, cli
}

func echoRequest(genSN, iteration uint32, padding int) []byte {
	pkt := make([]byte, 24+padding)
	binary.BigEndian.PutUint32(pkt[0:], genSN)
	binary.BigEndian.PutUint32(pkt[20:], iteration)
	for i := 24; i < len(pkt); i++ {
		pkt[i] = byte(i)
	}
	return pkt
}

func roundTrip(t *testing.T, cli *net.UDPConn, req []byte) []byte {
	t.Helper()
	if _, err := cli.Write(req); err != nil {
		t.Fatal(err)
	}
	cli.SetReadDeadline(time.Now().Add(2 * time.Second))
	resp := make([]byte, 65536)
	n, err := cli.Read(resp)
	if err != nil {
		t.Fatal(err)
	}
	return resp[:n]
}

func TestEchoPlus(t *testing.T) {
	_, cli := startEchoServer(t, EchoConfig{})

	for i := uint32(1); i <= 3; i++ {
		req := echoRequest(100+i, i, 40)
		resp := roundTrip(t, cli, req)

		if len(resp) != len(req) {
			t.Fatalf("response length %d, want %d", len(resp), len(req))
		}
		if got := binary.BigEndian.Uint32(resp[0:]); got != 100+i {
			t.Errorf("TestGenSN modified: got %d, want %d", got, 100+i)
		}
		if got := binary.BigEndian.Uint32(resp[echoOffRespSN:]); got != i {
			t.Errorf("TestRespSN = %d, want %d", got, i)
		}
		if got := binary.BigEndian.Uint32(resp[20:]); got != i {
			t.Errorf("TestIterationNumber modified: got %d, want %d", got, i)
		}
		if got := binary.BigEndian.Uint32(resp[echoOffFailureCount:]); got != 0 {
			t.Errorf("TestRespReplyFailureCount = %d, want 0", got)
		}
		recv := binary.BigEndian.Uint32(resp[echoOffRecvTime:])
		reply := binary.BigEndian.Uint32(resp[echoOffReplyTime:])
		if reply-recv > 1_000_000 {
			t.Errorf("server processing interval %d us implausibly large", reply-recv)
		}
		for j := 24; j < len(resp); j++ {
			if resp[j] != byte(j) {
				t.Fatalf("padding modified at offset %d", j)
			}
		}
	}
}

func TestEchoPlainReflect(t *testing.T) {
	// Payloads too short for Echo Plus fields are reflected unmodified
	// (RFC 862 behavior, TR-143 A.1.4).
	_, cli := startEchoServer(t, EchoConfig{})
	req := []byte("hello echo")
	resp := roundTrip(t, cli, req)
	if string(resp) != string(req) {
		t.Errorf("plain echo modified payload: %q", resp)
	}
}

func TestEchoLegacy20Byte(t *testing.T) {
	// The legacy format ends at offset 20; server fields must be filled and
	// TestGenSN left alone.
	_, cli := startEchoServer(t, EchoConfig{})
	req := make([]byte, 20)
	binary.BigEndian.PutUint32(req[0:], 42)
	resp := roundTrip(t, cli, req)
	if len(resp) != 20 {
		t.Fatalf("response length %d, want 20", len(resp))
	}
	if got := binary.BigEndian.Uint32(resp[0:]); got != 42 {
		t.Errorf("TestGenSN modified: got %d", got)
	}
	if got := binary.BigEndian.Uint32(resp[echoOffRespSN:]); got != 1 {
		t.Errorf("TestRespSN = %d, want 1", got)
	}
}

func TestEchoACL(t *testing.T) {
	deny := func(netip.Addr) bool { return false }
	s, cli := startEchoServer(t, EchoConfig{Allow: deny})

	if _, err := cli.Write(echoRequest(1, 1, 0)); err != nil {
		t.Fatal(err)
	}
	cli.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	buf := make([]byte, 1500)
	if n, err := cli.Read(buf); err == nil {
		t.Fatalf("got %d-byte response for disallowed source, want silence", n)
	}
	if got := s.Stats().PacketsReceived; got != 0 {
		t.Errorf("disallowed packet counted as received: %d", got)
	}
}

func TestEchoStats(t *testing.T) {
	s, cli := startEchoServer(t, EchoConfig{})
	roundTrip(t, cli, echoRequest(1, 1, 0))
	roundTrip(t, cli, []byte("plain"))
	st := s.Stats()
	if st.PacketsReceived != 2 {
		t.Errorf("PacketsReceived = %d, want 2", st.PacketsReceived)
	}
	if st.Responses != 1 {
		t.Errorf("Responses = %d, want 1 (plain echo does not count)", st.Responses)
	}
}
