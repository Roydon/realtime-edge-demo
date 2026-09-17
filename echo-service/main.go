// Command echo-service is the stand-in real-time service for this demo stack.
//
// It terminates WebSocket sessions, echoes what it receives, and exports the
// metrics the rest of the stack is built around. It stands in for a media
// server (LiveKit or similar) so the surrounding infrastructure - TLS edge,
// scraping, alerting, deploy pipeline - can be exercised end to end without
// running an SFU.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/realtime-edge-demo/realtime-edge-demo/internal/echo"
	"github.com/realtime-edge-demo/realtime-edge-demo/internal/metrics"
)

// version is stamped at build time with -ldflags. The default marks a binary
// built outside the pipeline, which is itself useful to see on a dashboard.
var version = "dev"

func main() {
	// The container image is distroless and has no shell, so the health probe
	// is the binary itself: `echo-service -healthcheck`. This keeps curl and
	// wget out of the runtime image without giving up a real HEALTHCHECK.
	if len(os.Args) > 1 && os.Args[1] == "-healthcheck" {
		os.Exit(healthcheck())
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

	cfg, err := echo.LoadConfig()
	if err != nil {
		log.Error("invalid configuration", "error", err)
		os.Exit(1)
	}

	srv := echo.NewServer(cfg, log, metrics.NewRegistry(), version)

	httpSrv := &http.Server{
		Addr:    cfg.ListenAddr,
		Handler: srv.Handler(),
		// No WriteTimeout: it would cap the lifetime of every WebSocket
		// session at that value. Idle sessions are bounded by IDLE_TIMEOUT in
		// the read loop instead, which is the correct place for a long-lived
		// connection protocol.
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// Signals are trapped before the listener opens so a SIGTERM arriving
	// during a slow start is still handled as a drain, not a hard kill.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		log.Info("echo-service listening",
			"addr", cfg.ListenAddr,
			"version", version,
			"max_sessions", cfg.MaxSessions,
			"processing_jitter", cfg.ProcessingJitter.String(),
			"error_injection_rate", cfg.ErrorInjectionRate,
		)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		log.Error("listener failed", "error", err)
		os.Exit(1)
	case <-ctx.Done():
	}

	// Shutdown order matters. Readiness drops first so the proxy stops sending
	// new sessions here, then live sessions are closed with a going-away frame
	// so clients reconnect deliberately instead of hitting a dead socket, and
	// only then is the listener closed.
	log.Info("shutdown signal received, draining")
	closed := srv.Drain()
	log.Info("sessions closed", "count", closed)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownGrace)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Error("graceful shutdown timed out, forcing close", "error", err)
		_ = httpSrv.Close()
		os.Exit(1)
	}
	log.Info("shutdown complete")
}

// healthcheck probes this instance's own liveness endpoint and returns a
// process exit code. It deliberately probes /healthz, not /readyz: a draining
// instance is intentionally not ready, and must not be killed for it.
func healthcheck() int {
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}

	client := &http.Client{Timeout: 3 * time.Second}
	// The address is built from this process's own configuration, not from
	// any request input, so there is no untrusted component to the URL.
	resp, err := client.Get("http://" + addr + "/healthz") //nolint:gosec // self-probe against our own listener
	if err != nil {
		return 1
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
