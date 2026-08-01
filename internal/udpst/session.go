package udpst

import (
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/netip"
	"os"
	"sync/atomic"
	"time"

	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

// testParams holds the negotiated (server-policed) test parameters.
type testParams struct {
	lowThresh      int // ms
	upperThresh    int // ms
	trialInt       time.Duration
	duration       time.Duration
	subInt         time.Duration
	dscpEcn        uint8
	srIndexConf    int // -1 = auto search
	indexIsStart   bool
	useOwDelVar    bool
	highSpeedDelta int
	slowAdjThresh  int
	seqErrThresh   int
	ignoreOooDup   bool
	randPayload    bool
	algo           uint8
	ecnCEThresh    int
	srAdjSupp      uint16
}

// session is one test connection: it waits for the Test Activation on its own
// socket, then runs the upstream or downstream engine.
type session struct {
	srv        *Server
	conn       *net.UDPConn
	setupPeer  netip.AddrPort
	pver       uint16
	authMode   uint8
	keys       *sessionKeys
	legacyKey  []byte
	bwMbps     int
	bwUpstream bool
	log        *slog.Logger

	peer       netip.AddrPort
	peerAddr   *net.UDPAddr
	ipv6       bool
	downstream bool // server sends the load
	p          testParams
}

// batchIO is the common surface of ipv4.PacketConn and ipv6.PacketConn used
// for sendmmsg/recvmmsg batching (their Message types are identical).
type batchIO interface {
	ReadBatch(ms []ipv4.Message, flags int) (int, error)
	WriteBatch(ms []ipv4.Message, flags int) (int, error)
}

func (s *session) run() {
	defer func() {
		s.conn.Close()
		if s.bwMbps > 0 {
			s.srv.releaseBandwidth(s.bwUpstream, s.bwMbps)
		}
		s.srv.sessionDone()
	}()

	if !s.awaitActivation() {
		s.log.Debug("no test activation before timeout")
		return
	}
	if s.downstream {
		s.runDownstream()
	} else {
		s.runUpstream()
	}
}

// awaitActivation waits for a valid Test Activation Request, polices its
// parameters, and answers with the corrected values (the reference server
// never rejects: it clamps and echoes).
func (s *session) awaitActivation() bool {
	buf := make([]byte, 512)
	deadline := time.Now().Add(watchdogTimeout)
	for {
		s.conn.SetReadDeadline(deadline)
		n, peer, err := s.conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			return false
		}
		if peer.Addr().Unmap() != s.setupPeer.Addr().Unmap() {
			continue
		}
		req, err := parseTestAct(buf[:n])
		if err != nil || req.CmdResponse != TestActRespNone {
			continue
		}
		if s.pver >= ProtocolVer && s.authMode == authModeControl && s.keys != nil {
			if !verifyDigest(s.keys.client[:], buf[:n], testActAuthDigestOff, req.Auth.AuthDigest) {
				s.log.Warn("test activation failed authentication")
				continue
			}
		}

		s.peer = peer
		s.peerAddr = net.UDPAddrFromAddrPort(peer)
		s.ipv6 = !peer.Addr().Unmap().Is4()
		s.downstream = req.CmdRequest == cmdTestActDownstream
		s.police(req)

		resp := *req
		resp.CmdResponse = TestActRespACKOK
		resp.LowThresh = uint16(s.p.lowThresh)
		resp.UpperThresh = uint16(s.p.upperThresh)
		resp.TrialInt = uint16(s.p.trialInt / time.Millisecond)
		resp.TestIntTime = uint16(s.p.duration / time.Second)
		resp.DSCPECN = s.p.dscpEcn
		if s.p.srIndexConf < 0 {
			resp.SRIndexConf = srIndexAuto
		} else {
			resp.SRIndexConf = uint16(s.p.srIndexConf)
		}
		resp.UseOwDelVar = boolByte(s.p.useOwDelVar)
		resp.HighSpeedDelta = uint8(s.p.highSpeedDelta)
		resp.SlowAdjThresh = uint16(s.p.slowAdjThresh)
		resp.SeqErrThresh = uint16(s.p.seqErrThresh)
		resp.IgnoreOooDup = boolByte(s.p.ignoreOooDup)
		resp.RateAdjAlgo = s.p.algo
		resp.ECNCEThresh = uint8(s.p.ecnCEThresh)
		resp.SRAdjSuppCount = s.p.srAdjSupp
		if s.pver >= ProtocolVer {
			resp.SubIntPeriod = uint16(s.p.subInt / time.Millisecond)
		} else {
			resp.LegacySubIntSec = uint8(s.p.subInt / time.Second)
		}
		if !s.downstream {
			// Upstream: the client transmits exactly the row we provide.
			resp.SR = s.srv.table.Rows[s.initialIndex()]
		} else {
			resp.SR = Rate{}
		}

		pdu := resp.marshal()
		if s.pver >= ProtocolVer && s.keys != nil {
			resp.Auth.AuthUnixTime = uint32(time.Now().Unix())
			pdu = resp.marshal()
			signPDU(s.keys.server[:], pdu, testActAuthDigestOff)
		}
		s.conn.WriteToUDPAddrPort(pdu, peer)
		return true
	}
}

// police clamps requested parameters into their valid ranges, replacing
// out-of-range values with defaults exactly as the reference server does.
func (s *session) police(req *TestActPDU) {
	p := &s.p
	lt, ut := int(req.LowThresh), int(req.UpperThresh)
	if lt < 1 || lt > 10000 {
		lt = 30
	}
	if ut < 1 || ut > 10000 {
		ut = 90
	}
	if lt > ut {
		lt, ut = 30, 90
	}
	p.lowThresh, p.upperThresh = lt, ut

	ti := int(req.TrialInt)
	if ti < 5 || ti > 250 {
		ti = 50
	}
	p.trialInt = time.Duration(ti) * time.Millisecond

	dur := int(req.TestIntTime)
	if dur < 5 || dur > 3600 {
		dur = 10
	}
	maxDur := s.srv.cfg.MaxTestSeconds
	if maxDur <= 0 || maxDur > 3600 {
		maxDur = 3600
	}
	if dur > maxDur {
		dur = maxDur
	}
	p.duration = time.Duration(dur) * time.Second

	var subMs int
	if s.pver >= ProtocolVer {
		subMs = int(req.SubIntPeriod)
	} else {
		subMs = int(req.LegacySubIntSec) * 1000
	}
	if subMs < 100 || subMs > 10000 {
		subMs = 1000
	}
	if subMs > dur*1000 {
		subMs = dur * 1000
	}
	p.subInt = time.Duration(subMs) * time.Millisecond

	p.srIndexConf = -1
	if req.SRIndexConf != srIndexAuto && int(req.SRIndexConf) < len(s.srv.table.Rows) {
		p.srIndexConf = int(req.SRIndexConf)
	}
	p.indexIsStart = req.Modifiers&taModStartIndex != 0
	p.randPayload = req.Modifiers&taModRandomPayload != 0
	p.useOwDelVar = req.UseOwDelVar == 1
	p.highSpeedDelta = int(req.HighSpeedDelta)
	if p.highSpeedDelta < 1 {
		p.highSpeedDelta = 10
	}
	p.slowAdjThresh = int(req.SlowAdjThresh)
	if p.slowAdjThresh < 1 {
		p.slowAdjThresh = 3
	}
	p.seqErrThresh = int(req.SeqErrThresh)
	p.ignoreOooDup = req.IgnoreOooDup != 0
	p.algo = req.RateAdjAlgo
	if p.algo > 1 {
		p.algo = 0
	}
	p.ecnCEThresh = int(req.ECNCEThresh)
	if ecn := req.DSCPECN & 0x03; ecn != 1 && ecn != 2 {
		p.ecnCEThresh = 0 // CE thresholding needs ECT(0) or ECT(1) marking
	}
	if s.pver >= ProtocolVer {
		numSub := dur * 1000 / subMs
		if int(req.SRAdjSuppCount) < numSub {
			p.srAdjSupp = req.SRAdjSuppCount
		}
	}

	p.dscpEcn = req.DSCPECN
	if p.dscpEcn != 0 && s.setTrafficClass(int(p.dscpEcn)) != nil {
		p.dscpEcn = 0
	}
}

func (s *session) setTrafficClass(tc int) error {
	if s.ipv6 {
		return ipv6.NewPacketConn(s.conn).SetTrafficClass(tc)
	}
	return ipv4.NewPacketConn(s.conn).SetTOS(tc)
}

func (s *session) newRateController() *rateController {
	return &rateController{
		table:          s.srv.table,
		algo:           s.p.algo,
		index:          s.initialIndex(),
		srIndexConf:    s.p.srIndexConf,
		indexIsStart:   s.p.indexIsStart,
		highSpeedDelta: s.p.highSpeedDelta,
		slowAdjThresh:  s.p.slowAdjThresh,
		seqErrThresh:   s.p.seqErrThresh,
		lowThresh:      s.p.lowThresh,
		upperThresh:    s.p.upperThresh,
		useOwDelVar:    s.p.useOwDelVar,
		ignoreOooDup:   s.p.ignoreOooDup,
		ecnCEThresh:    s.p.ecnCEThresh,
		srAdjSupp:      uint32(s.p.srAdjSupp),
	}
}

func (s *session) initialIndex() int {
	if s.p.srIndexConf >= 0 {
		return s.p.srIndexConf
	}
	return 0
}

func (s *session) batchConn() batchIO {
	if s.ipv6 {
		return ipv6.NewPacketConn(s.conn)
	}
	return ipv4.NewPacketConn(s.conn)
}

func (s *session) l3Overhead() uint32 {
	if s.ipv6 {
		return l3Overhead + ipv6AddSize
	}
	return l3Overhead
}

// subIntervalMbps converts saved sub-interval statistics to an IP-layer rate.
func (s *session) subIntervalMbps(st SubIntStats) float64 {
	if st.DeltaTime == 0 {
		return 0
	}
	bits := (float64(st.RxDatagrams)*float64(s.l3Overhead()) + float64(st.RxBytes)) * 8
	return bits / float64(st.DeltaTime) // bytes over microseconds yields Mbps
}

// runUpstream receives the client's load stream, accounts it, and steers the
// client's sending rate via status feedback messages every trial interval.
func (s *session) runUpstream() {
	rc := s.newRateController()
	now := time.Now()
	testStart := now
	a := newRxAccounting(now)
	br := s.batchConn()

	const batch = 64
	msgs := make([]ipv4.Message, batch)
	for i := range msgs {
		msgs[i].Buffers = [][]byte{make([]byte, maxJPayloadSize+64)}
	}

	var (
		statusSeq  uint32
		lastStatus = now
		nextStatus = now.Add(s.p.trialInt)
		stopAt     = now.Add(s.p.duration + 500*time.Millisecond)
		watchdog   = now.Add(watchdogTimeout)
		lastRx     = now
		stopping   bool
		clientDone bool
		maxMbps    float64
		totalLoss  uint64
		totalDG    uint64
	)

	for !clientDone {
		now = time.Now()
		if now.After(watchdog) {
			s.log.Debug("upstream test watchdog expired")
			break
		}
		if !stopping && now.After(stopAt) {
			stopping = true
		}
		if !now.Before(nextStatus) {
			action := uint8(testActionTest)
			if stopping {
				action = testActionStop2
			}
			rc.adjust(a.trialInput())
			a.maybeRoll(now, s.p.subInt, s.p.trialInt)
			if a.rolled {
				if mbps := s.subIntervalMbps(a.saved); mbps > maxMbps {
					maxMbps = mbps
				}
				a.rolled = false
			}
			st := a.buildStatus(now, lastStatus)
			st.TestAction = action
			st.RxStopped = boolByte(now.Sub(lastRx) > noTrafficWarning)
			statusSeq++
			st.SeqNo = statusSeq
			st.SR = s.srv.table.Rows[rc.index]
			st.AuthMode = s.authMode
			st.legacy = s.pver < ProtocolVer
			totalLoss += uint64(st.SeqErrLoss)
			totalDG += uint64(st.TIRxDatagrams)
			s.conn.WriteToUDPAddrPort(st.marshal(), s.peer)
			a.resetTrial(now)
			lastStatus = now
			nextStatus = nextStatus.Add(s.p.trialInt)
			if nextStatus.Before(now) {
				nextStatus = now.Add(s.p.trialInt)
			}
		}

		deadline := nextStatus
		if watchdog.Before(deadline) {
			deadline = watchdog
		}
		s.conn.SetReadDeadline(deadline)
		n, err := br.ReadBatch(msgs, 0)
		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				continue
			}
			break
		}
		rxNow := time.Now()
		for i := 0; i < n; i++ {
			m := &msgs[i]
			ap, ok := m.Addr.(*net.UDPAddr)
			if !ok || ap.AddrPort().Addr().Unmap() != s.peer.Addr().Unmap() {
				continue
			}
			hdr, err := parseLoadHdr(m.Buffers[0][:min(m.N, loadHdrSize)])
			if err != nil {
				continue
			}
			watchdog = rxNow.Add(watchdogTimeout)
			lastRx = rxNow
			if hdr.TestAction != testActionTest {
				clientDone = true
			}
			a.recordLoad(rxNow, hdr)
		}
	}

	// Fold in the final partial sub-interval.
	a.maybeRoll(time.Now(), 0, 0)
	if mbps := s.subIntervalMbps(a.saved); mbps > maxMbps {
		maxMbps = mbps
	}
	s.logSummary("upstream", testStart, maxMbps, totalDG, totalLoss)
}

// runDownstream generates the load stream, adjusting its rate from the
// client's status feedback.
func (s *session) runDownstream() {
	testStart := time.Now()
	rc := s.newRateController()
	var (
		index   atomic.Int64
		echo    atomic.Pointer[echoState]
		stop    atomic.Bool
		done    = make(chan struct{})
		sendEnd = make(chan struct{})
	)
	index.Store(int64(rc.index))

	go func() {
		defer close(sendEnd)
		s.sendLoop(&index, &echo, &stop, done)
	}()

	stopTimer := time.AfterFunc(s.p.duration+500*time.Millisecond, func() { stop.Store(true) })
	defer stopTimer.Stop()

	var (
		buf        = make([]byte, 512)
		watchdog   = time.Now().Add(watchdogTimeout)
		lastSubSeq uint32
		maxMbps    float64
		totalLoss  uint64
		totalDG    uint64
	)
	for {
		now := time.Now()
		if now.After(watchdog) {
			s.log.Debug("downstream test watchdog expired")
			break
		}
		s.conn.SetReadDeadline(watchdog)
		n, peer, err := s.conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				continue
			}
			break
		}
		if peer.Addr().Unmap() != s.peer.Addr().Unmap() {
			continue
		}
		st, err := parseStatus(buf[:n])
		if err != nil {
			continue
		}
		rxNow := time.Now()
		watchdog = rxNow.Add(watchdogTimeout)
		echo.Store(&echoState{sec: st.TimeSec, nsec: st.TimeNsec, rxAt: rxNow})

		totalLoss += uint64(st.SeqErrLoss)
		totalDG += uint64(st.TIRxDatagrams)
		if st.SubIntSeqNo != lastSubSeq {
			lastSubSeq = st.SubIntSeqNo
			if mbps := s.subIntervalMbps(st.SubIntSaved); mbps > maxMbps {
				maxMbps = mbps
			}
		}
		if st.TestAction != testActionTest {
			break
		}
		rc.adjust(trialInput{
			seqErrLoss:   st.SeqErrLoss,
			seqErrOoo:    st.SeqErrOoo,
			seqErrDup:    st.SeqErrDup,
			delayVarSum:  st.DelayVarSum,
			delayVarCnt:  st.DelayVarCnt,
			rttVarSample: st.RTTVarSample,
			rxDatagrams:  st.TIRxDatagrams,
			ceCount:      st.TIRxCECount,
			subIntSeqNo:  st.SubIntSeqNo,
		})
		index.Store(int64(rc.index))
	}
	close(done)
	<-sendEnd
	s.logSummary("downstream", testStart, maxMbps, totalDG, totalLoss)
}

func (s *session) logSummary(direction string, start time.Time, maxMbps float64, datagrams, loss uint64) {
	if direction == "upstream" {
		s.srv.testsUpstream.Add(1)
	} else {
		s.srv.testsDownstream.Add(1)
	}
	delivered := 100.0
	if datagrams+loss > 0 {
		delivered = float64(datagrams) * 100 / float64(datagrams+loss)
	}
	s.log.Info("test complete",
		"test", "tr471",
		"direction", direction,
		"max_mbps", int(maxMbps),
		"datagrams", datagrams,
		"loss", loss,
		"delivered_pct", fmt.Sprintf("%.2f", delivered),
		"duration_ms", time.Since(start).Milliseconds())
}

// echoState carries the most recent status PDU timestamp for RTT echoing.
type echoState struct {
	sec, nsec uint32
	rxAt      time.Time
}

// sendLoop paces load datagrams per the current sending rate row: bursts on
// two transmitter cadences plus a once-per-interval add-on datagram, sent
// with sendmmsg batching. Missed ticks are caught up in bounded bursts so
// scheduler jitter does not systematically undershoot the nominal rate.
func (s *session) sendLoop(index *atomic.Int64, echo *atomic.Pointer[echoState], stop *atomic.Bool, done chan struct{}) {
	bw := s.batchConn()

	maxSize := maxJPayloadSize
	msgs := make([]ipv4.Message, maxBurstSize+1)
	bufs := make([][]byte, maxBurstSize+1)
	for i := range msgs {
		bufs[i] = make([]byte, maxSize)
		if s.p.randPayload {
			for j := loadHdrSize; j < maxSize; j += 7 {
				bufs[i][j] = byte(j * 31)
			}
		}
		msgs[i].Addr = s.peerAddr
	}

	minSize := minPayloadSize
	if s.ipv6 {
		minSize -= ipv6AddSize
	}

	var seq uint32
	startDelay := time.Duration(5+rand.IntN(46)) * time.Millisecond
	now := time.Now()
	next1 := now.Add(startDelay)
	next2 := now.Add(startDelay)

	sendBurst := func(action uint8, sizeField uint32, burst int, addon uint32) {
		count := 0
		es := echo.Load()
		now := time.Now()
		var respDelay uint16
		var echoSec, echoNsec uint32
		if es != nil {
			echoSec, echoNsec = es.sec, es.nsec
			if d := now.Sub(es.rxAt).Milliseconds(); d >= 0 && d < 65535 {
				respDelay = uint16(d)
			} else {
				respDelay = 65535
			}
		}
		add := func(size uint32) {
			if size < uint32(minSize) {
				size = uint32(minSize)
			}
			if size > uint32(maxSize) {
				size = uint32(maxSize)
			}
			seq++
			hdr := LoadPDU{
				TestAction:   action,
				SeqNo:        seq,
				UDPPayload:   uint16(size),
				SPDUTimeSec:  echoSec,
				SPDUTimeNsec: echoNsec,
				RTTRespDelay: respDelay,
			}
			hdr.TimeSec, hdr.TimeNsec = wallNow()
			hdr.marshalInto(bufs[count])
			msgs[count].Buffers = [][]byte{bufs[count][:size]}
			count++
		}
		size := sizeField &^ rateRandBit
		if s.ipv6 && size > ipv6AddSize {
			size -= ipv6AddSize
		}
		if sizeField&rateRandBit != 0 && size > uint32(minSize) {
			size = uint32(minSize) + rand.Uint32N(size-uint32(minSize)+1)
		}
		for i := 0; i < burst; i++ {
			add(size)
		}
		if addon > 0 {
			asize := addon &^ rateRandBit
			if s.ipv6 && asize > ipv6AddSize {
				asize -= ipv6AddSize
			}
			if addon&rateRandBit != 0 && asize > uint32(minSize) {
				asize = uint32(minSize) + rand.Uint32N(asize-uint32(minSize)+1)
			}
			add(asize)
		}
		if count == 0 {
			return
		}
		sent := 0
		for sent < count {
			n, err := bw.WriteBatch(msgs[sent:count], 0)
			if err != nil || n == 0 {
				// Roll back sequence numbers the kernel did not accept so
				// the receiver does not count them as network loss.
				seq -= uint32(count - sent)
				return
			}
			sent += n
		}
	}

	for {
		select {
		case <-done:
			return
		default:
		}
		if stop.Load() {
			// Stop indication: single datagrams until the client confirms.
			sendBurst(testActionStop2, uint32(minSize), 1, 0)
			select {
			case <-done:
				return
			case <-time.After(50 * time.Millisecond):
			}
			continue
		}

		row := s.srv.table.Rows[index.Load()]
		now = time.Now()

		type due struct {
			at       time.Time
			interval time.Duration
		}
		t1Active := row.TxInterval1 > 0 && row.BurstSize1 > 0
		t2Active := row.TxInterval2 > 0 && (row.BurstSize2 > 0 || row.UDPAddon2 > 0)
		if !t1Active && !t2Active {
			select {
			case <-done:
				return
			case <-time.After(10 * time.Millisecond):
			}
			continue
		}

		wake := time.Time{}
		if t1Active && (wake.IsZero() || next1.Before(wake)) {
			wake = next1
		}
		if t2Active && (wake.IsZero() || next2.Before(wake)) {
			wake = next2
		}
		sleepUntil(wake, done)
		now = time.Now()

		if t1Active {
			iv := time.Duration(row.TxInterval1) * time.Microsecond
			for caught := 0; !next1.After(now) && caught < 20; caught++ {
				sendBurst(testActionTest, row.UDPPayload1, int(row.BurstSize1), 0)
				next1 = next1.Add(iv)
			}
			if next1.Before(now) {
				next1 = now.Add(iv)
			}
		} else {
			next1 = now.Add(10 * time.Millisecond)
		}
		if t2Active {
			iv := time.Duration(row.TxInterval2) * time.Microsecond
			for caught := 0; !next2.After(now) && caught < 20; caught++ {
				sendBurst(testActionTest, row.UDPPayload2, int(row.BurstSize2), row.UDPAddon2)
				next2 = next2.Add(iv)
			}
			if next2.Before(now) {
				next2 = now.Add(iv)
			}
		} else {
			next2 = now.Add(10 * time.Millisecond)
		}
	}
}

// sleepUntil sleeps to just before the target, then spins the remainder, so
// burst timing does not inherit the scheduler's wakeup slack.
func sleepUntil(t time.Time, done chan struct{}) {
	for {
		d := time.Until(t)
		if d <= 0 {
			return
		}
		if d > 500*time.Microsecond {
			select {
			case <-done:
				return
			case <-time.After(d - 200*time.Microsecond):
			}
			continue
		}
		for time.Now().Before(t) {
		}
		return
	}
}

func wallNow() (sec, nsec uint32) {
	t := time.Now()
	return uint32(t.Unix()), uint32(t.Nanosecond())
}

// wallDiffMs returns t minus the wire timestamp, in milliseconds, tolerating
// the 32-bit second truncation.
func wallDiffMs(t time.Time, sec, nsec uint32) int64 {
	ds := int32(uint32(t.Unix()) - sec)
	return int64(ds)*1000 + (int64(t.Nanosecond())-int64(nsec))/1e6
}

func boolByte(b bool) uint8 {
	if b {
		return 1
	}
	return 0
}
