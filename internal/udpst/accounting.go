package udpst

import "time"

// loadHistorySize matches LPDU_HISTORY_SIZE: the ring of recently seen
// sequence numbers used to tell late reordered datagrams from duplicates.
const loadHistorySize = 32

// rxAccounting implements the receiver-side accounting of a test: sequence
// error classification, one-way delay variation with running clock-offset
// correction, sampled RTT, and trial plus sub-interval counters.
type rxAccounting struct {
	seq     uint32
	history [loadHistorySize]uint32
	histIdx int

	clockDeltaMin  int64 // ms; running minimum approximates clock offset
	haveClockDelta bool
	rttMin         uint32
	rttVarSample   uint32
	lastEchoSec    uint32
	lastEchoNsec   uint32
	delayMinUpd    bool

	// Trial interval counters, reset after each status message.
	tRxDatagrams uint32
	tRxBytes     uint32
	tLoss        uint32
	tOoo         uint32
	tDup         uint32
	tDvMin       uint32
	tDvMax       uint32
	tDvSum       uint32
	tDvCnt       uint32
	tCE          uint32

	// Active sub-interval counters.
	sub       SubIntStats
	subCE     uint32
	subStart  time.Time
	accumMs   uint32
	saved     SubIntStats
	savedCE   uint32
	subIntSeq uint32
	rolled    bool
}

func newRxAccounting(now time.Time) *rxAccounting {
	a := &rxAccounting{
		rttMin:       statusNoDelay,
		rttVarSample: statusNoDelay,
		subStart:     now,
	}
	a.tDvMin = statusNoDelay
	a.sub.DelayVarMin = statusNoDelay
	a.sub.RTTVarMin = statusNoDelay
	return a
}

// recordLoad processes one received load PDU.
func (a *rxAccounting) recordLoad(rxTime time.Time, hdr *LoadPDU) {
	a.tRxDatagrams++
	a.tRxBytes += uint32(hdr.UDPPayload)
	a.sub.RxDatagrams++
	a.sub.RxBytes += uint64(hdr.UDPPayload)

	if !a.recordSeq(hdr.SeqNo) {
		return // late datagrams carry no usable delay information
	}

	// One-way delay variation: the running minimum of (receive time minus
	// send time) estimates the clock offset; each sample's excess over the
	// minimum is the variation.
	delta := wallDiffMs(rxTime, hdr.TimeSec, hdr.TimeNsec)
	if !a.haveClockDelta || delta < a.clockDeltaMin {
		a.clockDeltaMin = delta
		a.haveClockDelta = true
		a.delayMinUpd = true
	}
	dv := uint32(delta - a.clockDeltaMin)
	if a.tDvMin == statusNoDelay || dv < a.tDvMin {
		a.tDvMin = dv
	}
	if dv > a.tDvMax {
		a.tDvMax = dv
	}
	a.tDvSum += dv
	a.tDvCnt++
	if a.sub.DelayVarMin == statusNoDelay || dv < a.sub.DelayVarMin {
		a.sub.DelayVarMin = dv
	}
	if dv > a.sub.DelayVarMax {
		a.sub.DelayVarMax = dv
	}
	a.sub.DelayVarSum += dv
	a.sub.DelayVarCnt++

	// Sampled RTT: the load PDU echoes the timestamp of the last status
	// message this receiver sent, plus the sender's response delay.
	if (hdr.SPDUTimeSec != 0 || hdr.SPDUTimeNsec != 0) &&
		(hdr.SPDUTimeSec != a.lastEchoSec || hdr.SPDUTimeNsec != a.lastEchoNsec) {
		a.lastEchoSec, a.lastEchoNsec = hdr.SPDUTimeSec, hdr.SPDUTimeNsec
		rtt := wallDiffMs(rxTime, hdr.SPDUTimeSec, hdr.SPDUTimeNsec) - int64(hdr.RTTRespDelay)
		if rtt < 0 {
			rtt = 0
		}
		r := uint32(rtt)
		if a.rttMin == statusNoDelay || r < a.rttMin {
			a.rttMin = r
		}
		a.rttVarSample = r - a.rttMin
		if a.sub.RTTVarMin == statusNoDelay || a.rttVarSample < a.sub.RTTVarMin {
			a.sub.RTTVarMin = a.rttVarSample
		}
		if a.rttVarSample > a.sub.RTTVarMax {
			a.sub.RTTVarMax = a.rttVarSample
		}
	}
}

// recordSeq classifies the sequence number and returns whether the datagram
// advances the stream (in order, possibly after a gap).
func (a *rxAccounting) recordSeq(seq uint32) bool {
	if seq >= a.seq+1 {
		gap := seq - a.seq - 1
		a.tLoss += gap
		a.sub.SeqErrLoss += gap
		a.seq = seq
		a.pushHistory(seq)
		return true
	}
	if a.inHistory(seq) {
		a.tDup++
		a.sub.SeqErrDup++
		return false
	}
	// A late but unseen datagram is reordering; a previously counted loss
	// is taken back if the correction has not already been reported.
	a.tOoo++
	a.sub.SeqErrOoo++
	if a.tLoss > 0 {
		a.tLoss--
	}
	if a.sub.SeqErrLoss > 0 {
		a.sub.SeqErrLoss--
	}
	a.pushHistory(seq)
	return false
}

func (a *rxAccounting) pushHistory(seq uint32) {
	a.history[a.histIdx] = seq
	a.histIdx = (a.histIdx + 1) % loadHistorySize
}

func (a *rxAccounting) inHistory(seq uint32) bool {
	for _, s := range a.history {
		if s == seq {
			return true
		}
	}
	return false
}

// trialInput snapshots the current trial counters for rate adjustment.
func (a *rxAccounting) trialInput() trialInput {
	return trialInput{
		seqErrLoss:   a.tLoss,
		seqErrOoo:    a.tOoo,
		seqErrDup:    a.tDup,
		delayVarSum:  a.tDvSum,
		delayVarCnt:  a.tDvCnt,
		rttVarSample: a.rttVarSample,
		rxDatagrams:  a.tRxDatagrams,
		ceCount:      a.tCE,
		subIntSeqNo:  a.subIntSeq,
	}
}

// maybeRoll closes the active sub-interval when its period has elapsed
// (within half a trial interval, as the reference implementation does).
// Passing a zero subInt forces a roll for the end of the test.
func (a *rxAccounting) maybeRoll(now time.Time, subInt, trialInt time.Duration) {
	elapsed := now.Sub(a.subStart)
	if subInt > 0 && elapsed <= subInt-trialInt/2 {
		return
	}
	if a.sub.RxDatagrams == 0 && subInt == 0 {
		return // nothing to save in the final partial sub-interval
	}
	a.accumMs += uint32(elapsed.Milliseconds())
	a.sub.DeltaTime = uint32(elapsed.Microseconds())
	a.sub.AccumTime = a.accumMs
	a.saved = a.sub
	a.savedCE = a.subCE
	a.subIntSeq++
	a.rolled = true

	a.sub = SubIntStats{DelayVarMin: statusNoDelay, RTTVarMin: statusNoDelay}
	a.subCE = 0
	a.subStart = now
}

// buildStatus fills a status PDU from the current counters. The caller sets
// the sequence number, test action, rate row, and legacy flag.
func (a *rxAccounting) buildStatus(now, lastStatus time.Time) *StatusPDU {
	st := &StatusPDU{
		SubIntSeqNo:   a.subIntSeq,
		SubIntSaved:   a.saved,
		SeqErrLoss:    a.tLoss,
		SeqErrOoo:     a.tOoo,
		SeqErrDup:     a.tDup,
		ClockDeltaMin: uint32(int32(a.clockDeltaMin)),
		DelayVarMin:   a.tDvMin,
		DelayVarMax:   a.tDvMax,
		DelayVarSum:   a.tDvSum,
		DelayVarCnt:   a.tDvCnt,
		RTTMinimum:    a.rttMin,
		RTTVarSample:  a.rttVarSample,
		DelayMinUpd:   boolByte(a.delayMinUpd),
		TIDeltaTime:   uint32(now.Sub(lastStatus).Microseconds()),
		TIRxDatagrams: a.tRxDatagrams,
		TIRxBytes:     a.tRxBytes,
		TIRxCECount:   a.tCE,
		SavedCECount:  a.savedCE,
	}
	st.TimeSec, st.TimeNsec = wallNow()
	return st
}

// resetTrial clears the trial-interval counters after a status message.
func (a *rxAccounting) resetTrial(now time.Time) {
	a.tRxDatagrams = 0
	a.tRxBytes = 0
	a.tLoss = 0
	a.tOoo = 0
	a.tDup = 0
	a.tDvMin = statusNoDelay
	a.tDvMax = 0
	a.tDvSum = 0
	a.tDvCnt = 0
	a.tCE = 0
	a.delayMinUpd = false
}
