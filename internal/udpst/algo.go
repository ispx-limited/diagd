package udpst

// rateController runs the TR-471 sending rate search (algorithm B per
// section 5.2.1 and Y.1540 Annex B, or the multiplicative algorithm C of
// Annex B) exactly as OB-UDPST's adjust_sending_rate does.
type rateController struct {
	table *RateTable

	algo           uint8 // 0 = B, 1 = C
	index          int
	srIndexConf    int  // -1 when auto search
	indexIsStart   bool // srIndexConf is a starting index, not fixed
	highSpeedDelta int
	slowAdjThresh  int
	seqErrThresh   int
	lowThresh      int // ms
	upperThresh    int // ms
	useOwDelVar    bool
	ignoreOooDup   bool
	ecnCEThresh    int
	srAdjSupp      uint32

	slowAdjCount int
	retryCount   int // algorithm C
	retryThresh  int
	update       bool
}

// trialInput is the per-trial-interval measurement set feeding the search.
type trialInput struct {
	seqErrLoss   uint32
	seqErrOoo    uint32
	seqErrDup    uint32
	delayVarSum  uint32
	delayVarCnt  uint32
	rttVarSample uint32 // statusNoDelay when absent
	rxDatagrams  uint32
	ceCount      uint32
	subIntSeqNo  uint32
}

// adjust advances the sending rate index for one trial interval.
func (rc *rateController) adjust(in trialInput) {
	seqerr := int(in.seqErrLoss)
	if !rc.ignoreOooDup {
		seqerr += int(in.seqErrOoo + in.seqErrDup)
	}
	delay := rc.lowThresh // no data means "no change"
	if rc.useOwDelVar {
		if in.delayVarCnt > 0 {
			delay = int((in.delayVarSum*10/in.delayVarCnt + 5) / 10)
		}
	} else if in.rttVarSample != statusNoDelay {
		delay = int(in.rttVarSample)
	}
	cethresh := false
	if rc.ecnCEThresh > 0 && in.ceCount > 0 {
		need := (uint32(rc.ecnCEThresh-1)*in.rxDatagrams*10/255 + 5) / 10
		cethresh = in.ceCount > need
	}

	hst := rc.table.HighSpeedThresh
	maxIdx := len(rc.table.Rows) - 1

	if rc.srAdjSupp > 0 && in.subIntSeqNo < rc.srAdjSupp {
		if rc.srIndexConf >= 0 && !rc.indexIsStart {
			rc.index = 0 // static rate is held at zero while suppressed
		}
		return
	}
	if rc.srIndexConf >= 0 && !rc.indexIsStart {
		rc.index = rc.srIndexConf
		return
	}

	good := seqerr <= rc.seqErrThresh && delay < rc.lowThresh && !cethresh
	bad := seqerr > rc.seqErrThresh || delay > rc.upperThresh || cethresh

	if rc.algo == 1 {
		rc.adjustAlgoC(good, bad, hst, maxIdx)
		return
	}
	switch {
	case good:
		if rc.index < hst && rc.slowAdjCount < rc.slowAdjThresh {
			if rc.index+rc.highSpeedDelta > hst {
				rc.index = hst
			} else {
				rc.index += rc.highSpeedDelta
			}
			rc.slowAdjCount = 0
		} else if rc.index < maxIdx {
			rc.index++
		}
	case bad:
		rc.slowAdjCount++
		if rc.index < hst && rc.slowAdjCount == rc.slowAdjThresh {
			if rc.index > rc.highSpeedDelta*3 {
				rc.index -= rc.highSpeedDelta * 3
			} else {
				rc.index = 0
			}
		} else if rc.index > 0 {
			rc.index--
		}
	}
	// Between the thresholds the decision is deferred: no change.
}

func (rc *rateController) adjustAlgoC(good, bad bool, hst, maxIdx int) {
	if rc.retryThresh == 0 {
		rc.retryThresh = 5
	}
	switch {
	case good:
		if rc.index < hst && rc.slowAdjCount < rc.slowAdjThresh {
			if rc.index*2 > hst {
				rc.index = hst
			} else {
				if rc.index == 0 {
					rc.index++
				}
				// Doubling on alternate trials yields an effective 1.5x
				// multiplicative ramp.
				if rc.update {
					rc.index *= 2
					rc.update = false
				} else {
					rc.update = true
				}
			}
			rc.slowAdjCount = 0
		} else {
			if rc.index < maxIdx {
				rc.index++
				rc.retryCount++
			}
			if rc.retryCount >= rc.retryThresh {
				rc.slowAdjCount = 0
				rc.retryCount = 0
				rc.retryThresh += 5 // wait longer before the next fast retry
			}
		}
	case bad:
		rc.slowAdjCount++
		if rc.index < hst && rc.slowAdjCount == rc.slowAdjThresh {
			if rc.index > rc.highSpeedDelta*3 {
				rc.index -= rc.highSpeedDelta * 3
			} else {
				rc.index = 0
			}
		} else if rc.index > 0 {
			rc.index--
			rc.retryCount++
			if rc.retryCount >= rc.retryThresh {
				rc.slowAdjCount = 0
				rc.retryCount = 0
			}
		}
	}
}
