// Package udpst implements the server side of the OB-UDPST protocol
// (control protocol version 20, backward compatible to 11), the de facto
// wire format for TR-471 / RFC 9097 maximum IP-layer capacity tests.
// Existing udpst clients, including those embedded in CPE firmware, can run
// tests against this server unmodified.
package udpst

import (
	"encoding/binary"
	"fmt"
)

// Protocol versions.
const (
	ProtocolVer = 20 // current version
	ProtocolMin = 11 // oldest accepted
)

// PDU identifiers.
const (
	pduIDSetup   = 0xACE1
	pduIDTestAct = 0xACE2
	pduIDNull    = 0xDEAD
	pduIDLoad    = 0xBEEF
	pduIDStatus  = 0xFEED
)

// Wire sizes in bytes.
const (
	setupSize        = 56
	nullSize         = 48
	testActSize      = 104
	testActMinSize   = 60 // legacy layout without the auth trailer
	loadHdrSize      = 32
	statusSize       = 204
	statusLegacySize = 168 // protocol versions 11-19
)

// Command values shared by control PDUs.
const (
	cmdSetupRequest  = 1
	cmdSetupResponse = 2

	cmdTestActUpstream   = 1
	cmdTestActDownstream = 2
)

// Setup Response codes (cmdResponse in the Setup Response).
const (
	SetupRespNone       = 0
	SetupRespACKOK      = 1
	SetupRespBadVersion = 2
	SetupRespBadJumbo   = 3
	SetupRespAuthNC     = 4 // authentication not configured on server
	SetupRespAuthReq    = 5 // authentication required by server
	SetupRespAuthInv    = 6 // invalid authentication mode
	SetupRespAuthFail   = 7
	SetupRespAuthTime   = 8 // authentication time window exceeded
	SetupRespNoMaxBW    = 9 // bandwidth management requires a client -B value
	SetupRespCapExc     = 10
	SetupRespBadTMTU    = 11
	SetupRespMCInvPar   = 12
	SetupRespConnFail   = 13
)

// Test Activation response codes.
const (
	TestActRespNone     = 0
	TestActRespACKOK    = 1
	TestActRespBadParam = 2
)

// testAction values carried by load and status PDUs.
const (
	testActionTest  = 0
	testActionStop1 = 1
	testActionStop2 = 2
)

// Setup Request modifier bits and bandwidth field flags.
const (
	modJumbo          = 0x01
	modTraditionalMTU = 0x02
	bwUpstreamBit     = 0x8000
)

// Test Activation modifier bits.
const (
	taModStartIndex    = 0x01 // srIndexConf is a starting, not fixed, index
	taModRandomPayload = 0x02
)

// srIndexAuto requests the rate search rather than a fixed index.
const srIndexAuto = 0xFFFF

// authTrailer is the 44-byte trailer shared by authenticated control PDUs:
// authMode, authUnixTime, 32-byte HMAC-SHA-256 digest, key ID, reserved,
// checksum.
type authTrailer struct {
	AuthMode     uint8
	AuthUnixTime uint32
	AuthDigest   [32]byte
	KeyID        uint8
}

func (a *authTrailer) marshal(b []byte) {
	// b is positioned at the authMode byte; layout per controlHdrSR/TA.
	b[0] = a.AuthMode
	binary.BigEndian.PutUint32(b[1:], a.AuthUnixTime)
	copy(b[5:37], a.AuthDigest[:])
	b[37] = a.KeyID
	b[38] = 0 // reservedAuth1
	// checksum bytes are written separately
}

func (a *authTrailer) parse(b []byte) {
	a.AuthMode = b[0]
	a.AuthUnixTime = binary.BigEndian.Uint32(b[1:])
	copy(a.AuthDigest[:], b[5:37])
	a.KeyID = b[37]
}

// SetupPDU is the Setup Request/Response (controlHdrSR, 56 bytes).
type SetupPDU struct {
	ProtocolVer  uint16
	MCIndex      uint8
	MCCount      uint8
	MCIdent      uint16
	CmdRequest   uint8
	CmdResponse  uint8
	MaxBandwidth uint16 // Mbps; bit 15 set means upstream
	TestPort     uint16
	Modifiers    uint8
	Auth         authTrailer
}

func parseSetup(b []byte) (*SetupPDU, error) {
	if len(b) != setupSize {
		return nil, fmt.Errorf("setup PDU size %d, want %d", len(b), setupSize)
	}
	if binary.BigEndian.Uint16(b[0:]) != pduIDSetup {
		return nil, fmt.Errorf("bad setup PDU id %#x", binary.BigEndian.Uint16(b[0:]))
	}
	p := &SetupPDU{
		ProtocolVer:  binary.BigEndian.Uint16(b[2:]),
		MCIndex:      b[4],
		MCCount:      b[5],
		MCIdent:      binary.BigEndian.Uint16(b[6:]),
		CmdRequest:   b[8],
		CmdResponse:  b[9],
		MaxBandwidth: binary.BigEndian.Uint16(b[10:]),
		TestPort:     binary.BigEndian.Uint16(b[12:]),
		Modifiers:    b[14],
	}
	if p.CmdRequest != cmdSetupRequest && p.CmdRequest != cmdSetupResponse {
		return nil, fmt.Errorf("bad setup command %d", p.CmdRequest)
	}
	p.Auth.parse(b[15:])
	return p, nil
}

func (p *SetupPDU) marshal() []byte {
	b := make([]byte, setupSize)
	binary.BigEndian.PutUint16(b[0:], pduIDSetup)
	binary.BigEndian.PutUint16(b[2:], p.ProtocolVer)
	b[4] = p.MCIndex
	b[5] = p.MCCount
	binary.BigEndian.PutUint16(b[6:], p.MCIdent)
	b[8] = p.CmdRequest
	b[9] = p.CmdResponse
	binary.BigEndian.PutUint16(b[10:], p.MaxBandwidth)
	binary.BigEndian.PutUint16(b[12:], p.TestPort)
	b[14] = p.Modifiers
	p.Auth.marshal(b[15:])
	return b
}

// setupAuthDigestOff is the byte offset of the HMAC digest in a Setup PDU,
// used to zero it during digest computation.
const setupAuthDigestOff = 20

// NullPDU (controlHdrNR, 48 bytes) opens the server-side firewall pinhole for
// the new test socket; the client silently discards it.
type NullPDU struct {
	ProtocolVer uint16
	Auth        authTrailer
}

func (p *NullPDU) marshal() []byte {
	b := make([]byte, nullSize)
	binary.BigEndian.PutUint16(b[0:], pduIDNull)
	binary.BigEndian.PutUint16(b[2:], p.ProtocolVer)
	b[4] = 1 // cmdRequest: null request
	b[5] = 0 // cmdResponse
	b[6] = 0 // reserved
	p.Auth.marshal(b[7:])
	return b
}

const nullAuthDigestOff = 12

// TestActPDU is the Test Activation Request/Response (controlHdrTA,
// 104 bytes for protocol version 20, 60 bytes for legacy versions).
type TestActPDU struct {
	ProtocolVer     uint16
	CmdRequest      uint8 // 1 upstream, 2 downstream
	CmdResponse     uint8
	LowThresh       uint16
	UpperThresh     uint16
	TrialInt        uint16 // status feedback interval, ms
	TestIntTime     uint16 // test duration, seconds
	LegacySubIntSec uint8  // versions <20: sub-interval period in seconds
	DSCPECN         uint8
	SRIndexConf     uint16
	UseOwDelVar     uint8
	HighSpeedDelta  uint8
	SlowAdjThresh   uint16
	SeqErrThresh    uint16
	IgnoreOooDup    uint8
	Modifiers       uint8
	RateAdjAlgo     uint8
	ECNCEThresh     uint8  // version >=20 (reuses reserved2)
	SR              Rate   // response only: initial rate row for upstream tests
	SubIntPeriod    uint16 // ms, version >=20
	SRAdjSuppCount  uint16 // version >=20 (reuses reserved4)
	Auth            authTrailer

	legacy bool // parsed from a legacy-size PDU; respond in kind
}

func parseTestAct(b []byte) (*TestActPDU, error) {
	if len(b) < testActMinSize || len(b) > testActSize {
		return nil, fmt.Errorf("test activation PDU size %d", len(b))
	}
	if binary.BigEndian.Uint16(b[0:]) != pduIDTestAct {
		return nil, fmt.Errorf("bad test activation PDU id %#x", binary.BigEndian.Uint16(b[0:]))
	}
	p := &TestActPDU{
		ProtocolVer:     binary.BigEndian.Uint16(b[2:]),
		CmdRequest:      b[4],
		CmdResponse:     b[5],
		LowThresh:       binary.BigEndian.Uint16(b[6:]),
		UpperThresh:     binary.BigEndian.Uint16(b[8:]),
		TrialInt:        binary.BigEndian.Uint16(b[10:]),
		TestIntTime:     binary.BigEndian.Uint16(b[12:]),
		LegacySubIntSec: b[14],
		DSCPECN:         b[15],
		SRIndexConf:     binary.BigEndian.Uint16(b[16:]),
		UseOwDelVar:     b[18],
		HighSpeedDelta:  b[19],
		SlowAdjThresh:   binary.BigEndian.Uint16(b[20:]),
		SeqErrThresh:    binary.BigEndian.Uint16(b[22:]),
		IgnoreOooDup:    b[24],
		Modifiers:       b[25],
		RateAdjAlgo:     b[26],
		ECNCEThresh:     b[27],
		SR:              parseRate(b[28:56]),
		SubIntPeriod:    binary.BigEndian.Uint16(b[56:]),
		legacy:          len(b) < testActSize,
	}
	if p.CmdRequest != cmdTestActUpstream && p.CmdRequest != cmdTestActDownstream {
		return nil, fmt.Errorf("bad test activation command %d", p.CmdRequest)
	}
	if !p.legacy {
		p.SRAdjSuppCount = binary.BigEndian.Uint16(b[60:])
		p.Auth.parse(b[63:])
	}
	return p, nil
}

func (p *TestActPDU) marshal() []byte {
	size := testActSize
	if p.legacy {
		size = testActMinSize
	}
	b := make([]byte, size)
	binary.BigEndian.PutUint16(b[0:], pduIDTestAct)
	binary.BigEndian.PutUint16(b[2:], p.ProtocolVer)
	b[4] = p.CmdRequest
	b[5] = p.CmdResponse
	binary.BigEndian.PutUint16(b[6:], p.LowThresh)
	binary.BigEndian.PutUint16(b[8:], p.UpperThresh)
	binary.BigEndian.PutUint16(b[10:], p.TrialInt)
	binary.BigEndian.PutUint16(b[12:], p.TestIntTime)
	b[14] = p.LegacySubIntSec
	b[15] = p.DSCPECN
	binary.BigEndian.PutUint16(b[16:], p.SRIndexConf)
	b[18] = p.UseOwDelVar
	b[19] = p.HighSpeedDelta
	binary.BigEndian.PutUint16(b[20:], p.SlowAdjThresh)
	binary.BigEndian.PutUint16(b[22:], p.SeqErrThresh)
	b[24] = p.IgnoreOooDup
	b[25] = p.Modifiers
	b[26] = p.RateAdjAlgo
	b[27] = p.ECNCEThresh
	p.SR.marshal(b[28:56])
	binary.BigEndian.PutUint16(b[56:], p.SubIntPeriod)
	if !p.legacy {
		binary.BigEndian.PutUint16(b[60:], p.SRAdjSuppCount)
		p.Auth.marshal(b[63:])
	}
	return b
}

const testActAuthDigestOff = 68

// LoadPDU is the 32-byte load datagram header (loadHdr). The datagram is
// padded to the negotiated payload size.
type LoadPDU struct {
	TestAction   uint8
	RxStopped    uint8
	SeqNo        uint32 // starts at 1
	UDPPayload   uint16 // datagram payload size; used for byte accounting
	SPDUSeqErr   uint16
	SPDUTimeSec  uint32 // echo of the last status PDU's send timestamp
	SPDUTimeNsec uint32
	TimeSec      uint32 // this PDU's send timestamp (wall clock)
	TimeNsec     uint32
	RTTRespDelay uint16 // ms between last receive and this send
}

func parseLoadHdr(b []byte) (*LoadPDU, error) {
	if len(b) < loadHdrSize {
		return nil, fmt.Errorf("load PDU size %d", len(b))
	}
	if binary.BigEndian.Uint16(b[0:]) != pduIDLoad {
		return nil, fmt.Errorf("bad load PDU id %#x", binary.BigEndian.Uint16(b[0:]))
	}
	p := &LoadPDU{
		TestAction:   b[2],
		RxStopped:    b[3],
		SeqNo:        binary.BigEndian.Uint32(b[4:]),
		UDPPayload:   binary.BigEndian.Uint16(b[8:]),
		SPDUSeqErr:   binary.BigEndian.Uint16(b[10:]),
		SPDUTimeSec:  binary.BigEndian.Uint32(b[12:]),
		SPDUTimeNsec: binary.BigEndian.Uint32(b[16:]),
		TimeSec:      binary.BigEndian.Uint32(b[20:]),
		TimeNsec:     binary.BigEndian.Uint32(b[24:]),
		RTTRespDelay: binary.BigEndian.Uint16(b[28:]),
	}
	if p.TestAction > testActionStop2 || p.RxStopped > 1 {
		return nil, fmt.Errorf("bad load PDU fields")
	}
	return p, nil
}

// marshalInto writes the header into b, which must be at least loadHdrSize.
func (p *LoadPDU) marshalInto(b []byte) {
	binary.BigEndian.PutUint16(b[0:], pduIDLoad)
	b[2] = p.TestAction
	b[3] = p.RxStopped
	binary.BigEndian.PutUint32(b[4:], p.SeqNo)
	binary.BigEndian.PutUint16(b[8:], p.UDPPayload)
	binary.BigEndian.PutUint16(b[10:], p.SPDUSeqErr)
	binary.BigEndian.PutUint32(b[12:], p.SPDUTimeSec)
	binary.BigEndian.PutUint32(b[16:], p.SPDUTimeNsec)
	binary.BigEndian.PutUint32(b[20:], p.TimeSec)
	binary.BigEndian.PutUint32(b[24:], p.TimeNsec)
	binary.BigEndian.PutUint16(b[28:], p.RTTRespDelay)
	binary.BigEndian.PutUint16(b[30:], 0) // checksum not used
}

// SubIntStats mirrors the packed subIntStats structure (56 bytes) embedded in
// status PDUs: the saved statistics of the most recently completed
// sub-interval.
type SubIntStats struct {
	RxDatagrams uint32
	RxBytes     uint64
	DeltaTime   uint32 // microseconds
	SeqErrLoss  uint32
	SeqErrOoo   uint32
	SeqErrDup   uint32
	DelayVarMin uint32 // ms
	DelayVarMax uint32
	DelayVarSum uint32
	DelayVarCnt uint32
	RTTVarMin   uint32 // ms
	RTTVarMax   uint32
	AccumTime   uint32 // ms of test time at the end of this sub-interval
}

func (s *SubIntStats) marshal(b []byte) {
	binary.BigEndian.PutUint32(b[0:], s.RxDatagrams)
	binary.BigEndian.PutUint64(b[4:], s.RxBytes)
	binary.BigEndian.PutUint32(b[12:], s.DeltaTime)
	binary.BigEndian.PutUint32(b[16:], s.SeqErrLoss)
	binary.BigEndian.PutUint32(b[20:], s.SeqErrOoo)
	binary.BigEndian.PutUint32(b[24:], s.SeqErrDup)
	binary.BigEndian.PutUint32(b[28:], s.DelayVarMin)
	binary.BigEndian.PutUint32(b[32:], s.DelayVarMax)
	binary.BigEndian.PutUint32(b[36:], s.DelayVarSum)
	binary.BigEndian.PutUint32(b[40:], s.DelayVarCnt)
	binary.BigEndian.PutUint32(b[44:], s.RTTVarMin)
	binary.BigEndian.PutUint32(b[48:], s.RTTVarMax)
	binary.BigEndian.PutUint32(b[52:], s.AccumTime)
}

func parseSubIntStats(b []byte) SubIntStats {
	return SubIntStats{
		RxDatagrams: binary.BigEndian.Uint32(b[0:]),
		RxBytes:     binary.BigEndian.Uint64(b[4:]),
		DeltaTime:   binary.BigEndian.Uint32(b[12:]),
		SeqErrLoss:  binary.BigEndian.Uint32(b[16:]),
		SeqErrOoo:   binary.BigEndian.Uint32(b[20:]),
		SeqErrDup:   binary.BigEndian.Uint32(b[24:]),
		DelayVarMin: binary.BigEndian.Uint32(b[28:]),
		DelayVarMax: binary.BigEndian.Uint32(b[32:]),
		DelayVarSum: binary.BigEndian.Uint32(b[36:]),
		DelayVarCnt: binary.BigEndian.Uint32(b[40:]),
		RTTVarMin:   binary.BigEndian.Uint32(b[44:]),
		RTTVarMax:   binary.BigEndian.Uint32(b[48:]),
		AccumTime:   binary.BigEndian.Uint32(b[52:]),
	}
}

// statusNoDelay marks delay fields with no data (STATUS_NODEL).
const statusNoDelay = 0xFFFFFFFF

// StatusPDU is the status feedback message (statusHdr, 204 bytes for
// protocol version 20, 168 bytes for legacy versions).
type StatusPDU struct {
	TestAction    uint8
	RxStopped     uint8
	SeqNo         uint32 // starts at 1
	SR            Rate   // upstream tests: next rate row for the client
	SubIntSeqNo   uint32
	SubIntSaved   SubIntStats
	SeqErrLoss    uint32
	SeqErrOoo     uint32
	SeqErrDup     uint32
	ClockDeltaMin uint32 // ms (signed value stored as uint32)
	DelayVarMin   uint32
	DelayVarMax   uint32
	DelayVarSum   uint32
	DelayVarCnt   uint32
	RTTMinimum    uint32
	RTTVarSample  uint32
	DelayMinUpd   uint8
	TIDeltaTime   uint32 // trial interval elapsed, microseconds
	TIRxDatagrams uint32
	TIRxBytes     uint32
	TimeSec       uint32 // this PDU's send timestamp
	TimeNsec      uint32
	AuthMode      uint8
	TIRxCECount   uint32
	SavedCECount  uint32
	Modifiers     uint8

	legacy bool
}

func (p *StatusPDU) marshal() []byte {
	if p.legacy {
		return p.marshalLegacy()
	}
	b := make([]byte, statusSize)
	binary.BigEndian.PutUint16(b[0:], pduIDStatus)
	b[2] = p.TestAction
	b[3] = p.RxStopped
	binary.BigEndian.PutUint32(b[4:], p.SeqNo)
	p.SR.marshal(b[8:36])
	binary.BigEndian.PutUint32(b[36:], p.SubIntSeqNo)
	p.SubIntSaved.marshal(b[40:96])
	binary.BigEndian.PutUint32(b[96:], p.SeqErrLoss)
	binary.BigEndian.PutUint32(b[100:], p.SeqErrOoo)
	binary.BigEndian.PutUint32(b[104:], p.SeqErrDup)
	binary.BigEndian.PutUint32(b[108:], p.ClockDeltaMin)
	binary.BigEndian.PutUint32(b[112:], p.DelayVarMin)
	binary.BigEndian.PutUint32(b[116:], p.DelayVarMax)
	binary.BigEndian.PutUint32(b[120:], p.DelayVarSum)
	binary.BigEndian.PutUint32(b[124:], p.DelayVarCnt)
	binary.BigEndian.PutUint32(b[128:], p.RTTMinimum)
	binary.BigEndian.PutUint32(b[132:], p.RTTVarSample)
	b[136] = p.DelayMinUpd
	binary.BigEndian.PutUint32(b[140:], p.TIDeltaTime)
	binary.BigEndian.PutUint32(b[144:], p.TIRxDatagrams)
	binary.BigEndian.PutUint32(b[148:], p.TIRxBytes)
	binary.BigEndian.PutUint32(b[152:], p.TimeSec)
	binary.BigEndian.PutUint32(b[156:], p.TimeNsec)
	b[163] = p.AuthMode
	binary.BigEndian.PutUint32(b[176:], p.TIRxCECount)
	binary.BigEndian.PutUint32(b[180:], p.SavedCECount)
	b[185] = p.Modifiers
	return b
}

// marshalLegacy emits the version 11-19 layout: same fields through the
// timestamp but with an unpacked subIntStats (4 pad bytes after RxDatagrams
// and after AccumTime) and no auth trailer.
func (p *StatusPDU) marshalLegacy() []byte {
	b := make([]byte, statusLegacySize)
	binary.BigEndian.PutUint16(b[0:], pduIDStatus)
	b[2] = p.TestAction
	b[3] = p.RxStopped
	binary.BigEndian.PutUint32(b[4:], p.SeqNo)
	p.SR.marshal(b[8:36])
	binary.BigEndian.PutUint32(b[36:], p.SubIntSeqNo)
	// Unpacked subIntStats: rxDatagrams, 4 pad, then the rest as packed,
	// then 4 trailing pad bytes (total 64 bytes).
	binary.BigEndian.PutUint32(b[40:], p.SubIntSaved.RxDatagrams)
	tmp := make([]byte, 56)
	p.SubIntSaved.marshal(tmp)
	copy(b[48:100], tmp[4:56])
	off := 104
	for _, v := range []uint32{p.SeqErrLoss, p.SeqErrOoo, p.SeqErrDup, p.ClockDeltaMin,
		p.DelayVarMin, p.DelayVarMax, p.DelayVarSum, p.DelayVarCnt,
		p.RTTMinimum, p.RTTVarSample} {
		binary.BigEndian.PutUint32(b[off:], v)
		off += 4
	}
	b[144] = p.DelayMinUpd
	binary.BigEndian.PutUint32(b[148:], p.TIDeltaTime)
	binary.BigEndian.PutUint32(b[152:], p.TIRxDatagrams)
	binary.BigEndian.PutUint32(b[156:], p.TIRxBytes)
	binary.BigEndian.PutUint32(b[160:], p.TimeSec)
	binary.BigEndian.PutUint32(b[164:], p.TimeNsec)
	return b
}

func parseStatus(b []byte) (*StatusPDU, error) {
	if len(b) != statusSize && len(b) != statusLegacySize {
		return nil, fmt.Errorf("status PDU size %d", len(b))
	}
	if binary.BigEndian.Uint16(b[0:]) != pduIDStatus {
		return nil, fmt.Errorf("bad status PDU id %#x", binary.BigEndian.Uint16(b[0:]))
	}
	p := &StatusPDU{
		TestAction:  b[2],
		RxStopped:   b[3],
		SeqNo:       binary.BigEndian.Uint32(b[4:]),
		SR:          parseRate(b[8:36]),
		SubIntSeqNo: binary.BigEndian.Uint32(b[36:]),
		legacy:      len(b) == statusLegacySize,
	}
	if p.TestAction > testActionStop2 || p.RxStopped > 1 {
		return nil, fmt.Errorf("bad status PDU fields")
	}
	if p.legacy {
		st := &p.SubIntSaved
		st.RxDatagrams = binary.BigEndian.Uint32(b[40:])
		tmp := make([]byte, 56)
		binary.BigEndian.PutUint32(tmp[0:], st.RxDatagrams)
		copy(tmp[4:56], b[48:100])
		*st = parseSubIntStats(tmp)
		off := 104
		for _, dst := range []*uint32{&p.SeqErrLoss, &p.SeqErrOoo, &p.SeqErrDup, &p.ClockDeltaMin,
			&p.DelayVarMin, &p.DelayVarMax, &p.DelayVarSum, &p.DelayVarCnt,
			&p.RTTMinimum, &p.RTTVarSample} {
			*dst = binary.BigEndian.Uint32(b[off:])
			off += 4
		}
		p.DelayMinUpd = b[144]
		p.TIDeltaTime = binary.BigEndian.Uint32(b[148:])
		p.TIRxDatagrams = binary.BigEndian.Uint32(b[152:])
		p.TIRxBytes = binary.BigEndian.Uint32(b[156:])
		p.TimeSec = binary.BigEndian.Uint32(b[160:])
		p.TimeNsec = binary.BigEndian.Uint32(b[164:])
		return p, nil
	}
	p.SubIntSaved = parseSubIntStats(b[40:96])
	p.SeqErrLoss = binary.BigEndian.Uint32(b[96:])
	p.SeqErrOoo = binary.BigEndian.Uint32(b[100:])
	p.SeqErrDup = binary.BigEndian.Uint32(b[104:])
	p.ClockDeltaMin = binary.BigEndian.Uint32(b[108:])
	p.DelayVarMin = binary.BigEndian.Uint32(b[112:])
	p.DelayVarMax = binary.BigEndian.Uint32(b[116:])
	p.DelayVarSum = binary.BigEndian.Uint32(b[120:])
	p.DelayVarCnt = binary.BigEndian.Uint32(b[124:])
	p.RTTMinimum = binary.BigEndian.Uint32(b[128:])
	p.RTTVarSample = binary.BigEndian.Uint32(b[132:])
	p.DelayMinUpd = b[136]
	p.TIDeltaTime = binary.BigEndian.Uint32(b[140:])
	p.TIRxDatagrams = binary.BigEndian.Uint32(b[144:])
	p.TIRxBytes = binary.BigEndian.Uint32(b[148:])
	p.TimeSec = binary.BigEndian.Uint32(b[152:])
	p.TimeNsec = binary.BigEndian.Uint32(b[156:])
	p.AuthMode = b[163]
	p.TIRxCECount = binary.BigEndian.Uint32(b[176:])
	p.SavedCECount = binary.BigEndian.Uint32(b[180:])
	p.Modifiers = b[185]
	return p, nil
}
