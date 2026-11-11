package metrics

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestRegisterAndObserve(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	m, err := New(reg, func(context.Context) (int, error) { return 4, nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(reg, nil); err == nil {
		t.Fatal("second registration succeeded")
	}

	m.ObserveBuild("succeeded", 1500*time.Millisecond)
	m.ObserveBuild("failed", 2*time.Second)
	m.ObserveDeploy("running")
	m.ObserveDeploy("failed")
	m.ObserveProxy("hello", 200, 25*time.Millisecond)
	m.ObserveProxy("", 404, 5*time.Millisecond)
	m.ObserveWebhook("accepted")
	m.ObserveWebhook("unauthorized")

	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]*dto.MetricFamily{}
	for _, fam := range families {
		got[fam.GetName()] = fam
	}
	want := []string{
		"shipyard_build_duration_seconds",
		"shipyard_builds_total",
		"shipyard_deploys_total",
		"shipyard_proxy_requests_total",
		"shipyard_proxy_request_duration_seconds",
		"shipyard_active_deployments",
		"shipyard_webhook_deliveries_total",
	}
	for _, name := range want {
		if got[name] == nil {
			t.Errorf("missing metric %s", name)
		}
	}
	if n := gaugeValue(t, got["shipyard_active_deployments"]); n != 4 {
		t.Fatalf("active deployments %v", n)
	}
	if n := counterValue(t, got["shipyard_builds_total"], "status", "succeeded"); n != 1 {
		t.Fatalf("succeeded builds %v", n)
	}
	if n := counterValue(t, got["shipyard_deploys_total"], "status", "running"); n != 1 {
		t.Fatalf("running deploys %v", n)
	}
	if n := counterValue(t, got["shipyard_proxy_requests_total"], "app", "hello"); n != 1 {
		t.Fatalf("proxy requests %v", n)
	}
	if n := counterValue(t, got["shipyard_proxy_requests_total"], "app", "none"); n != 1 {
		t.Fatalf("proxy requests without an app %v", n)
	}
	if n := counterValue(t, got["shipyard_webhook_deliveries_total"], "result", "accepted"); n != 1 {
		t.Fatalf("accepted webhooks %v", n)
	}
	if sampleCount(t, got["shipyard_build_duration_seconds"], "status", "succeeded") != 1 {
		t.Fatal("build duration was not observed")
	}
	if sampleCount(t, got["shipyard_proxy_request_duration_seconds"], "app", "hello") != 1 {
		t.Fatal("proxy latency was not observed")
	}

	rec := httptestResponse(t, m)
	if !strings.Contains(rec, "shipyard_builds_total") || !strings.Contains(rec, "shipyard_active_deployments 4") {
		t.Fatalf("exposition missing series:\n%s", rec)
	}
}

func TestNilMetricsAreIgnored(t *testing.T) {
	t.Parallel()
	var m *Metrics
	m.ObserveBuild("succeeded", time.Second)
	m.ObserveDeploy("running")
	m.ObserveProxy("hello", 200, time.Millisecond)
	m.ObserveWebhook("accepted")
}

func gaugeValue(t *testing.T, fam *dto.MetricFamily) float64 {
	t.Helper()
	if fam == nil || len(fam.Metric) != 1 || fam.Metric[0].Gauge == nil {
		t.Fatalf("gauge family %#v", fam)
	}
	return fam.Metric[0].Gauge.GetValue()
}

func counterValue(t *testing.T, fam *dto.MetricFamily, key, value string) float64 {
	t.Helper()
	for _, metric := range fam.GetMetric() {
		if hasLabel(metric, key, value) && metric.Counter != nil {
			return metric.Counter.GetValue()
		}
	}
	t.Fatalf("counter %s=%s not found in %s", key, value, fam.GetName())
	return 0
}

func sampleCount(t *testing.T, fam *dto.MetricFamily, key, value string) uint64 {
	t.Helper()
	for _, metric := range fam.GetMetric() {
		if hasLabel(metric, key, value) && metric.Histogram != nil {
			return metric.Histogram.GetSampleCount()
		}
	}
	t.Fatalf("histogram %s=%s not found in %s", key, value, fam.GetName())
	return 0
}

func hasLabel(metric *dto.Metric, key, value string) bool {
	for _, label := range metric.GetLabel() {
		if label.GetName() == key && label.GetValue() == value {
			return true
		}
	}
	return false
}

func httptestResponse(t *testing.T, m *Metrics) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("metrics status %d body %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}
