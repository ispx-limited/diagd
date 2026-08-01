package tr143

import (
	"encoding/binary"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"sync/atomic"
	"time"
)

// UDPEchoPlus payload layout (TR-143 A.1.4, Table 1). All fields are 32-bit
// big-endian. The server writes only offsets 4 through 19; TestGenSN (0-3)
// and TestIterationNumber (20-23) are echoed unmodified.
const (
	echoOffRespSN       = 4
	echoOffRecvTime     = 8
	echoOffReplyTime    = 12
	echoOffFailureCount = 16

	// echoPlusMinLen is the smallest payload treated as UDPEchoPlus rather
	// than plain RFC 862 echo. TR-143 A.1.4 defines a 24-byte format; the
	// legacy 20-byte format (without TestIterationNumber) shares the first
	// 20 bytes, and since the server never writes past offset 19 both are
	// served by the same threshold.
	echoPlusMinLen = 20
)

// EchoConfig configures the UDP Echo Plus responder.
type EchoConfig struct {
	// Allow restricts which source addresses are echoed. Packets from other
	// sources are silently ignored (TR-143 A.1.7 step 1). nil permits all.
	Allow AllowFunc

	Log *slog.Logger
}

// EchoServer answers RFC 862 UDP echo and TR-143 UDPEchoPlus requests on a
// single UDP socket. Counters follow the spec's enable semantics: they are
// zeroed when the server is created and run until it stops.
type EchoServer struct {
	cfg  EchoConfig
	log  *slog.Logger
	conn *net.UDPConn

	// epoch anchors the wrapping 32-bit microsecond timestamp counter
	// (TR-143 A.1.4). Stored without a monotonic reading so that timestamps
	// derived from kernel receive times (wall clock) and from time.Now() use
	// the same base.
	epoch time.Time

	received   atomic.Uint64
	responses  atomic.Uint32 // TestRespSN
	failures   atomic.Uint32 // TestRespReplyFailureCount
	lastDrops  uint32        // kernel-reported socket drops, cumulative
	haveKernTS bool
}

// EchoStats is a snapshot of responder counters.
type EchoStats struct {
	PacketsReceived uint64
	Responses       uint32
	Failures        uint32
}

// NewEchoServer wraps an already-bound UDP connection. On Linux it enables
// kernel receive timestamps and socket-drop accounting; both degrade
// gracefully elsewhere.
func NewEchoServer(conn *net.UDPConn, cfg EchoConfig) *EchoServer {
	s := &EchoServer{
		cfg:   cfg,
		log:   cfg.Log,
		conn:  conn,
		epoch: time.Now().Round(0),
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	if err := enableRxTimestamps(conn); err == nil {
		s.haveKernTS = true
	} else {
		s.log.Debug("kernel receive timestamps unavailable", "err", err)
	}
	return s
}

// Stats returns a snapshot of the responder counters.
func (s *EchoServer) Stats() EchoStats {
	return EchoStats{
		PacketsReceived: s.received.Load(),
		Responses:       s.responses.Load(),
		Failures:        s.failures.Load(),
	}
}

// Serve processes echo requests until the connection is closed.
func (s *EchoServer) Serve() error {
	buf := make([]byte, 65536)
	oob := make([]byte, 512)
	for {
		n, oobn, _, peer, err := s.conn.ReadMsgUDPAddrPort(buf, oob)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		rxTime := time.Now()
		if oobn > 0 {
			if t, drops, ok := parseRxControl(oob[:oobn]); ok {
				if !t.IsZero() {
					rxTime = t
				}
				// Kernel receive-queue overflow is exactly the "valid request
				// the server could not respond to" case of TR-143 A.1.8.5:
				// count it so overload is not misattributed to network loss.
				if drops > s.lastDrops {
					s.failures.Add(drops - s.lastDrops)
				}
				if drops != 0 || s.lastDrops != 0 {
					s.lastDrops = drops
				}
			}
		}
		if s.cfg.Allow != nil && !s.cfg.Allow(peer.Addr().Unmap()) {
			continue
		}
		s.received.Add(1)
		s.respond(buf[:n], peer, rxTime)
	}
}

func (s *EchoServer) respond(pkt []byte, peer netip.AddrPort, rxTime time.Time) {
	if len(pkt) >= echoPlusMinLen {
		sn := s.responses.Load() + 1
		binary.BigEndian.PutUint32(pkt[echoOffRespSN:], sn)
		binary.BigEndian.PutUint32(pkt[echoOffRecvTime:], s.stamp(rxTime))
		binary.BigEndian.PutUint32(pkt[echoOffFailureCount:], s.failures.Load())
		binary.BigEndian.PutUint32(pkt[echoOffReplyTime:], s.stamp(time.Now()))
	}
	if _, err := s.conn.WriteToUDPAddrPort(pkt, peer); err != nil {
		s.failures.Add(1)
		s.log.Debug("echo response send failed", "peer", peer, "err", err)
		return
	}
	if len(pkt) >= echoPlusMinLen {
		s.responses.Add(1)
	}
}

// stamp converts a wall-clock time to the wrapping 32-bit microsecond counter
// of TR-143 A.1.4.
func (s *EchoServer) stamp(t time.Time) uint32 {
	return uint32(t.Sub(s.epoch).Microseconds())
}
