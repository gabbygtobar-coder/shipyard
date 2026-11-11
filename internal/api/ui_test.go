package api

import (
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gabbygtobar-coder/shipyard/internal/deploy"
	"github.com/gabbygtobar-coder/shipyard/internal/metrics"
	"github.com/gabbygtobar-coder/shipyard/internal/store"
	"github.com/prometheus/client_golang/prometheus"
)

func TestUIRequiresAuth(t *testing.T) {
	t.Parallel()
	st := seededUIStore(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	closed := NewHandler(st, nil, logger, nil)
	rec := doRequest(closed, http.MethodGet, "/ui", "", "")
	if rec.Code != http.StatusUnauthorized || strings.Contains(rec.Body.String(), "demo") {
		t.Fatalf("unconfigured %d %s", rec.Code, rec.Body.String())
	}

	const token = "dashboard-secret"
	handler := NewHandler(st, rollbackStub{}, logger, nil, WithUIToken(token))
	rec = doRequest(handler, http.MethodGet, "/ui", "", "")
	if rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") == "" || strings.Contains(rec.Body.String(), "github.com/acme/demo") {
		t.Fatalf("missing auth %d %s", rec.Code, rec.Body.String())
	}
	rec = doUI(handler, http.MethodGet, "/ui", token, "wrong-secret", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token %d", rec.Code)
	}

	rec = doUI(handler, http.MethodGet, "/ui", token, token, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "demo") || !strings.Contains(rec.Body.String(), "running") {
		t.Fatalf("index %d %s", rec.Code, rec.Body.String())
	}
	rec = doUI(handler, http.MethodGet, "/ui/apps/demo", token, token, "")
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "dep_1") || !strings.Contains(body, "/deployments/dep_1/logs") || !strings.Contains(body, "health_failed") || !strings.Contains(body, "Roll back") {
		t.Fatalf("app page %d %s", rec.Code, body)
	}
	if !strings.Contains(body, "setTimeout") {
		t.Fatal("pending deployment did not enable refresh")
	}

	form := url.Values{}
	form.Set("confirm", "rollback")
	form.Set("deployment_id", "dep_1")
	req := httptest.NewRequest(http.MethodPost, "/ui/apps/demo/rollback", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", basicHeader("shipyard", token))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "/ui/apps/demo") {
		t.Fatalf("rollback %d location %s body %s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/ui/apps/demo/rollback", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("rollback without auth %d", rec.Code)
	}
}

func TestEventsAndMetricsEndpoints(t *testing.T) {
	t.Parallel()
	st := seededUIStore(t)
	reg := prometheus.NewRegistry()
	collected, err := metrics.New(reg, st.CountActiveDeployments)
	if err != nil {
		t.Fatal(err)
	}
	collected.ObserveBuild("succeeded", time.Second)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := NewHandler(st, nil, logger, nil, WithMetrics(collected))

	rec := doRequest(handler, http.MethodGet, "/apps/demo/events", "", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "health_failed") || !strings.Contains(rec.Body.String(), "deployed") {
		t.Fatalf("events %d %s", rec.Code, rec.Body.String())
	}
	rec = doRequest(handler, http.MethodGet, "/apps/missing/events", "", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing %d %s", rec.Code, rec.Body.String())
	}
	rec = doRequest(handler, http.MethodGet, "/metrics", "", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "shipyard_builds_total") || !strings.Contains(rec.Body.String(), "shipyard_active_deployments") {
		t.Fatalf("metrics %d %s", rec.Code, rec.Body.String())
	}
}

func seededUIStore(t *testing.T) *store.Memory {
	t.Helper()
	st := store.NewMemoryWithClock(func() time.Time {
		return time.Date(2026, 11, 11, 3, 4, 5, 0, time.UTC)
	})
	ctx := context.Background()
	if _, err := st.CreateApp(ctx, "demo", "https://github.com/acme/demo"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetAppStatus(ctx, "demo", store.AppStatusRunning, "http://demo.localhost:8081/"); err != nil {
		t.Fatal(err)
	}
	dep, err := st.CreateBuild(ctx, "demo", "main")
	if err != nil {
		t.Fatal(err)
	}
	building := store.DeploymentStatusBuilding
	if _, err := st.UpdateDeployment(ctx, dep.ID, store.DeploymentUpdate{Status: &building}); err != nil {
		t.Fatal(err)
	}
	succeeded := store.DeploymentStatusSucceeded
	tag := "shipyard/demo:0123456789ab"
	if _, err := st.UpdateDeployment(ctx, dep.ID, store.DeploymentUpdate{Status: &succeeded, ImageTag: &tag}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendEvent(ctx, store.Event{AppName: "demo", Type: store.EventDeployed, Message: "deployment " + dep.ID + " is running", DeploymentID: dep.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendEvent(ctx, store.Event{AppName: "demo", Type: store.EventHealthFailed, Message: "health check failed", DeploymentID: dep.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateBuild(ctx, "demo", "main"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RecordRelease(ctx, dep.ID, 5); err != nil {
		t.Fatal(err)
	}
	return st
}

type rollbackStub struct{}

func (rollbackStub) StartBuild(context.Context, string, string) (store.Deployment, error) {
	return store.Deployment{}, deploy.ErrStopped
}

func (rollbackStub) Rollback(_ context.Context, appName, deploymentID string) (store.Deployment, error) {
	return store.Deployment{ID: "dep_9", AppName: appName, RollbackOf: deploymentID, Status: store.DeploymentStatusQueued}, nil
}

func doUI(h http.Handler, method, path, user, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", basicHeader(user, token))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func basicHeader(user, token string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+token))
}

func httptestNew(method, path, body string) *http.Request {
	return httptest.NewRequest(method, path, strings.NewReader(body))
}
