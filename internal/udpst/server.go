package udpst

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"
)

// Protocol timing constants (udpst.h).
const (
	watchdogTimeout  = 3 * time.Second // TIMEOUT_NOTRAFFIC
	noTrafficWarning = time.Second     // WARNING_NOTRAFFIC
)

// Config configures a TR-471 measurement server instance.
type Config struct {
	// Keys maps key IDs to shared secrets for HMAC authentication of
	// control PDUs. Empty disables authentication; non-empty requires it.
	Keys map[uint8]string

	// MaxBandwidthMbps enables admission control: the summed bandwidth
	// requirement of accepted tests per direction never exceeds it, and
	// clients must state a requirement. 0 disables admission control.
	MaxBandwidthMbps int

	// Jumbo permits datagram sizes requiring jumbo frames, needed for
	// rates above about 1 Gbps. Clients must request the same setting.
	Jumbo bool

	// TraditionalMTU restricts payloads to a 1500 byte MTU. Clients must
	// request the same setting.
	TraditionalMTU bool

	// MaxTestSeconds clamps the client-requested test duration.
	// 0 applies the protocol maximum of 3600 seconds.
	MaxTestSeconds int

	// MaxSessions bounds concurrent test connections. 0 means 256.
	MaxSessions int

	// TestPortMin/TestPortMax, when both non-zero, restrict test connection
	// sockets to this UDP port range instead of kernel ephemeral ports.
	// A fixed range simplifies firewalling and load-balancer affinity.
	TestPortMin int
	TestPortMax int

	// Allow restricts which sources may set up tests. nil permits all.
	Allow func(netip.Addr) bool

	Log *slog.Logger
}

// Server answers udpst setup requests on a control socket and runs each
// accepted test on its own session socket.
type Server struct {
	cfg   Config
	log   *slog.Logger
	conn  *net.UDPConn
	table *RateTable

	mu       sync.Mutex
	usedUS   int // Mbps allocated to upstream tests
	usedDS   int // Mbps allocated to downstream tests
	sessions int
	nextPort int
	closed   bool
	wg       sync.WaitGroup

	testsUpstream   atomic.Uint64
	testsDownstream atomic.Uint64
	setupAccepts    atomic.Uint64
	setupRejects    atomic.Uint64
}

// Stats is a snapshot of server load, suitable for health reporting.
type Stats struct {
	ActiveSessions          int
	UpstreamMbpsAllocated   int
	DownstreamMbpsAllocated int
	TestsUpstream           uint64
	TestsDownstream         uint64
	SetupAccepts            uint64
	SetupRejects            uint64
}

// NewServer wraps an already-bound UDP control socket (conventionally port
// 24601).
func NewServer(conn *net.UDPConn, cfg Config) *Server {
	if cfg.MaxSessions == 0 {
		cfg.MaxSessions = 256
	}
	s := &Server{
		cfg:      cfg,
		log:      cfg.Log,
		conn:     conn,
		table:    BuildRateTable(cfg.Jumbo, cfg.TraditionalMTU),
		nextPort: cfg.TestPortMin,
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	return s
}

// Stats returns current server load.
func (s *Server) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Stats{
		ActiveSessions:          s.sessions,
		UpstreamMbpsAllocated:   s.usedUS,
		DownstreamMbpsAllocated: s.usedDS,
		TestsUpstream:           s.testsUpstream.Load(),
		TestsDownstream:         s.testsDownstream.Load(),
		SetupAccepts:            s.setupAccepts.Load(),
		SetupRejects:            s.setupRejects.Load(),
	}
}

// Close stops the control listener and waits for active sessions to finish.
func (s *Server) Close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.conn.Close()
	s.wg.Wait()
}

// Serve processes setup requests until the control socket is closed.
func (s *Server) Serve() error {
	buf := make([]byte, 512)
	for {
		n, peer, err := s.conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		if s.cfg.Allow != nil && !s.cfg.Allow(peer.Addr().Unmap()) {
			continue
		}
		req, err := parseSetup(buf[:n])
		if err != nil || req.CmdRequest != cmdSetupRequest {
			continue // not a setup request; silently ignore
		}
		s.handleSetup(req, buf[:n], peer)
	}
}

// handleSetup validates a Setup Request in the reference implementation's
// order and either rejects it with a response code or allocates a session.
func (s *Server) handleSetup(req *SetupPDU, raw []byte, peer netip.AddrPort) {
	resp := *req
	resp.CmdRequest = cmdSetupResponse

	var keys *sessionKeys
	var legacyKey []byte

	reject := func(code uint8) {
		resp.CmdResponse = code
		s.sendSetupResponse(&resp, req, keys, legacyKey, peer)
	}

	// Authentication is checked first so nothing else is revealed to an
	// unauthenticated peer.
	if req.Auth.AuthMode == authModeControl && len(s.cfg.Keys) > 0 {
		secret, ok := s.cfg.Keys[req.Auth.KeyID]
		if !ok {
			reject(SetupRespAuthFail)
			return
		}
		if req.ProtocolVer >= ProtocolVer {
			k := deriveSessionKeys(secret, req.Auth.AuthUnixTime)
			keys = &k
			if !verifyDigest(k.client[:], raw, setupAuthDigestOff, req.Auth.AuthDigest) {
				keys = nil
				reject(SetupRespAuthFail)
				return
			}
		} else {
			legacyKey = []byte(secret)
			if !verifyDigest(legacyKey, raw, setupAuthDigestOff, req.Auth.AuthDigest) {
				legacyKey = nil
				reject(SetupRespAuthFail)
				return
			}
		}
		skew := time.Since(time.Unix(int64(req.Auth.AuthUnixTime), 0))
		if skew < -authTimeWindow*time.Second || skew > authTimeWindow*time.Second {
			reject(SetupRespAuthTime)
			return
		}
	}

	if req.ProtocolVer < ProtocolMin || req.ProtocolVer > ProtocolVer {
		resp.ProtocolVer = ProtocolVer // tell the client what we expect
		reject(SetupRespBadVersion)
		return
	}
	if req.MCCount == 0 || req.MCCount > 24 || req.MCIndex >= req.MCCount {
		reject(SetupRespMCInvPar)
		return
	}
	if (req.Modifiers&modJumbo != 0) != s.cfg.Jumbo {
		reject(SetupRespBadJumbo)
		return
	}
	if (req.Modifiers&modTraditionalMTU != 0) != s.cfg.TraditionalMTU {
		reject(SetupRespBadTMTU)
		return
	}

	upstream := req.MaxBandwidth&bwUpstreamBit != 0
	mbw := int(req.MaxBandwidth &^ bwUpstreamBit)
	if s.cfg.MaxBandwidthMbps > 0 {
		if mbw == 0 {
			reject(SetupRespNoMaxBW)
			return
		}
		if !s.reserveBandwidth(upstream, mbw) {
			reject(SetupRespCapExc)
			return
		}
	} else {
		mbw = 0
	}
	releaseOnErr := func() {
		if mbw > 0 {
			s.releaseBandwidth(upstream, mbw)
		}
	}

	switch {
	case req.Auth.AuthMode == authModeControl && len(s.cfg.Keys) == 0:
		releaseOnErr()
		reject(SetupRespAuthNC)
		return
	case req.Auth.AuthMode == authModeNone && len(s.cfg.Keys) > 0:
		releaseOnErr()
		reject(SetupRespAuthReq)
		return
	case req.Auth.AuthMode > authModeControl:
		releaseOnErr()
		reject(SetupRespAuthInv)
		return
	}

	sessConn, err := s.newSessionSocket(peer)
	if err != nil {
		s.log.Warn("session socket allocation failed", "peer", peer, "err", err)
		releaseOnErr()
		reject(SetupRespConnFail)
		return
	}

	sess := &session{
		srv:        s,
		conn:       sessConn,
		setupPeer:  peer,
		pver:       req.ProtocolVer,
		authMode:   req.Auth.AuthMode,
		keys:       keys,
		legacyKey:  legacyKey,
		bwMbps:     mbw,
		bwUpstream: upstream,
		log: s.log.With("peer", peer,
			"port", sessConn.LocalAddr().(*net.UDPAddr).Port),
	}

	// The Null Request from the new socket opens the server-side firewall
	// or conntrack pinhole for the test connection (protocol version 20).
	// It is sent before the Setup Response on purpose: once the client
	// learns the test port it sends the Test Activation immediately, and a
	// null request still in flight in the opposite direction can lose the
	// kernel's conntrack clash resolution, making the client's send fail
	// with EPERM and the test time out. Establishing the flow first removes
	// that race.
	if req.ProtocolVer >= ProtocolVer {
		null := NullPDU{
			ProtocolVer: req.ProtocolVer,
			Auth: authTrailer{
				AuthMode:     req.Auth.AuthMode,
				AuthUnixTime: uint32(time.Now().Unix()),
				KeyID:        req.Auth.KeyID,
			},
		}
		pdu := null.marshal()
		if keys != nil {
			signPDU(keys.server[:], pdu, nullAuthDigestOff)
		}
		sessConn.WriteToUDPAddrPort(pdu, peer)
	}

	resp.CmdResponse = SetupRespACKOK
	resp.TestPort = uint16(sessConn.LocalAddr().(*net.UDPAddr).Port)
	s.sendSetupResponse(&resp, req, keys, legacyKey, peer)

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		sess.run()
	}()
}

func (s *Server) sendSetupResponse(resp, req *SetupPDU, keys *sessionKeys, legacyKey []byte, peer netip.AddrPort) {
	if resp.CmdResponse == SetupRespACKOK {
		s.setupAccepts.Add(1)
	} else {
		s.setupRejects.Add(1)
		s.log.Warn("test rejected",
			"test", "tr471", "peer", peer, "code", resp.CmdResponse, "reason", rejectReason(resp.CmdResponse))
	}
	pdu := resp.marshal()
	if req.ProtocolVer >= ProtocolVer && keys != nil {
		resp.Auth.AuthUnixTime = uint32(time.Now().Unix())
		pdu = resp.marshal()
		signPDU(keys.server[:], pdu, setupAuthDigestOff)
	} else if legacyKey != nil {
		signPDU(legacyKey, pdu, setupAuthDigestOff)
	}
	s.conn.WriteToUDPAddrPort(pdu, peer)
}

// rejectReason names a setup rejection code for logs.
func rejectReason(code uint8) string {
	switch code {
	case SetupRespBadVersion:
		return "protocol version"
	case SetupRespBadJumbo:
		return "jumbo option mismatch"
	case SetupRespAuthNC:
		return "authentication not configured"
	case SetupRespAuthReq:
		return "authentication required"
	case SetupRespAuthInv:
		return "authentication mode invalid"
	case SetupRespAuthFail:
		return "authentication failed"
	case SetupRespAuthTime:
		return "authentication time window"
	case SetupRespNoMaxBW:
		return "bandwidth requirement missing"
	case SetupRespCapExc:
		return "capacity exceeded"
	case SetupRespBadTMTU:
		return "traditional MTU mismatch"
	case SetupRespMCInvPar:
		return "multi-connection parameters"
	case SetupRespConnFail:
		return "connection allocation"
	}
	return "unknown"
}

func (s *Server) reserveBandwidth(upstream bool, mbw int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	used := &s.usedDS
	if upstream {
		used = &s.usedUS
	}
	if *used+mbw > s.cfg.MaxBandwidthMbps {
		return false
	}
	*used += mbw
	return true
}

func (s *Server) releaseBandwidth(upstream bool, mbw int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if upstream {
		s.usedUS -= mbw
	} else {
		s.usedDS -= mbw
	}
}

// newSessionSocket binds a fresh UDP socket for a test connection on the same
// local address as the control socket, using the configured test port range
// or a kernel ephemeral port.
func (s *Server) newSessionSocket(peer netip.AddrPort) (*net.UDPConn, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, errors.New("server closed")
	}
	if s.sessions >= s.cfg.MaxSessions {
		s.mu.Unlock()
		return nil, errors.New("session limit reached")
	}
	s.sessions++
	s.mu.Unlock()

	local := s.conn.LocalAddr().(*net.UDPAddr)
	bind := &net.UDPAddr{IP: local.IP, Zone: local.Zone}

	if s.cfg.TestPortMin > 0 && s.cfg.TestPortMax >= s.cfg.TestPortMin {
		span := s.cfg.TestPortMax - s.cfg.TestPortMin + 1
		s.mu.Lock()
		start := s.nextPort
		s.mu.Unlock()
		for i := 0; i < span; i++ {
			port := s.cfg.TestPortMin + (start-s.cfg.TestPortMin+i)%span
			bind.Port = port
			c, err := net.ListenUDP("udp", bind)
			if err == nil {
				s.mu.Lock()
				s.nextPort = port + 1
				if s.nextPort > s.cfg.TestPortMax {
					s.nextPort = s.cfg.TestPortMin
				}
				s.mu.Unlock()
				return c, nil
			}
		}
		s.sessionDone()
		return nil, fmt.Errorf("no free port in test range %d-%d", s.cfg.TestPortMin, s.cfg.TestPortMax)
	}

	bind.Port = 0
	c, err := net.ListenUDP("udp", bind)
	if err != nil {
		s.sessionDone()
		return nil, err
	}
	return c, nil
}

func (s *Server) sessionDone() {
	s.mu.Lock()
	s.sessions--
	s.mu.Unlock()
}
