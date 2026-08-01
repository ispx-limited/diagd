package tr143

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func newTestServer(t *testing.T, cfg HTTPConfig) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(NewHTTPHandler(cfg))
	t.Cleanup(srv.Close)
	return srv
}

func TestDownloadSized(t *testing.T) {
	srv := newTestServer(t, HTTPConfig{})
	for _, tc := range []struct {
		path string
		want int64
	}{
		{"/1000000", 1000000},
		{"/500KB.bin", 500000},
		{"/2MB.bin", 2000000},
		{"/download/100kb.txt", 100000},
		{"/1GB", 1000000000},
	} {
		resp, err := http.Get(srv.URL + tc.path)
		if err != nil {
			t.Fatalf("GET %s: %v", tc.path, err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: status %d", tc.path, resp.StatusCode)
		}
		if resp.ContentLength != tc.want {
			t.Errorf("GET %s: Content-Length %d, want %d", tc.path, resp.ContentLength, tc.want)
		}
		n, err := io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if err != nil || n != tc.want {
			t.Errorf("GET %s: read %d bytes (err %v), want %d", tc.path, n, err, tc.want)
		}
	}
}

func TestDownloadServerCloses(t *testing.T) {
	// TR-143 A.4: the client waits for the server-side close after the last
	// byte, so the response must not be reused for keep-alive.
	srv := newTestServer(t, HTTPConfig{})
	resp, err := http.Get(srv.URL + "/1000")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if !resp.Close {
		t.Error("response did not signal Connection: close")
	}
}

func TestDownloadTimed(t *testing.T) {
	srv := newTestServer(t, HTTPConfig{})
	for _, p := range []string{"/dntimebasedmode_1.txt", "/whatever?time=1"} {
		start := time.Now()
		resp, err := http.Get(srv.URL + p)
		if err != nil {
			t.Fatalf("GET %s: %v", p, err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: status %d", p, resp.StatusCode)
		}
		if resp.ContentLength >= 0 {
			t.Errorf("GET %s: timed download has Content-Length %d", p, resp.ContentLength)
		}
		n, err := io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatalf("GET %s: read: %v", p, err)
		}
		if elapsed := time.Since(start); elapsed < time.Second {
			t.Errorf("GET %s: completed in %v, want at least 1s", p, elapsed)
		}
		if n == 0 {
			t.Errorf("GET %s: no data received", p)
		}
	}
}

func TestDownloadTimedBounds(t *testing.T) {
	srv := newTestServer(t, HTTPConfig{MaxDuration: 30 * time.Second})
	for path, want := range map[string]int{
		"/x?time=0":                http.StatusBadRequest,
		"/x?time=1000":             http.StatusBadRequest,
		"/dntimebasedmode_0.txt":   http.StatusBadRequest,
		"/dntimebasedmode_31.txt":  http.StatusForbidden,
		"/dntimebasedmode_abc.txt": http.StatusBadRequest,
	} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("GET %s: status %d, want %d", path, resp.StatusCode, want)
		}
	}
}

func TestDownloadSizeCap(t *testing.T) {
	srv := newTestServer(t, HTTPConfig{MaxDownloadBytes: 1000})
	resp, err := http.Get(srv.URL + "/1001")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status %d, want %d", resp.StatusCode, http.StatusForbidden)
	}
}

func TestNotFound(t *testing.T) {
	srv := newTestServer(t, HTTPConfig{})
	for _, p := range []string{"/nosuchfile.txt", "/dir/other"} {
		resp, err := http.Get(srv.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s: status %d, want 404", p, resp.StatusCode)
		}
	}
}

func TestUpload(t *testing.T) {
	srv := newTestServer(t, HTTPConfig{})
	body := bytes.Repeat([]byte("x"), 5<<20)
	req, err := http.NewRequest(http.MethodPut, srv.URL+"/upload/test.bin", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status %d, want 200", resp.StatusCode)
	}
}

func TestUploadChunked(t *testing.T) {
	srv := newTestServer(t, HTTPConfig{})
	// A reader without a length forces chunked transfer encoding, which
	// TR-143 A.5 allows the client to use.
	req, err := http.NewRequest(http.MethodPut, srv.URL+"/u", io.NopCloser(strings.NewReader(strings.Repeat("y", 1<<20))))
	if err != nil {
		t.Fatal(err)
	}
	req.ContentLength = -1
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status %d, want 200", resp.StatusCode)
	}
}

func TestConcurrencyLimit(t *testing.T) {
	h := NewHTTPHandler(HTTPConfig{MaxConcurrent: 1})
	h.sem <- struct{}{} // occupy the only slot
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/1000", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status %d, want 503", rec.Code)
	}
}

func TestAllowList(t *testing.T) {
	allow := func(a netip.Addr) bool { return a == netip.MustParseAddr("192.0.2.1") }
	srv := newTestServer(t, HTTPConfig{Allow: allow})
	resp, err := http.Get(srv.URL + "/1000")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status %d, want 403 for disallowed peer", resp.StatusCode)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	srv := newTestServer(t, HTTPConfig{})
	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/1000", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status %d, want 405", resp.StatusCode)
	}
}
