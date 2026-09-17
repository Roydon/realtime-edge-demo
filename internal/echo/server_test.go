package echo

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/realtime-edge-demo/realtime-edge-demo/internal/metrics"
)

func newTestServer(t *testing.T, mutate func(*Config)) (*Server, *httptest.Server) {
	t.Helper()

	cfg := Config{
		ListenAddr:     ":0",
		MaxSessions:    10,
		IdleTimeout:    2 * time.Second,
		ShutdownGrace:  time.Second,
		ReadLimitBytes: 64 * 1024,
	}
	if mutate != nil {
		mutate(&cfg)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := NewServer(cfg, log, prometheus.NewRegistry(), "test")
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return srv, ts
}

func dialWS(t *testing.T, ctx context.Context, ts *httptest.Server) *websocket.Conn {
	t.Helper()
	url := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("dial /ws: %v", err)
	}
	return conn
}

func TestWebSocketEchoesPayload(t *testing.T) {
	_, ts := newTestServer(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn := dialWS(t, ctx, ts)
	defer conn.Close(websocket.StatusNormalClosure, "")

	want := "the quick brown fox"
	if err := conn.Write(ctx, websocket.MessageText, []byte(want)); err != nil {
		t.Fatalf("write: %v", err)
	}

	typ, got, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if typ != websocket.MessageText {
		t.Errorf("message type = %v, want text", typ)
	}
	if string(got) != want {
		t.Errorf("echo = %q, want %q", got, want)
	}
}

// A session that ends with a normal client close must not be counted as an
// error. Getting this wrong is what makes an error-rate alert fire every time
// a user hangs up normally.
func TestCleanClientCloseIsNotAnError(t *testing.T) {
	srv, ts := newTestServer(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn := dialWS(t, ctx, ts)
	if err := conn.Write(ctx, websocket.MessageText, []byte("hi")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, _, err := conn.Read(ctx); err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := conn.Close(websocket.StatusNormalClosure, "done"); err != nil {
		t.Fatalf("close: %v", err)
	}

	waitFor(t, func() bool {
		return counterValue(t, srv.metrics.DisconnectsTotal, metrics.ReasonClient) == 1
	}, "disconnect recorded as client_closed")

	if got := counterValue(t, srv.metrics.ErrorsTotal, metrics.TransportWS, metrics.CauseRead); got != 0 {
		t.Errorf("read errors = %v, want 0 after a clean close", got)
	}
	if got := counterValue(t, srv.metrics.DisconnectsTotal, metrics.ReasonError); got != 0 {
		t.Errorf("error disconnects = %v, want 0 after a clean close", got)
	}
}

func TestActiveSessionsTracksLifecycle(t *testing.T) {
	srv, ts := newTestServer(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conns := make([]*websocket.Conn, 3)
	for i := range conns {
		conns[i] = dialWS(t, ctx, ts)
	}
	waitFor(t, func() bool { return gaugeValue(t, srv.metrics.ActiveSessions) == 3 }, "3 active sessions")

	for _, c := range conns {
		_ = c.Close(websocket.StatusNormalClosure, "")
	}
	waitFor(t, func() bool { return gaugeValue(t, srv.metrics.ActiveSessions) == 0 }, "0 active sessions")
}

func TestSessionCapacityIsEnforced(t *testing.T) {
	srv, ts := newTestServer(t, func(c *Config) { c.MaxSessions = 1 })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	first := dialWS(t, ctx, ts)
	defer first.Close(websocket.StatusNormalClosure, "")
	waitFor(t, func() bool { return gaugeValue(t, srv.metrics.ActiveSessions) == 1 }, "first session established")

	url := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
	if _, _, err := websocket.Dial(ctx, url, nil); err == nil {
		t.Fatal("second dial succeeded, want rejection at capacity")
	}

	if got := counterValue(t, srv.metrics.ErrorsTotal, metrics.TransportWS, metrics.CauseOverloaded); got != 1 {
		t.Errorf("overloaded errors = %v, want 1", got)
	}
}

// Draining must flip readiness before closing sockets, so the proxy stops
// routing new sessions here while the existing ones are still being closed.
func TestDrainClosesSessionsAndFailsReadiness(t *testing.T) {
	srv, ts := newTestServer(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn := dialWS(t, ctx, ts)
	waitFor(t, func() bool { return gaugeValue(t, srv.metrics.ActiveSessions) == 1 }, "session established")

	if code := probe(t, ts.URL+"/readyz"); code != http.StatusOK {
		t.Fatalf("readyz before drain = %d, want 200", code)
	}

	if closed := srv.Drain(); closed != 1 {
		t.Errorf("drained %d sessions, want 1", closed)
	}

	if code := probe(t, ts.URL+"/readyz"); code != http.StatusServiceUnavailable {
		t.Errorf("readyz after drain = %d, want 503", code)
	}
	// Liveness must stay green while draining, or the supervisor restarts the
	// process mid-drain and undoes the point of draining.
	if code := probe(t, ts.URL+"/healthz"); code != http.StatusOK {
		t.Errorf("healthz during drain = %d, want 200", code)
	}

	if _, _, err := conn.Read(ctx); err == nil {
		t.Error("read after drain succeeded, want the session to be closed")
	}
}

func TestHTTPEchoRoundTrip(t *testing.T) {
	srv, ts := newTestServer(t, nil)

	resp, err := http.Post(ts.URL+"/api/echo", "application/json", strings.NewReader(`{"message":"ping"}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body echoResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Message != "ping" {
		t.Errorf("message = %q, want %q", body.Message, "ping")
	}
	if got := counterValue(t, srv.metrics.MessagesTotal, metrics.TransportHTTP, "out"); got != 1 {
		t.Errorf("http out messages = %v, want 1", got)
	}
}

func TestHTTPEchoRejectsMalformedBody(t *testing.T) {
	srv, ts := newTestServer(t, nil)

	resp, err := http.Post(ts.URL+"/api/echo", "application/json", strings.NewReader("not json"))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
	if got := counterValue(t, srv.metrics.ErrorsTotal, metrics.TransportHTTP, metrics.CauseBadRequest); got != 1 {
		t.Errorf("bad_request errors = %v, want 1", got)
	}
}

// Error injection is what gives the error-rate dashboard something to show.
// If it silently stopped working the dashboard would look healthy for the
// wrong reason, so it is worth a test of its own.
func TestErrorInjectionIsCountedAndReturned(t *testing.T) {
	srv, ts := newTestServer(t, func(c *Config) { c.ErrorInjectionRate = 1 })

	resp, err := http.Post(ts.URL+"/api/echo", "application/json", strings.NewReader(`{"message":"ping"}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", resp.StatusCode)
	}
	if got := counterValue(t, srv.metrics.ErrorsTotal, metrics.TransportHTTP, metrics.CauseInjected); got != 1 {
		t.Errorf("injected errors = %v, want 1", got)
	}
}

func TestLoadConfigRejectsMalformedValues(t *testing.T) {
	t.Setenv("ERROR_INJECTION_RATE", "1.5")
	if _, err := LoadConfig(); err == nil {
		t.Error("LoadConfig accepted an out-of-range injection rate, want an error")
	}

	t.Setenv("ERROR_INJECTION_RATE", "")
	t.Setenv("IDLE_TIMEOUT", "not-a-duration")
	if _, err := LoadConfig(); err == nil {
		t.Error("LoadConfig accepted a malformed duration, want an error")
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.ListenAddr != ":8080" {
		t.Errorf("ListenAddr = %q, want :8080", cfg.ListenAddr)
	}
	if cfg.IdleTimeout != 75*time.Second {
		t.Errorf("IdleTimeout = %v, want 75s", cfg.IdleTimeout)
	}
	if cfg.ErrorInjectionRate != 0 {
		t.Errorf("ErrorInjectionRate = %v, want 0 by default", cfg.ErrorInjectionRate)
	}
}

func TestMetricsEndpointExposesServiceMetrics(t *testing.T) {
	_, ts := newTestServer(t, nil)

	resp, err := http.Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatalf("get /metrics: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	// These three are what the provisioned dashboards and alert rules query by
	// name. Renaming one without updating them would break the dashboards
	// silently, so the contract is pinned here.
	for _, want := range []string{
		"voice_echo_message_latency_seconds",
		"voice_echo_active_sessions",
		"voice_echo_errors_total",
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("/metrics is missing %q, which the dashboards query by name", want)
		}
	}
}

// --- helpers ---

func probe(t *testing.T, url string) int {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("get %s: %v", url, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// waitFor polls until cond holds. Session teardown is handled by the server's
// own goroutine, so metrics settle shortly after the client-side close returns.
func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}

func counterValue(t *testing.T, vec *prometheus.CounterVec, labels ...string) float64 {
	t.Helper()
	m := &dto.Metric{}
	if err := vec.WithLabelValues(labels...).Write(m); err != nil {
		t.Fatalf("read counter %v: %v", labels, err)
	}
	return m.GetCounter().GetValue()
}

func gaugeValue(t *testing.T, g prometheus.Gauge) float64 {
	t.Helper()
	m := &dto.Metric{}
	if err := g.Write(m); err != nil {
		t.Fatalf("read gauge: %v", err)
	}
	return m.GetGauge().GetValue()
}
