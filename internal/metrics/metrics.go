// Package metrics registers the Prometheus collectors exposed on /metrics.
package metrics

import (
	"context"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const namespace = "shipyard"

var labelPattern = regexp.MustCompile(`^[a-z0-9_][a-z0-9_-]{0,62}$`)

// ActiveCount returns how many deployments are queued, building, starting, or running.
type ActiveCount func(context.Context) (int, error)

// Metrics is the set of collectors shipyardd records.
// A nil *Metrics ignores every observation so callers can leave it unset in tests.
type Metrics struct {
	reg           *prometheus.Registry
	buildDuration *prometheus.HistogramVec
	builds        *prometheus.CounterVec
	deploys       *prometheus.CounterVec
	proxyRequests *prometheus.CounterVec
	proxyLatency  *prometheus.HistogramVec
	webhooks      *prometheus.CounterVec
}

// New registers the shipyard collectors on reg.
// A nil registry allocates a fresh one. count may be nil, in which case the
// active-deployments gauge stays at zero.
func New(reg *prometheus.Registry, count ActiveCount) (*Metrics, error) {
	if reg == nil {
		reg = prometheus.NewRegistry()
	}
	m := &Metrics{
		reg: reg,
		buildDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "build_duration_seconds",
			Help:      "Time spent cloning and building an image.",
			Buckets:   []float64{0.5, 1, 2, 5, 10, 30, 60, 120, 300, 600},
		}, []string{"status"}),
		builds: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "builds_total",
			Help:      "Image builds finished, by status.",
		}, []string{"status"}),
		deploys: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "deploys_total",
			Help:      "Deployments that reached a terminal deploy status.",
		}, []string{"status"}),
		proxyRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "proxy_requests_total",
			Help:      "HTTP requests received by the reverse proxy.",
		}, []string{"app", "status"}),
		proxyLatency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "proxy_request_duration_seconds",
			Help:      "Reverse proxy request latency, by app and HTTP status.",
			Buckets:   []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
		}, []string{"app", "status"}),
		webhooks: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "webhook_deliveries_total",
			Help:      "GitHub webhook deliveries handled, by result.",
		}, []string{"result"}),
	}
	active := prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "active_deployments",
		Help:      "Deployments that are queued, building, starting, or running.",
	}, func() float64 {
		if count == nil {
			return 0
		}
		n, err := count(context.Background())
		if err != nil || n < 0 {
			return 0
		}
		return float64(n)
	})
	for _, c := range []prometheus.Collector{m.buildDuration, m.builds, m.deploys, m.proxyRequests, m.proxyLatency, m.webhooks, active} {
		if err := reg.Register(c); err != nil {
			return nil, err
		}
	}
	m.prime()
	return m, nil
}

func (m *Metrics) prime() {
	for _, status := range []string{"succeeded", "failed"} {
		m.builds.WithLabelValues(status)
		m.buildDuration.WithLabelValues(status)
	}
	for _, status := range []string{"running", "succeeded", "failed"} {
		m.deploys.WithLabelValues(status)
	}
	for _, result := range []string{"accepted", "coalesced", "duplicate", "ignored", "unauthorized", "invalid", "error", "pong"} {
		m.webhooks.WithLabelValues(result)
	}
}

// Handler serves the Prometheus text exposition of this registry.
func (m *Metrics) Handler() http.Handler {
	if m == nil || m.reg == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "metrics are not configured\n", http.StatusNotFound)
		})
	}
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
}

// ObserveBuild records one finished image build.
func (m *Metrics) ObserveBuild(status string, d time.Duration) {
	if m == nil {
		return
	}
	if d < 0 {
		d = 0
	}
	status = sanitize(status)
	m.buildDuration.WithLabelValues(status).Observe(d.Seconds())
	m.builds.WithLabelValues(status).Inc()
}

// ObserveDeploy records one deployment that reached status.
func (m *Metrics) ObserveDeploy(status string) {
	if m == nil {
		return
	}
	m.deploys.WithLabelValues(sanitize(status)).Inc()
}

// ObserveProxy records one reverse-proxy request.
func (m *Metrics) ObserveProxy(app string, status int, latency time.Duration) {
	if m == nil {
		return
	}
	if latency < 0 {
		latency = 0
	}
	app = sanitizeApp(app)
	code := strconv.Itoa(status)
	m.proxyRequests.WithLabelValues(app, code).Inc()
	m.proxyLatency.WithLabelValues(app, code).Observe(latency.Seconds())
}

// ObserveWebhook records one webhook delivery result.
func (m *Metrics) ObserveWebhook(result string) {
	if m == nil {
		return
	}
	m.webhooks.WithLabelValues(sanitize(result)).Inc()
}

func sanitize(value string) string {
	if labelPattern.MatchString(value) {
		return value
	}
	return "other"
}

func sanitizeApp(app string) string {
	if app == "" {
		return "none"
	}
	if labelPattern.MatchString(app) {
		return app
	}
	return "other"
}
