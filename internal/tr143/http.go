package tr143

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// TR-143 Section 4.3: TimeBasedTestDuration is bounded to 999 seconds.
const maxTimeBasedSeconds = 999

// HTTPConfig configures the TR-143 HTTP download/upload endpoints.
type HTTPConfig struct {
	// MaxConcurrent bounds simultaneous test transfers across downloads and
	// uploads. A multi-connection test (TR-143 Section 4.4) consumes one slot
	// per TCP connection. 0 means no limit.
	MaxConcurrent int

	// MaxDownloadBytes caps the size of a single generated download.
	// 0 means no cap.
	MaxDownloadBytes int64

	// MaxDuration caps time-based downloads below the spec maximum of 999
	// seconds. 0 applies the spec maximum only.
	MaxDuration time.Duration

	// Allow restricts which peers may run tests. nil permits all peers.
	Allow AllowFunc

	Log *slog.Logger
}

// HTTPHandler serves TR-143 DownloadDiagnostics and UploadDiagnostics
// (Appendix A.4/A.5): generated downloads by size or duration, and uploads
// of arbitrary length that are read and discarded.
type HTTPHandler struct {
	cfg HTTPConfig
	log *slog.Logger
	sem chan struct{}

	downloads     atomic.Uint64
	uploads       atomic.Uint64
	rejects       atomic.Uint64
	bytesSent     atomic.Uint64
	bytesReceived atomic.Uint64
	active        atomic.Int64
}

// HTTPStats is a snapshot of transfer counters.
type HTTPStats struct {
	Downloads       uint64
	Uploads         uint64
	Rejects         uint64
	BytesSent       uint64
	BytesReceived   uint64
	ActiveTransfers int64
}

// Stats returns a snapshot of the handler's counters.
func (h *HTTPHandler) Stats() HTTPStats {
	return HTTPStats{
		Downloads:       h.downloads.Load(),
		Uploads:         h.uploads.Load(),
		Rejects:         h.rejects.Load(),
		BytesSent:       h.bytesSent.Load(),
		BytesReceived:   h.bytesReceived.Load(),
		ActiveTransfers: h.active.Load(),
	}
}

// NewHTTPHandler returns a handler implementing the TR-143 HTTP server side.
func NewHTTPHandler(cfg HTTPConfig) *HTTPHandler {
	h := &HTTPHandler{cfg: cfg, log: cfg.Log}
	if h.log == nil {
		h.log = slog.Default()
	}
	if cfg.MaxConcurrent > 0 {
		h.sem = make(chan struct{}, cfg.MaxConcurrent)
	}
	return h
}

func (h *HTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !h.peerAllowed(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		h.download(w, r)
	case http.MethodPut, http.MethodPost:
		h.upload(w, r)
	default:
		w.Header().Set("Allow", "GET, HEAD, PUT, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *HTTPHandler) peerAllowed(r *http.Request) bool {
	if h.cfg.Allow == nil {
		return true
	}
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	return h.cfg.Allow(ap.Addr().Unmap())
}

// acquire reserves a transfer slot, or fails the request with 503 when the
// server is at its concurrent test limit (admission control; TR-143 Section 4
// notes that concurrent tests skew each other's results).
func (h *HTTPHandler) acquire(w http.ResponseWriter, r *http.Request) bool {
	if h.sem != nil {
		select {
		case h.sem <- struct{}{}:
		default:
			h.rejects.Add(1)
			h.log.Warn("test rejected",
				"test", "http", "reason", "capacity", "peer", r.RemoteAddr, "ref", r.URL.Query().Get("ref"))
			http.Error(w, "test capacity exceeded", http.StatusServiceUnavailable)
			return false
		}
	}
	h.active.Add(1)
	return true
}

func (h *HTTPHandler) release() {
	h.active.Add(-1)
	if h.sem != nil {
		<-h.sem
	}
}

// event emits the per-test completion record. The "ref" field carries the
// correlation token the operator may embed in the provisioned URL as a
// "ref" query parameter.
func (h *HTTPHandler) event(r *http.Request, test string, complete bool, bytes int64, start time.Time) {
	msg := "test complete"
	if !complete {
		// Client-side teardown is the normal end of time-based tests and
		// of tests that hit their CPE-side timeout; record, do not alarm.
		msg = "test ended by client"
	}
	h.log.Info(msg,
		"test", test,
		"peer", r.RemoteAddr,
		"ref", r.URL.Query().Get("ref"),
		"bytes", bytes,
		"duration_ms", time.Since(start).Milliseconds())
}

func (h *HTTPHandler) download(w http.ResponseWriter, r *http.Request) {
	req, err := parseDownloadRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req == nil {
		if r.URL.Path == "/" {
			h.index(w)
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	if req.seconds > 0 {
		h.downloadTimed(w, r, req.seconds)
		return
	}
	h.downloadSized(w, r, req.bytes)
}

type downloadRequest struct {
	bytes   int64 // sized download; exact byte count
	seconds int   // time-based download; minimum duration
}

// parseDownloadRequest maps a request URL to a generated download.
//
// Time-based conventions come from TR-143 Section 4.3 / Appendix A.6: a
// "time" query parameter on any path, or a filename of the form
// "dntimebasedmode_<seconds>.txt". Sized downloads use a filename that is a
// byte count with an optional decimal unit suffix, for example /1000000,
// /100MB.bin or /1GB.
//
// A nil result with nil error means the URL names no test resource.
func parseDownloadRequest(r *http.Request) (*downloadRequest, error) {
	if v := r.URL.Query().Get("time"); v != "" {
		secs, err := strconv.Atoi(v)
		if err != nil || secs < 1 || secs > maxTimeBasedSeconds {
			return nil, fmt.Errorf("time must be 1..%d seconds", maxTimeBasedSeconds)
		}
		return &downloadRequest{seconds: secs}, nil
	}

	name := path.Base(r.URL.Path)
	if rest, ok := strings.CutPrefix(name, "dntimebasedmode_"); ok {
		rest = strings.TrimSuffix(rest, path.Ext(rest))
		secs, err := strconv.Atoi(rest)
		if err != nil || secs < 1 || secs > maxTimeBasedSeconds {
			return nil, fmt.Errorf("duration must be 1..%d seconds", maxTimeBasedSeconds)
		}
		return &downloadRequest{seconds: secs}, nil
	}

	if size, ok := parseSizeName(name); ok {
		return &downloadRequest{bytes: size}, nil
	}
	return nil, nil
}

var sizeNameRE = regexp.MustCompile(`^([0-9]+)(?:([kKmMgG])[bB])?(?:\.[A-Za-z0-9]+)?$`)

// parseSizeName parses filenames like "1000000", "500KB.bin" or "1GB" into a
// byte count. Unit suffixes are decimal (KB=1e3, MB=1e6, GB=1e9), matching
// how access speeds are provisioned.
func parseSizeName(name string) (int64, bool) {
	m := sizeNameRE.FindStringSubmatch(name)
	if m == nil {
		return 0, false
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return 0, false
	}
	switch strings.ToLower(m[2]) {
	case "k":
		n *= 1e3
	case "m":
		n *= 1e6
	case "g":
		n *= 1e9
	}
	return n, true
}

// downloadSized streams exactly size generated bytes with a Content-Length.
// Per TR-143 A.4 the server closes the connection after the last byte; the
// Connection header makes the Go HTTP server send the FIN the client waits
// for before computing EOMTime.
func (h *HTTPHandler) downloadSized(w http.ResponseWriter, r *http.Request, size int64) {
	if h.cfg.MaxDownloadBytes > 0 && size > h.cfg.MaxDownloadBytes {
		http.Error(w, "requested size exceeds server limit", http.StatusForbidden)
		return
	}
	if !h.acquire(w, r) {
		return
	}
	defer h.release()

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.Header().Set("Connection", "close")
	if r.Method == http.MethodHead {
		return
	}

	start := time.Now()
	var sent int64
	defer func() {
		h.downloads.Add(1)
		h.bytesSent.Add(uint64(sent))
		h.event(r, "http_download", sent == size, sent, start)
	}()
	for sent < size {
		n := int64(len(payloadBlock))
		if remaining := size - sent; remaining < n {
			n = remaining
		}
		wrote, err := w.Write(payloadBlock[:n])
		sent += int64(wrote)
		if err != nil {
			// Client teardown mid-transfer is normal (timeouts, aborted
			// tests); not a server error.
			return
		}
	}
}

// downloadTimed streams generated data for at least the requested duration
// (TR-143 Section 4.3). No Content-Length is sent; the client either counts
// the duration itself and resets the connection (generic mode, A.6) or reads
// until the server closes after the minimum duration has elapsed.
func (h *HTTPHandler) downloadTimed(w http.ResponseWriter, r *http.Request, seconds int) {
	d := time.Duration(seconds) * time.Second
	if h.cfg.MaxDuration > 0 && d > h.cfg.MaxDuration {
		http.Error(w, "requested duration exceeds server limit", http.StatusForbidden)
		return
	}
	if !h.acquire(w, r) {
		return
	}
	defer h.release()

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Connection", "close")
	if r.Method == http.MethodHead {
		return
	}

	start := time.Now()
	deadline := start.Add(d)
	var sent int64
	complete := true
	for time.Now().Before(deadline) {
		n, err := w.Write(payloadBlock)
		sent += int64(n)
		if err != nil {
			// Expected end of a generic-mode time-based test: the client
			// resets the connection when its duration elapses (A.6).
			complete = false
			break
		}
	}
	h.downloads.Add(1)
	h.bytesSent.Add(uint64(sent))
	h.event(r, "http_download_timed", complete, sent, start)
}

// upload accepts a test upload of arbitrary length, discards the body, and
// returns 200 only after the full body has been received; the 200 sets the
// client's EOMTime (TR-143 A.5). Time-based uploads end with the client
// closing the connection mid-body, which needs no response.
func (h *HTTPHandler) upload(w http.ResponseWriter, r *http.Request) {
	if !h.acquire(w, r) {
		return
	}
	defer h.release()

	start := time.Now()
	n, err := io.Copy(io.Discard, r.Body)
	h.uploads.Add(1)
	h.bytesReceived.Add(uint64(n))
	if err != nil {
		h.event(r, "http_upload", false, n, start)
		return
	}
	w.WriteHeader(http.StatusOK)
	h.event(r, "http_upload", true, n, start)
}

func (h *HTTPHandler) index(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	io.WriteString(w, `diagd TR-143 test server

Download (GET):
  /<bytes>            exact size, for example /1000000
  /<n>KB.bin          decimal units KB, MB, GB
  /dntimebasedmode_<seconds>.txt
  /any/path?time=<seconds>

Upload (PUT or POST): any path, body is discarded.
`)
}
