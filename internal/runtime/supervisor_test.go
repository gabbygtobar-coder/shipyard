package runtime

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gabbygtobar-coder/shipyard/internal/proxy"
	"github.com/gabbygtobar-coder/shipyard/internal/route"
	"github.com/gabbygtobar-coder/shipyard/internal/store"
)

const testImage = "shipyard/demo:0123456789ab"

func newSupervisor(t *testing.T, rt Runtime, checker Checker, routes *route.Table) (*Supervisor, *store.Memory) {
	t.Helper()
	st := store.NewMemory()
	if _, err := st.CreateApp(context.Background(), "demo", "https://github.com/acme/demo"); err != nil {
		t.Fatal(err)
	}
	if routes == nil {
		routes = route.NewTable()
	}
	sup, err := NewSupervisor(SupervisorConfig{
		Store:         st,
		Runtime:       rt,
		Checker:       checker,
		Routes:        routes,
		HealthTimeout: 200 * time.Millisecond,
		PublicURL: func(app string) string {
			return "http://" + app + ".localhost:8081/"
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return sup, st
}

func startingDeployment(t *testing.T, st store.Store) store.Deployment {
	t.Helper()
	dep, err := st.CreateDeployment(context.Background(), "demo", store.DeploymentStatusStarting)
	if err != nil {
		t.Fatal(err)
	}
	return dep
}

func TestSupervisorSwapKeepsPreviousUntilHealthy(t *testing.T) {
	t.Parallel()
	blue := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "blue")
	}))
	green := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "green")
	}))
	t.Cleanup(blue.Close)
	t.Cleanup(green.Close)

	routes := route.NewTable()
	fake := &Fake{Endpoints: []string{blue.URL, green.URL}}
	fake.OnStop = func(id string) {
		if current, ok := routes.Lookup("demo"); ok && current.InstanceID == id {
			t.Errorf("stopped %s while it was still routed", id)
		}
	}
	sup, st := newSupervisor(t, fake, &FakeChecker{}, routes)
	handler := proxy.New(routes, nil)

	first := startingDeployment(t, st)
	var logs bytes.Buffer
	if err := sup.Release(context.Background(), "demo", first.ID, testImage, &logs); err != nil {
		t.Fatal(err)
	}
	if body := getApp(t, handler, "demo"); body != "blue" {
		t.Fatalf("body %q", body)
	}
	if !strings.Contains(logs.String(), "healthy") || !strings.Contains(logs.String(), "http://demo.localhost:8081/") {
		t.Fatalf("log %s", logs.String())
	}
	app, err := st.GetApp(context.Background(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	if app.Status != store.AppStatusRunning || app.URL != "http://demo.localhost:8081/" {
		t.Fatalf("app %#v", app)
	}

	second := startingDeployment(t, st)
	logs.Reset()
	if err := sup.Release(context.Background(), "demo", second.ID, testImage, &logs); err != nil {
		t.Fatal(err)
	}
	if body := getApp(t, handler, "demo"); body != "green" {
		t.Fatalf("body %q", body)
	}
	if len(fake.Stops) != 1 {
		t.Fatalf("stops %#v", fake.Stops)
	}
	prev, err := st.GetDeployment(context.Background(), first.ID)
	if err != nil {
		t.Fatal(err)
	}
	next, err := st.GetDeployment(context.Background(), second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if prev.Status != store.DeploymentStatusStopped || next.Status != store.DeploymentStatusRunning || next.URL == "" {
		t.Fatalf("prev %#v next %#v", prev, next)
	}
	if !strings.Contains(logs.String(), "stopping "+first.ID) {
		t.Fatalf("log %s", logs.String())
	}

	if _, err := sup.Stop(context.Background(), "demo"); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "http://demo.localhost/", nil)
	req.Host = "demo.localhost"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("after stop %d %s", rec.Code, rec.Body.String())
	}
	app, err = st.GetApp(context.Background(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	if app.Status != store.AppStatusStopped || app.URL != "" {
		t.Fatalf("stopped app %#v", app)
	}
	if _, err := sup.Stop(context.Background(), "demo"); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("second stop %v", err)
	}
}

func TestSupervisorHealthTimeoutLeavesPreviousRunning(t *testing.T) {
	t.Parallel()
	routes := route.NewTable()
	fake := &Fake{Endpoint: "http://127.0.0.1:9"}
	calls := 0
	checker := &FakeChecker{WaitFunc: func(context.Context, string) error {
		calls++
		if calls == 1 {
			return nil
		}
		return ErrHealthTimeout
	}}
	fake.OnStop = func(id string) {
		if current, ok := routes.Lookup("demo"); ok && current.InstanceID == id {
			t.Errorf("stopped routed instance %s", id)
		}
	}
	sup, st := newSupervisor(t, fake, checker, routes)
	first := startingDeployment(t, st)
	if err := sup.Release(context.Background(), "demo", first.ID, testImage, nil); err != nil {
		t.Fatal(err)
	}
	second := startingDeployment(t, st)
	err := sup.Release(context.Background(), "demo", second.ID, testImage, nil)
	if !errors.Is(err, ErrHealthTimeout) {
		t.Fatalf("error %v", err)
	}
	current, ok := routes.Lookup("demo")
	if !ok || current.DeploymentID != first.ID {
		t.Fatalf("route %#v %v", current, ok)
	}
	if len(fake.Stops) != 1 || len(fake.Specs) != 2 {
		t.Fatalf("stops %#v specs %d", fake.Stops, len(fake.Specs))
	}
	failed := fake.Specs[1]
	stopped := fake.Stops[0]
	if stopped == "" || failed.DeploymentID != second.ID {
		t.Fatalf("stopped %s failed deployment %s", stopped, failed.DeploymentID)
	}
	got, err := st.GetDeployment(context.Background(), second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.DeploymentStatusStarting {
		t.Fatalf("second status %s", got.Status)
	}
	still, err := st.GetDeployment(context.Background(), first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if still.Status != store.DeploymentStatusRunning {
		t.Fatalf("first status %s", still.Status)
	}
}

func TestSupervisorStartFailureDoesNotRoute(t *testing.T) {
	t.Parallel()
	fake := &Fake{StartErr: errors.New("daemon down")}
	sup, st := newSupervisor(t, fake, &FakeChecker{}, nil)
	dep := startingDeployment(t, st)
	err := sup.Release(context.Background(), "demo", dep.ID, testImage, nil)
	if err == nil || len(fake.Specs) != 0 {
		t.Fatalf("error %v specs %#v", err, fake.Specs)
	}
	if _, ok := sup.routes.Lookup("demo"); ok {
		t.Fatal("failed start was routed")
	}
}

func TestNewSupervisorRequiresDependencies(t *testing.T) {
	t.Parallel()
	if _, err := NewSupervisor(SupervisorConfig{}); err == nil {
		t.Fatal("expected an error")
	}
}

func getApp(t *testing.T, handler http.Handler, app string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "http://"+app+".localhost/apps/"+app+"/", nil)
	req.Host = app + ".localhost"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}
