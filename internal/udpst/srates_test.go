package udpst

import (
	"math"
	"testing"
)

// The table must reproduce OB-UDPST's def_sending_rates exactly; these
// expectations come from the reference implementation's table dump.
func TestRateTableDefault(t *testing.T) {
	tbl := BuildRateTable(true, false)

	if len(tbl.Rows) != maxSendingRates {
		t.Fatalf("table has %d rows, want %d", len(tbl.Rows), maxSendingRates)
	}
	if tbl.HighSpeedThresh != 1000 {
		t.Errorf("HighSpeedThresh = %d, want 1000", tbl.HighSpeedThresh)
	}

	// Row 0: one randomized datagram every 50 ms.
	r0 := tbl.Rows[0]
	if r0.TxInterval2 != 50000 || r0.UDPAddon2 != maxPayloadSize|rateRandBit ||
		r0.BurstSize1 != 0 || r0.BurstSize2 != 0 {
		t.Errorf("row 0 = %+v", r0)
	}

	// Row 1: 1 Mbps add-on only: (1*1000)/8 - 28 = 97 byte add-on each ms.
	r1 := tbl.Rows[1]
	if r1.UDPAddon2 != 97 || r1.TxInterval2 != 1000 || r1.BurstSize1 != 0 || r1.BurstSize2 != 0 {
		t.Errorf("row 1 = %+v", r1)
	}
	if mbps := r1.BitsPerSecond(false) / 1e6; math.Abs(mbps-1.0) > 0.01 {
		t.Errorf("row 1 rate = %.2f Mbps, want 1.00", mbps)
	}

	// Row 1000: 1 Gbps, transmitter 1 only, burst 10 every 100 us.
	r1000 := tbl.Rows[1000]
	if r1000.TxInterval1 != 100 || r1000.UDPPayload1 != maxPayloadSize || r1000.BurstSize1 != 10 ||
		r1000.TxInterval2 != 0 || r1000.UDPAddon2 != 0 {
		t.Errorf("row 1000 = %+v", r1000)
	}
	if mbps := r1000.BitsPerSecond(false) / 1e6; math.Abs(mbps-1000.0) > 0.5 {
		t.Errorf("row 1000 rate = %.2f Mbps, want 1000", mbps)
	}

	// Rows 1001..1062: jumbo ramp, L3 packet 1375..9000 step 125, burst 10.
	if got := tbl.Rows[1001].UDPPayload1; got != 1375-l3Overhead {
		t.Errorf("row 1001 payload = %d, want %d", got, 1375-l3Overhead)
	}
	if got := tbl.Rows[1062].UDPPayload1; got != maxJPayloadSize {
		t.Errorf("row 1062 payload = %d, want %d", got, maxJPayloadSize)
	}

	// Rows 1063 on: burst grows from 11; final row burst 100 of 8972 bytes,
	// which is the 40 Gbps ceiling.
	if got := tbl.Rows[1063].BurstSize1; got != 11 {
		t.Errorf("row 1063 burst = %d, want 11", got)
	}
	last := tbl.Rows[maxSendingRates-1]
	if last.BurstSize1 != maxBurstSize || last.UDPPayload1 != maxJPayloadSize {
		t.Errorf("last row = %+v", last)
	}
	if gbps := last.BitsPerSecond(false) / 1e9; math.Abs(gbps-72.0) > 0.1 {
		// 100 datagrams of 9000 L3 bytes per 100 us = 72 Gbps nominal.
		t.Errorf("last row rate = %.2f Gbps", gbps)
	}

	// Monotonically non-decreasing through region 1.
	prev := -1.0
	for i := 0; i <= 1000; i++ {
		bps := tbl.Rows[i].BitsPerSecond(false)
		if bps < prev {
			t.Fatalf("rate decreases at row %d: %.0f < %.0f", i, bps, prev)
		}
		prev = bps
	}
}

func TestRateTableVariants(t *testing.T) {
	for _, tc := range []struct {
		jumbo, trad bool
	}{{false, false}, {true, true}, {false, true}} {
		tbl := BuildRateTable(tc.jumbo, tc.trad)
		if len(tbl.Rows) != maxSendingRates {
			t.Errorf("jumbo=%v trad=%v: %d rows, want %d",
				tc.jumbo, tc.trad, len(tbl.Rows), maxSendingRates)
		}
		if tbl.HighSpeedThresh != 1000 {
			t.Errorf("jumbo=%v trad=%v: HighSpeedThresh = %d, want 1000",
				tc.jumbo, tc.trad, tbl.HighSpeedThresh)
		}
	}
}

func TestRateRoundTrip(t *testing.T) {
	r := Rate{100, 8972, 10, 1000, 1222, 5, 97}
	var b [28]byte
	r.marshal(b[:])
	if got := parseRate(b[:]); got != r {
		t.Errorf("round trip: got %+v, want %+v", got, r)
	}
}
