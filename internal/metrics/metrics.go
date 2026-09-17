// Package metrics defines the Prometheus instrumentation exposed by the echo
// service. The metric names and labels deliberately mirror what a real-time
// media service needs on a dashboard: how long the data path takes, how many
// sessions are live, and why sessions end.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// Namespace prefixes every metric this service exports.
const Namespace = "voice_echo"

// Set is the full collection of metrics for one service instance. It is passed
// explicitly rather than kept in package globals so tests can build an isolated
// registry per case.
type Set struct {
	MessageLatency   *prometheus.HistogramVec
	ActiveSessions   prometheus.Gauge
	SessionsTotal    prometheus.Counter
	MessagesTotal    *prometheus.CounterVec
	ErrorsTotal      *prometheus.CounterVec
	DisconnectsTotal *prometheus.CounterVec
	BuildInfo        *prometheus.GaugeVec
}

// LatencyBuckets are tuned for a real-time data path, where the interesting
// range is sub-millisecond to a few hundred milliseconds. The default
// client_golang buckets top out in a range that is useless here: anything past
// ~500ms is already a failed call for interactive audio, so the tail buckets
// exist only to catch pathological outliers.
var LatencyBuckets = []float64{
	0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05,
	0.075, 0.1, 0.15, 0.25, 0.5, 1, 2.5,
}

// New builds the metric set and registers it on reg.
func New(reg prometheus.Registerer, version string) *Set {
	s := &Set{
		MessageLatency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: Namespace,
			Name:      "message_latency_seconds",
			Help:      "Time spent handling a single message, from read to write.",
			Buckets:   LatencyBuckets,
		}, []string{"transport"}),

		ActiveSessions: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: Namespace,
			Name:      "active_sessions",
			Help:      "Number of WebSocket sessions currently open.",
		}),

		SessionsTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: Namespace,
			Name:      "sessions_total",
			Help:      "Total WebSocket sessions accepted since start.",
		}),

		MessagesTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace,
			Name:      "messages_total",
			Help:      "Messages moved through the service, by transport and direction.",
		}, []string{"transport", "direction"}),

		ErrorsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace,
			Name:      "errors_total",
			Help:      "Errors encountered while serving, by transport and cause.",
		}, []string{"transport", "cause"}),

		DisconnectsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace,
			Name:      "disconnects_total",
			Help:      "Session terminations, by reason.",
		}, []string{"reason"}),

		BuildInfo: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: Namespace,
			Name:      "build_info",
			Help:      "Build metadata, always 1. Use the labels, not the value.",
		}, []string{"version"}),
	}

	reg.MustRegister(
		s.MessageLatency, s.ActiveSessions, s.SessionsTotal,
		s.MessagesTotal, s.ErrorsTotal, s.DisconnectsTotal, s.BuildInfo,
	)
	s.BuildInfo.WithLabelValues(version).Set(1)

	// Pre-create the label combinations the dashboards query, so panels read
	// zero instead of "No data" before the first event of that kind.
	for _, transport := range []string{TransportWS, TransportHTTP} {
		s.MessageLatency.WithLabelValues(transport)
		for _, dir := range []string{"in", "out"} {
			s.MessagesTotal.WithLabelValues(transport, dir)
		}
		for _, cause := range []string{CauseInjected, CauseRead, CauseWrite, CauseOverloaded, CauseBadRequest} {
			s.ErrorsTotal.WithLabelValues(transport, cause)
		}
	}
	for _, reason := range []string{ReasonClient, ReasonError, ReasonShutdown, ReasonIdle} {
		s.DisconnectsTotal.WithLabelValues(reason)
	}

	return s
}

// Transport and cause label values, named rather than inlined so a typo shows
// up at compile time instead of as a silently empty dashboard panel.
const (
	TransportWS   = "websocket"
	TransportHTTP = "http"

	CauseInjected   = "injected"
	CauseRead       = "read"
	CauseWrite      = "write"
	CauseOverloaded = "overloaded"
	CauseBadRequest = "bad_request"

	ReasonClient   = "client_closed"
	ReasonError    = "error"
	ReasonShutdown = "server_shutdown"
	ReasonIdle     = "idle_timeout"
)

// NewRegistry returns a registry carrying the Go runtime and process
// collectors alongside whatever the caller registers. The runtime metrics are
// what answer "is this a leak or is it load?" during an incident.
func NewRegistry() *prometheus.Registry {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	return reg
}
