package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ispx-limited/diagd/internal/tr143"
	"github.com/ispx-limited/diagd/internal/udpst"
)

// opsState exposes operational endpoints for one diagd instance:
// /metrics in Prometheus text format and /healthz as JSON. These are the
// signals a steering layer (BGP anycast health checker, DNS controller,
// load balancer) uses to admit or drain an instance.
type opsState struct {
	instance string
	version  string
	started  time.Time
	httpH    *tr143.HTTPHandler
	echo     *tr143.EchoServer
	tr471    *udpst.Server
}

func (o *opsState) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", o.metrics)
	mux.HandleFunc("GET /healthz", o.healthz)
	return mux
}

func (o *opsState) metrics(w http.ResponseWriter, r *http.Request) {
	var b strings.Builder
	counter := func(name, help string, v uint64) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s counter\n%s %d\n", name, help, name, name, v)
	}
	gauge := func(name, help string, v int64) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s gauge\n%s %d\n", name, help, name, name, v)
	}

	fmt.Fprintf(&b, "# HELP diagd_build_info Build information.\n# TYPE diagd_build_info gauge\n")
	fmt.Fprintf(&b, "diagd_build_info{version=%q,instance=%q} 1\n", o.version, o.instance)
	gauge("diagd_uptime_seconds", "Seconds since the instance started.",
		int64(time.Since(o.started).Seconds()))

	if o.httpH != nil {
		st := o.httpH.Stats()
		counter("diagd_http_downloads_total", "TR-143 download tests served.", st.Downloads)
		counter("diagd_http_uploads_total", "TR-143 upload tests served.", st.Uploads)
		counter("diagd_http_rejects_total", "TR-143 HTTP tests rejected by admission control.", st.Rejects)
		counter("diagd_http_bytes_sent_total", "Test payload bytes sent to clients.", st.BytesSent)
		counter("diagd_http_bytes_received_total", "Test payload bytes received from clients.", st.BytesReceived)
		gauge("diagd_http_active_transfers", "TR-143 HTTP transfers in progress.", st.ActiveTransfers)
	}
	if o.echo != nil {
		st := o.echo.Stats()
		counter("diagd_echo_packets_received_total", "UDP echo requests received.", st.PacketsReceived)
		counter("diagd_echo_responses_total", "UDP Echo Plus responses sent.", uint64(st.Responses))
		counter("diagd_echo_failures_total", "Echo requests dropped by overload (TestRespReplyFailureCount).", uint64(st.Failures))
	}
	if o.tr471 != nil {
		st := o.tr471.Stats()
		gauge("diagd_tr471_active_sessions", "TR-471 test sessions in progress.", int64(st.ActiveSessions))
		fmt.Fprintf(&b, "# HELP diagd_tr471_allocated_mbps Bandwidth allocated to admitted tests.\n# TYPE diagd_tr471_allocated_mbps gauge\n")
		fmt.Fprintf(&b, "diagd_tr471_allocated_mbps{direction=\"upstream\"} %d\n", st.UpstreamMbpsAllocated)
		fmt.Fprintf(&b, "diagd_tr471_allocated_mbps{direction=\"downstream\"} %d\n", st.DownstreamMbpsAllocated)
		fmt.Fprintf(&b, "# HELP diagd_tr471_tests_total Completed TR-471 tests.\n# TYPE diagd_tr471_tests_total counter\n")
		fmt.Fprintf(&b, "diagd_tr471_tests_total{direction=\"upstream\"} %d\n", st.TestsUpstream)
		fmt.Fprintf(&b, "diagd_tr471_tests_total{direction=\"downstream\"} %d\n", st.TestsDownstream)
		counter("diagd_tr471_setup_accepts_total", "TR-471 setup requests accepted.", st.SetupAccepts)
		counter("diagd_tr471_setup_rejects_total", "TR-471 setup requests rejected.", st.SetupRejects)
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Write([]byte(b.String()))
}

func (o *opsState) healthz(w http.ResponseWriter, r *http.Request) {
	resp := map[string]any{
		"status":         "ok",
		"instance":       o.instance,
		"version":        o.version,
		"uptime_seconds": int64(time.Since(o.started).Seconds()),
	}
	if o.httpH != nil {
		resp["http_active_transfers"] = o.httpH.Stats().ActiveTransfers
	}
	if o.tr471 != nil {
		st := o.tr471.Stats()
		resp["tr471_active_sessions"] = st.ActiveSessions
		resp["tr471_allocated_mbps_upstream"] = st.UpstreamMbpsAllocated
		resp["tr471_allocated_mbps_downstream"] = st.DownstreamMbpsAllocated
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
