package udpst

import "encoding/binary"

// Sending rate table constants, matching OB-UDPST (udpst.h, udpst_srates.c).
// Both ends compute the table independently, so the construction must match
// the reference implementation exactly.
const (
	maxSendingRates = 1153
	baseSendTimer1  = 100  // transmitter 1 interval, microseconds
	baseSendTimer2  = 1000 // transmitter 2 interval, microseconds
	maxL3Packet     = 1250
	maxJL3Packet    = 9000
	maxTL3Packet    = 1500
	l3Overhead      = 28 // UDP + IPv4 headers
	ipv6AddSize     = 20
	maxBurstSize    = 100

	maxPayloadSize  = maxL3Packet - l3Overhead  // 1222
	maxJPayloadSize = maxJL3Packet - l3Overhead // 8972
	maxTPayloadSize = maxTL3Packet - l3Overhead // 1472
	minPayloadSize  = loadHdrSize + ipv6AddSize // 52

	// rateRandBit in a payload field means the size is randomized per
	// datagram between the minimum and the remaining bits' value.
	rateRandBit = 0x80000000
)

// Rate is one row of the sending rate table (struct sendingRate): dual
// transmitters plus a once-per-interval add-on datagram.
type Rate struct {
	TxInterval1 uint32 // microseconds; 0 disables transmitter 1
	UDPPayload1 uint32
	BurstSize1  uint32
	TxInterval2 uint32
	UDPPayload2 uint32
	BurstSize2  uint32
	UDPAddon2   uint32
}

func (r Rate) marshal(b []byte) {
	binary.BigEndian.PutUint32(b[0:], r.TxInterval1)
	binary.BigEndian.PutUint32(b[4:], r.UDPPayload1)
	binary.BigEndian.PutUint32(b[8:], r.BurstSize1)
	binary.BigEndian.PutUint32(b[12:], r.TxInterval2)
	binary.BigEndian.PutUint32(b[16:], r.UDPPayload2)
	binary.BigEndian.PutUint32(b[20:], r.BurstSize2)
	binary.BigEndian.PutUint32(b[24:], r.UDPAddon2)
}

func parseRate(b []byte) Rate {
	return Rate{
		TxInterval1: binary.BigEndian.Uint32(b[0:]),
		UDPPayload1: binary.BigEndian.Uint32(b[4:]),
		BurstSize1:  binary.BigEndian.Uint32(b[8:]),
		TxInterval2: binary.BigEndian.Uint32(b[12:]),
		UDPPayload2: binary.BigEndian.Uint32(b[16:]),
		BurstSize2:  binary.BigEndian.Uint32(b[20:]),
		UDPAddon2:   binary.BigEndian.Uint32(b[24:]),
	}
}

// BitsPerSecond returns the nominal L3 (IP layer) sending rate of the row.
// Randomized payloads count at their average size, as in the reference
// implementation's table display.
func (r Rate) BitsPerSecond(ipv6 bool) float64 {
	ipAdd := 0
	if ipv6 {
		ipAdd = ipv6AddSize
	}
	sized := func(v uint32) float64 {
		payload := int(v&^rateRandBit) - ipAdd
		if v&rateRandBit != 0 {
			payload = (minPayloadSize - ipAdd + payload) / 2
		}
		return float64(payload + l3Overhead + ipAdd)
	}
	var bytesPerSec float64
	if r.BurstSize1 > 0 && r.TxInterval1 > 0 {
		bytesPerSec += float64(1e6/r.TxInterval1) * float64(r.BurstSize1) * sized(r.UDPPayload1)
	}
	if r.BurstSize2 > 0 && r.TxInterval2 > 0 {
		bytesPerSec += float64(1e6/r.TxInterval2) * float64(r.BurstSize2) * sized(r.UDPPayload2)
	}
	if r.UDPAddon2 > 0 && r.TxInterval2 > 0 {
		bytesPerSec += float64(1e6/r.TxInterval2) * sized(r.UDPAddon2)
	}
	return bytesPerSec * 8
}

// RateTable is the shared table of sending rates, indexed by the rate
// adjustment algorithms.
type RateTable struct {
	Rows []Rate
	// HighSpeedThresh is the index at which fast ramping ends, nominally
	// the 1 Gbps row.
	HighSpeedThresh int
}

// BuildRateTable constructs the sending rate table exactly as OB-UDPST's
// def_sending_rates does for the given jumbo and traditional-MTU settings.
// The flags must match the client's or the two ends would disagree on every
// row, which is why setup rejects mismatches.
func BuildRateTable(jumbo, traditionalMTU bool) *RateTable {
	t := &RateTable{Rows: make([]Rate, 0, maxSendingRates)}

	var jmax, kmax int
	var payload uint32
	if traditionalMTU {
		jmax, kmax, payload = 11, 8, maxTPayloadSize
	} else {
		jmax, kmax, payload = 9, 10, maxPayloadSize
	}

	// Region 1: up to 1 Gbps (indexes 0..1000).
	stop := false
	for k := 0; k <= kmax && !stop; k++ {
		for i := 0; i < 10 && !stop; i++ {
			var sr Rate
			if k > 0 {
				sr.TxInterval1 = baseSendTimer1
				sr.UDPPayload1 = payload
				sr.BurstSize1 = uint32(k)
			}
			if i > 0 {
				sr.TxInterval2 = baseSendTimer2
				sr.UDPPayload2 = payload
				sr.BurstSize2 = uint32(i)
			}
			if k == 0 && i == 0 {
				// Row 0: a single randomized-size datagram every 50 ms,
				// used as the connectivity keepalive rate.
				sr.TxInterval2 = 50000
				sr.UDPAddon2 = payload | rateRandBit
				t.Rows = append(t.Rows, sr)
			} else if !traditionalMTU && k == kmax {
				t.Rows = append(t.Rows, sr)
				stop = true
				break
			} else {
				t.Rows = append(t.Rows, sr)
			}
			for j := 1; j <= jmax; j++ {
				add := sr
				add.TxInterval2 = baseSendTimer2
				add.UDPAddon2 = uint32((j*1000)/8 - l3Overhead)
				t.Rows = append(t.Rows, add)
				if len(t.Rows) > 1000 {
					stop = true
					break
				}
			}
		}
	}
	t.HighSpeedThresh = len(t.Rows) - 1

	// Region 2: above 1 Gbps. The row count is identical with or without
	// jumbo sizes; only the payload/burst progression differs.
	if jumbo {
		for l3 := maxL3Packet + 125; l3 <= maxJL3Packet; l3 += 125 {
			t.Rows = append(t.Rows, Rate{
				TxInterval1: baseSendTimer1,
				UDPPayload1: uint32(l3 - l3Overhead),
				BurstSize1:  10,
			})
		}
		jmax, payload = 11, maxJPayloadSize
	} else if traditionalMTU {
		jmax, payload = 9, maxTPayloadSize
	} else {
		jmax, payload = 11, maxPayloadSize
	}
	for j := jmax; len(t.Rows) < maxSendingRates; j++ {
		burst := uint32(j)
		if j >= maxBurstSize {
			burst = maxBurstSize
		}
		t.Rows = append(t.Rows, Rate{
			TxInterval1: baseSendTimer1,
			UDPPayload1: payload,
			BurstSize1:  burst,
		})
	}
	return t
}
