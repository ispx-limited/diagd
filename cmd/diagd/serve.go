package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ispx-limited/diagd/internal/tr143"
)

func cmdServe(args []string) int {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	httpAddr := fs.String("http", ":8080", "TR-143 HTTP listen address, empty to disable")
	echoAddr := fs.String("echo", ":9000", "TR-143 UDP Echo Plus listen address, empty to disable")
	maxTransfers := fs.Int("max-transfers", 64, "maximum concurrent HTTP test transfers, 0 for no limit")
	maxBytes := fs.Int64("max-download-bytes", 0, "maximum size of a generated download in bytes, 0 for no limit")
	maxSeconds := fs.Int("max-download-seconds", 0, "maximum duration of a time-based download, 0 for the TR-143 maximum of 999")
	allowFlag := fs.String("allow", "", "comma-separated CIDRs allowed to run tests, empty to allow all")
	logLevel := fs.String("log-level", "info", "log level: debug, info, warn, error")
	fs.Parse(args)

	log, err := newLogger(*logLevel)
	if err != nil {
		fmt.Fprintln(os.Stderr, "diagd:", err)
		return 2
	}
	allow, err := parseAllowList(*allowFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "diagd:", err)
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errc := make(chan error, 2)

	var httpSrv *http.Server
	if *httpAddr != "" {
		handler := tr143.NewHTTPHandler(tr143.HTTPConfig{
			MaxConcurrent:    *maxTransfers,
			MaxDownloadBytes: *maxBytes,
			MaxDuration:      time.Duration(*maxSeconds) * time.Second,
			Allow:            allow,
			Log:              log,
		})
		httpSrv = &http.Server{
			Addr:              *httpAddr,
			Handler:           handler,
			ReadHeaderTimeout: 10 * time.Second,
		}
		go func() {
			log.Info("tr143 http server listening", "addr", *httpAddr)
			if err := httpSrv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
				errc <- fmt.Errorf("http server: %w", err)
			}
		}()
	}

	var echoConn *net.UDPConn
	if *echoAddr != "" {
		addr, err := net.ResolveUDPAddr("udp", *echoAddr)
		if err != nil {
			fmt.Fprintln(os.Stderr, "diagd:", err)
			return 2
		}
		echoConn, err = net.ListenUDP("udp", addr)
		if err != nil {
			fmt.Fprintln(os.Stderr, "diagd:", err)
			return 2
		}
		echo := tr143.NewEchoServer(echoConn, tr143.EchoConfig{Allow: allow, Log: log})
		go func() {
			log.Info("udp echo plus responder listening", "addr", *echoAddr)
			if err := echo.Serve(); err != nil {
				errc <- fmt.Errorf("echo server: %w", err)
			}
		}()
	}

	if httpSrv == nil && echoConn == nil {
		fmt.Fprintln(os.Stderr, "diagd: all listeners disabled, nothing to serve")
		return 2
	}

	select {
	case <-ctx.Done():
		log.Info("shutting down")
	case err := <-errc:
		log.Error("server failed", "err", err)
		return 1
	}

	if httpSrv != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		httpSrv.Shutdown(shutdownCtx)
	}
	if echoConn != nil {
		echoConn.Close()
	}
	return 0
}

func newLogger(level string) (*slog.Logger, error) {
	var l slog.Level
	if err := l.UnmarshalText([]byte(level)); err != nil {
		return nil, fmt.Errorf("invalid log level %q", level)
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: l})), nil
}

// parseAllowList builds an allow function from comma-separated CIDRs.
// An empty list allows every peer.
func parseAllowList(s string) (tr143.AllowFunc, error) {
	if s == "" {
		return nil, nil
	}
	var prefixes []netip.Prefix
	for _, c := range strings.Split(s, ",") {
		p, err := netip.ParsePrefix(strings.TrimSpace(c))
		if err != nil {
			return nil, fmt.Errorf("invalid allow CIDR %q", c)
		}
		prefixes = append(prefixes, p.Masked())
	}
	return func(a netip.Addr) bool {
		for _, p := range prefixes {
			if p.Contains(a) {
				return true
			}
		}
		return false
	}, nil
}
