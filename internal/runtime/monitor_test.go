package runtime

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gabbygtobar-coder/shipyard/internal/route"
	"github.com/gabbygtobar-coder/shipyard/internal/store"
)

type restartRecorder struct {
	calls atomic.Int32
	ids   []string
}

func (r *restartRecorder) Restart(_ context.Context, id string) error {
	r.calls.Add(1)
	r.ids = append(r.ids, id)
	return nil
}

func TestMonitorMarksUnhealthyAndRestartsOnce(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)

	st := store.NewMemory()
	if _, err := st.CreateApp(context.Background(), "demo", "https://github.com/acme/demo"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetAppStatus(context.Background(), "demo", store.AppStatusRunning, "http://demo.localhost:8081/"); err != nil {
		t.Fatal(err)
	}
	routes := route.NewTable()
	routes.Swap("demo", route.Target{Endpoint: srv.URL, InstanceID: "0123456789abcdef", DeploymentID: "dep_1"})
	restarts := &restartRecorder{}
	mon := newTestMonitor(t, st, routes, 3, true, restarts)

	for i := 0; i < 2; i++ {
		mon.Tick(context.Background())
	}
	app, err := st.GetApp(context.Background(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	if app.Status != store.AppStatusRunning {
		t.Fatalf("status %s before threshold", app.Status)
	}
	events, err := st.ListEvents(context.Background(), "demo", 0)
	if err != nil || len(events) != 0 || restarts.calls.Load() != 0 {
		t.Fatalf("events %d restarts %d err %v", len(events), restarts.calls.Load(), err)
	}

	mon.Tick(context.Background())
	app, err = st.GetApp(context.Background(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	if app.Status != store.AppStatusUnhealthy || app.URL == "" {
		t.Fatalf("app %#v", app)
	}
	events, err = st.ListEvents(context.Background(), "demo", 0)
	if err != nil || len(events) != 1 || events[0].Type != store.EventHealthFailed || events[0].DeploymentID != "dep_1" {
		t.Fatalf("events %#v err %v", events, err)
	}
	if restarts.calls.Load() != 1 || len(restarts.ids) != 1 || restarts.ids[0] != "0123456789abcdef" {
		t.Fatalf("restarts %#v", restarts.ids)
	}

	mon.Tick(context.Background())
	events, err = st.ListEvents(context.Background(), "demo", 0)
	if err != nil || len(events) != 1 || restarts.calls.Load() != 1 {
		t.Fatalf("repeated tick events %d restarts %d", len(events), restarts.calls.Load())
	}
}

func TestMonitorRecoversAndSkipsRestartWhenDisabled(t *testing.T) {
	t.Parallel()
	var healthy atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if healthy.Load() {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)

	st := store.NewMemory()
	if _, err := st.CreateApp(context.Background(), "demo", "https://github.com/acme/demo"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetAppStatus(context.Background(), "demo", store.AppStatusRunning, "http://demo.localhost:8081/"); err != nil {
		t.Fatal(err)
	}
	routes := route.NewTable()
	routes.Swap("demo", route.Target{Endpoint: srv.URL, InstanceID: "0123456789abcdef", DeploymentID: "dep_2"})
	restarts := &restartRecorder{}
	mon := newTestMonitor(t, st, routes, 1, false, restarts)
	mon.Tick(context.Background())
	app, err := st.GetApp(context.Background(), "demo")
	if err != nil || app.Status != store.AppStatusUnhealthy || restarts.calls.Load() != 0 {
		t.Fatalf("app %#v restarts %d err %v", app, restarts.calls.Load(), err)
	}

	healthy.Store(true)
	mon.Tick(context.Background())
	app, err = st.GetApp(context.Background(), "demo")
	if err != nil || app.Status != store.AppStatusRunning {
		t.Fatalf("recovered %#v err %v", app, err)
	}
}

func newTestMonitor(t *testing.T, st store.Store, routes *route.Table, threshold int, restart bool, restarter Restarter) *Monitor {
	t.Helper()
	mon, err := NewMonitor(MonitorConfig{
		Store:       st,
		Routes:      routes,
		Path:        "/",
		Interval:    time.Hour,
		Threshold:   threshold,
		AutoRestart: restart,
		Restarter:   restarter,
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return mon
}
