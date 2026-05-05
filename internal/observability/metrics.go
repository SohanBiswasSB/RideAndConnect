// Package observability hosts the Prometheus registry and the small set
// of counters/histograms wired into the hot paths.
//
// The metric names follow Prometheus conventions: `subsystem_unit_total`
// for counters, `subsystem_seconds` for histograms. See
// docs/ROUTE_PROVIDER.md §11 for the full catalog.
package observability

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics groups the registered collectors so call sites have a single
// dependency to thread.
type Metrics struct {
	registry *prometheus.Registry

	HTTPRequests       *prometheus.CounterVec
	WSConnections      prometheus.Gauge
	WSMessages         *prometheus.CounterVec
	RouteProviderCalls *prometheus.CounterVec
	RouteCacheLookups  *prometheus.CounterVec
	EmergencyEvents    prometheus.Counter
	PairEvents         *prometheus.CounterVec
	WSTierTransitions  *prometheus.CounterVec
}

// New builds the registry with our process-local collectors plus the
// standard Go and process collectors.
func New() *Metrics {
	reg := prometheus.NewRegistry()
	reg.MustRegister(prometheus.NewGoCollector())
	reg.MustRegister(prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}))

	m := &Metrics{registry: reg}

	m.HTTPRequests = prometheus.NewCounterVec(
		prometheus.CounterOpts{Name: "http_requests_total", Help: "HTTP request count by route and status."},
		[]string{"route", "status"},
	)
	m.WSConnections = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "ws_connections_open",
		Help: "Currently-open WebSocket connections.",
	})
	m.WSMessages = prometheus.NewCounterVec(
		prometheus.CounterOpts{Name: "ws_messages_total", Help: "WebSocket messages by type and direction."},
		[]string{"type", "direction"},
	)
	m.RouteProviderCalls = prometheus.NewCounterVec(
		prometheus.CounterOpts{Name: "route_provider_calls_total", Help: "Route provider calls by provider and outcome."},
		[]string{"provider", "outcome"},
	)
	m.RouteCacheLookups = prometheus.NewCounterVec(
		prometheus.CounterOpts{Name: "route_cache_lookups_total", Help: "Route cache lookups by kind and result."},
		[]string{"kind", "result"},
	)
	m.EmergencyEvents = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "emergency_events_total", Help: "Emergency events triggered.",
	})
	m.PairEvents = prometheus.NewCounterVec(
		prometheus.CounterOpts{Name: "pair_events_total", Help: "Rider/pillion pair lifecycle events."},
		[]string{"event"},
	)
	m.WSTierTransitions = prometheus.NewCounterVec(
		prometheus.CounterOpts{Name: "ws_tier_transitions_total", Help: "Bandwidth-tier transitions observed by the heartbeat classifier."},
		[]string{"from", "to"},
	)

	reg.MustRegister(
		m.HTTPRequests, m.WSConnections, m.WSMessages,
		m.RouteProviderCalls, m.RouteCacheLookups, m.EmergencyEvents, m.PairEvents,
		m.WSTierTransitions,
	)
	return m
}

// Handler returns the /metrics HTTP handler.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{Registry: m.registry})
}

// ─── small typed wrappers ───────────────────────────────────────────────
// These satisfy the narrow interfaces in middleware / realtime / rides
// without those packages having to import prometheus/client_golang.

// IncRequest is the HTTPRequestCounter implementation.
func (m *Metrics) IncRequest(route, status string) {
	if m == nil {
		return
	}
	m.HTTPRequests.WithLabelValues(route, status).Inc()
}

// IncWSMessage is incremented in WS read/write loops.
func (m *Metrics) IncWSMessage(msgType, direction string) {
	if m == nil {
		return
	}
	m.WSMessages.WithLabelValues(msgType, direction).Inc()
}

// WSConnectionOpened / Closed track gauge.
func (m *Metrics) WSConnectionOpened() {
	if m == nil {
		return
	}
	m.WSConnections.Inc()
}
func (m *Metrics) WSConnectionClosed() {
	if m == nil {
		return
	}
	m.WSConnections.Dec()
}

// IncRouteProvider is incremented by the rides service after a provider
// call (or cache hit / breaker open / throttle).
func (m *Metrics) IncRouteProvider(provider, outcome string) {
	if m == nil {
		return
	}
	m.RouteProviderCalls.WithLabelValues(provider, outcome).Inc()
}

// IncRouteCache is incremented on cache hit/miss.
func (m *Metrics) IncRouteCache(kind, result string) {
	if m == nil {
		return
	}
	m.RouteCacheLookups.WithLabelValues(kind, result).Inc()
}

// IncEmergency is incremented on each fresh emergency event.
func (m *Metrics) IncEmergency() {
	if m == nil {
		return
	}
	m.EmergencyEvents.Inc()
}

// IncPair is incremented on pair create / detach.
func (m *Metrics) IncPair(event string) {
	if m == nil {
		return
	}
	m.PairEvents.WithLabelValues(event).Inc()
}

// IncWSTierTransition records a bandwidth-tier change so the RTT
// thresholds can be tuned against real rider data.
func (m *Metrics) IncWSTierTransition(from, to string) {
	if m == nil {
		return
	}
	m.WSTierTransitions.WithLabelValues(from, to).Inc()
}
