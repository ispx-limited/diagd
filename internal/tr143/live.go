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

// LiveTransfer is one in-flight test as seen from the server side.
type LiveTransfer struct {
	Test      string `json:"test"`
	Ref       string `json:"ref"`
	Peer      string `json:"peer"`
	Bytes     int64  `json:"bytes"`
	ElapsedMS int64  `json:"elapsed_ms"`
}

type liveEntry struct {
	test  string
	ref   string
	peer  string
	start time.Time
	bytes atomic.Int64
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
		delete(r.live, id)
		r.mu.Unlock()
	}
}

func (r *liveRegistry) snapshot() []LiveTransfer {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]LiveTransfer, 0, len(r.live))
	for _, e := range r.live {
		out = append(out, LiveTransfer{
			Test:      e.test,
			Ref:       e.ref,
			Peer:      e.peer,
			Bytes:     e.bytes.Load(),
			ElapsedMS: time.Since(e.start).Milliseconds(),
		})
	}
	return out
}

// Live returns a snapshot of every transfer currently on the wire.
func (h *HTTPHandler) Live() []LiveTransfer {
	return h.liveReg.snapshot()
}
