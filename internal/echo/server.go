// Package echo implements the stand-in real-time service: a WebSocket echo
// endpoint instrumented the way a media service would be, plus the health and
// metrics endpoints the surrounding infrastructure depends on.
package echo

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/realtime-edge-demo/realtime-edge-demo/internal/metrics"
)

// Server owns the HTTP surface and the set of live WebSocket sessions.
type Server struct {
	cfg     Config
	log     *slog.Logger
	metrics *metrics.Set
	reg     *prometheus.Registry
	version string

	// ready gates /readyz. It is cleared at the start of shutdown so the load
	// balancer stops sending new sessions here while existing ones drain.
	ready atomic.Bool

	mu       sync.Mutex
	sessions map[*websocket.Conn]struct{}
	closed   bool
}

// NewServer constructs a Server. The registry is supplied by the caller so
// tests can assert on metrics without touching a global.
func NewServer(cfg Config, log *slog.Logger, reg *prometheus.Registry, version string) *Server {
	s := &Server{
		cfg:      cfg,
		log:      log,
		metrics:  metrics.New(reg, version),
		reg:      reg,
		version:  version,
		sessions: make(map[*websocket.Conn]struct{}),
	}
	s.ready.Store(true)
	return s
}

// Handler returns the service's HTTP routes.
//
// /metrics is served from the same listener as the application traffic for the
// demo. In production it would move to a separate port bound to the private
// interface, so scraping never shares a socket with user traffic - see
// docs/DECISIONS.md.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", s.handleWS)
	mux.HandleFunc("/api/echo", s.handleHTTPEcho)
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/readyz", s.handleReadyz)
	mux.Handle("/metrics", promhttp.HandlerFor(s.reg, promhttp.HandlerOpts{}))
	return mux
}

// handleHealthz reports process liveness only. It stays 200 during draining:
// a draining process is unhealthy to route to but must not be restart-looped
// by the supervisor while it is still closing sessions cleanly.
func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "version": s.version})
}

// handleReadyz reports whether this instance should receive new sessions.
func (s *Server) handleReadyz(w http.ResponseWriter, _ *http.Request) {
	if !s.ready.Load() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "draining"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":   "ready",
		"sessions": s.sessionCount(),
	})
}

type echoRequest struct {
	Message string `json:"message"`
}

type echoResponse struct {
	Message    string  `json:"message"`
	ServedAt   string  `json:"served_at"`
	LatencyMs  float64 `json:"latency_ms"`
	ServerName string  `json:"server"`
}

// handleHTTPEcho mirrors the WebSocket path over plain HTTP. It exists so the
// stack can be smoke-tested with curl and so the dashboards have a non-
// WebSocket latency series to compare against.
func (s *Server) handleHTTPEcho(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.metrics.ErrorsTotal.WithLabelValues(metrics.TransportHTTP, metrics.CauseBadRequest).Inc()
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "POST only"})
		return
	}

	start := time.Now()
	var req echoRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, s.cfg.ReadLimitBytes)).Decode(&req); err != nil {
		s.metrics.ErrorsTotal.WithLabelValues(metrics.TransportHTTP, metrics.CauseBadRequest).Inc()
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
		return
	}
	s.metrics.MessagesTotal.WithLabelValues(metrics.TransportHTTP, "in").Inc()

	s.simulateProcessing()

	if s.shouldInjectError() {
		s.metrics.ErrorsTotal.WithLabelValues(metrics.TransportHTTP, metrics.CauseInjected).Inc()
		s.metrics.MessageLatency.WithLabelValues(metrics.TransportHTTP).Observe(time.Since(start).Seconds())
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "injected failure"})
		return
	}

	elapsed := time.Since(start)
	s.metrics.MessageLatency.WithLabelValues(metrics.TransportHTTP).Observe(elapsed.Seconds())
	s.metrics.MessagesTotal.WithLabelValues(metrics.TransportHTTP, "out").Inc()
	writeJSON(w, http.StatusOK, echoResponse{
		Message:   req.Message,
		ServedAt:  time.Now().UTC().Format(time.RFC3339Nano),
		LatencyMs: float64(elapsed.Microseconds()) / 1000,
	})
}

// handleWS upgrades the connection and runs the echo loop for its lifetime.
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	if !s.ready.Load() {
		s.metrics.ErrorsTotal.WithLabelValues(metrics.TransportWS, metrics.CauseOverloaded).Inc()
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "draining"})
		return
	}
	if s.sessionCount() >= s.cfg.MaxSessions {
		// Shedding load at a known ceiling beats discovering the real one
		// during an incident. The ceiling is configuration, not a guess.
		s.metrics.ErrorsTotal.WithLabelValues(metrics.TransportWS, metrics.CauseOverloaded).Inc()
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "at session capacity"})
		return
	}

	// InsecureSkipVerify disables same-origin checking. That is correct for
	// this demo, where the client is a load generator with no browser origin,
	// and wrong for a browser-facing deployment - there, OriginPatterns must
	// list the real front-end hosts.
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
		CompressionMode:    websocket.CompressionDisabled,
	})
	if err != nil {
		s.metrics.ErrorsTotal.WithLabelValues(metrics.TransportWS, metrics.CauseRead).Inc()
		s.log.Warn("websocket upgrade failed", "error", err)
		return
	}
	conn.SetReadLimit(s.cfg.ReadLimitBytes)

	if !s.trackSession(conn) {
		_ = conn.Close(websocket.StatusGoingAway, "server shutting down")
		return
	}
	s.metrics.SessionsTotal.Inc()
	s.metrics.ActiveSessions.Set(float64(s.sessionCount()))

	reason := s.runSession(r.Context(), conn)

	s.untrackSession(conn)
	s.metrics.ActiveSessions.Set(float64(s.sessionCount()))
	s.metrics.DisconnectsTotal.WithLabelValues(reason).Inc()
}

// runSession reads and echoes until the peer goes away or the session errors,
// returning the disconnect reason to record.
func (s *Server) runSession(ctx context.Context, conn *websocket.Conn) string {
	for {
		readCtx, cancel := context.WithTimeout(ctx, s.cfg.IdleTimeout)
		typ, data, err := conn.Read(readCtx)
		cancel()

		if err != nil {
			return s.classifyReadError(ctx, err)
		}

		start := time.Now()
		s.metrics.MessagesTotal.WithLabelValues(metrics.TransportWS, "in").Inc()

		s.simulateProcessing()

		if s.shouldInjectError() {
			s.metrics.ErrorsTotal.WithLabelValues(metrics.TransportWS, metrics.CauseInjected).Inc()
			s.metrics.MessageLatency.WithLabelValues(metrics.TransportWS).Observe(time.Since(start).Seconds())
			_ = conn.Close(websocket.StatusInternalError, "injected failure")
			return metrics.ReasonError
		}

		writeCtx, cancelWrite := context.WithTimeout(ctx, 10*time.Second)
		err = conn.Write(writeCtx, typ, data)
		cancelWrite()
		if err != nil {
			s.metrics.ErrorsTotal.WithLabelValues(metrics.TransportWS, metrics.CauseWrite).Inc()
			return metrics.ReasonError
		}

		s.metrics.MessageLatency.WithLabelValues(metrics.TransportWS).Observe(time.Since(start).Seconds())
		s.metrics.MessagesTotal.WithLabelValues(metrics.TransportWS, "out").Inc()
	}
}

// classifyReadError turns a read failure into a disconnect reason. A clean
// peer close and an idle timeout are normal operation; everything else is an
// error worth counting, because only that class should drive an alert.
func (s *Server) classifyReadError(ctx context.Context, err error) string {
	status := websocket.CloseStatus(err)
	switch status {
	case websocket.StatusNormalClosure, websocket.StatusGoingAway:
		return metrics.ReasonClient
	}

	// The parent context is cancelled when the HTTP server shuts down, which
	// is a drain rather than a fault.
	if ctx.Err() != nil {
		return metrics.ReasonShutdown
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return metrics.ReasonIdle
	}
	if status != -1 {
		// Any other explicit close code is still the client leaving.
		return metrics.ReasonClient
	}

	s.metrics.ErrorsTotal.WithLabelValues(metrics.TransportWS, metrics.CauseRead).Inc()
	return metrics.ReasonError
}

// simulateProcessing spends a demo-only slice of time so the latency histogram
// has a distribution instead of a single bucket. The occasional multiplied
// sample gives p99 something to diverge from p50 on, which is the whole point
// of the latency dashboard.
func (s *Server) simulateProcessing() {
	if s.cfg.ProcessingJitter <= 0 {
		return
	}
	d := time.Duration(rand.Int64N(int64(s.cfg.ProcessingJitter)))
	if rand.Float64() < 0.02 {
		d *= 8
	}
	time.Sleep(d)
}

func (s *Server) shouldInjectError() bool {
	return s.cfg.ErrorInjectionRate > 0 && rand.Float64() < s.cfg.ErrorInjectionRate
}

func (s *Server) trackSession(conn *websocket.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.sessions[conn] = struct{}{}
	return true
}

func (s *Server) untrackSession(conn *websocket.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, conn)
}

func (s *Server) sessionCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sessions)
}

// Drain stops advertising readiness, then closes every live session with a
// going-away frame so clients reconnect to a healthy instance rather than
// discovering a dead socket. This is the part of a deploy that decides whether
// a rollout is invisible to callers or drops every call in flight.
func (s *Server) Drain() int {
	s.ready.Store(false)

	s.mu.Lock()
	s.closed = true
	conns := make([]*websocket.Conn, 0, len(s.sessions))
	for c := range s.sessions {
		conns = append(conns, c)
	}
	s.mu.Unlock()

	for _, c := range conns {
		_ = c.Close(websocket.StatusGoingAway, "server shutting down")
	}
	return len(conns)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
