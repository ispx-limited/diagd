package tr143

import (
	"sync"
	"sync/atomic"
	"time"
)

// Live transfer registry.
//
// TR-143 clients report their measurement once, at the end. The server
// is the other party to the same TCP stream, so it can observe the
// transfer while it happens: every generated block written, every
// uploaded chunk read. That observation is what /live on the ops
// listener serves, and it is a real measurement of bytes moved on the
// wire — the average rate so far, not an estimate of the client's
// eventual number (which also accounts for its own ROM/BOM windows).
//
// Consumers correlate by ref: the same free-form tag the client puts in
// the test URL's ?ref= parameter and that already stamps the completion
// event. An orchestrator that minted the ref gets exact attribution
// even when many CPEs test at once behind the same NAT.

// LiveTransfer is one test as seen from the server side: in flight, or
// finished within the grace window.
type LiveTransfer struct {
	Test      string `json:"test"`
	Ref       string `json:"ref"`
	Peer      string `json:"peer"`
	Bytes     int64  `json:"bytes"`
	ElapsedMS int64  `json:"elapsed_ms"`
	Done      bool   `json:"done"`
}

// liveGrace is how long a finished transfer stays visible. A poller
// sampling every second or two would otherwise race the transfer's end
// and read an empty list one tick after reading real progress — the
// final figures would exist nowhere except the moment they vanished.
const liveGrace = 15 * time.Second

type liveEntry struct {
	test  string
	ref   string
	peer  string
	start time.Time
	bytes atomic.Int64

	// Set on completion; elapsed freezes here so the reported average
	// stays the transfer's own, not diluted by the grace window.
	done  bool
	ended time.Time
}

type liveRegistry struct {
	mu   sync.Mutex
	seq  uint64
	live map[uint64]*liveEntry
}

// begin registers an in-flight transfer and returns the entry (whose
// byte counter the transfer loop updates) plus its deregistration hook.
func (r *liveRegistry) begin(test, ref, peer string) (*liveEntry, func()) {
	e := &liveEntry{test: test, ref: ref, peer: peer, start: time.Now()}
	r.mu.Lock()
	if r.live == nil {
		r.live = make(map[uint64]*liveEntry)
	}
	r.seq++
	id := r.seq
	r.live[id] = e
	r.mu.Unlock()
	return e, func() {
		r.mu.Lock()
		e.done = true
		e.ended = time.Now()
		r.mu.Unlock()
	}
}

func (r *liveRegistry) snapshot() []LiveTransfer {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]LiveTransfer, 0, len(r.live))
	for id, e := range r.live {
		if e.done && time.Since(e.ended) > liveGrace {
			delete(r.live, id)
			continue
		}
		elapsed := time.Since(e.start)
		if e.done {
			elapsed = e.ended.Sub(e.start)
		}
		out = append(out, LiveTransfer{
			Test:      e.test,
			Ref:       e.ref,
			Peer:      e.peer,
			Bytes:     e.bytes.Load(),
			ElapsedMS: elapsed.Milliseconds(),
			Done:      e.done,
		})
	}
	return out
}

// Live returns a snapshot of every transfer currently on the wire.
func (h *HTTPHandler) Live() []LiveTransfer {
	return h.liveReg.snapshot()
}
