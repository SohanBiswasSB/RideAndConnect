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

	reg.MustRegister(
		m.HTTPRequests, m.WSConnections, m.WSMessages,
		m.RouteProviderCalls, m.RouteCacheLookups, m.EmergencyEvents, m.PairEvents,
	)
	return m
}

// Handler returns the /metrics HTTP handler.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{Registry: m.registry})
}
