package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/gabbygtobar-coder/shipyard/internal/runtime"
	"github.com/gabbygtobar-coder/shipyard/internal/store"
)

type stopStub struct {
	st  *store.Memory
	id  string
	err error
}

func (s stopStub) Stop(ctx context.Context, app string) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	current, err := s.st.GetApp(ctx, app)
	if err != nil {
		return "", err
	}
	if current.Status != store.AppStatusRunning {
		return "", runtime.ErrNotRunning
	}
	stopped := store.DeploymentStatusStopped
	if _, err := s.st.UpdateDeployment(ctx, s.id, store.DeploymentUpdate{Status: &stopped}); err != nil {
		return "", err
	}
	if _, err := s.st.SetAppStatus(ctx, app, store.AppStatusStopped, ""); err != nil {
		return "", err
	}
	return s.id, nil
}

func TestAppURLAndStop(t *testing.T) {
	t.Parallel()
	st := store.NewMemory()
	if _, err := st.CreateApp(context.Background(), "demo", "https://github.com/acme/demo"); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := NewHandler(st, nil, logger, stopStub{st: st})
	rec := doRequest(h, http.MethodGet, "/apps/demo/url", "", "")
	if rec.Code != http.StatusConflict || decodeError(t, rec) != runtime.ErrNotRunning.Error() {
		t.Fatalf("idle url %d %s", rec.Code, rec.Body.String())
	}
	rec = doRequest(h, http.MethodPost, "/apps/missing/stop", "", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing stop %d %s", rec.Code, rec.Body.String())
	}

	dep, err := st.CreateDeployment(context.Background(), "demo", store.DeploymentStatusRunning)
	if err != nil {
		t.Fatal(err)
	}
	const publicURL = "http://demo.localhost:8081/"
	if _, err := st.SetAppStatus(context.Background(), "demo", store.AppStatusRunning, publicURL); err != nil {
		t.Fatal(err)
	}
	h = NewHandler(st, nil, logger, stopStub{st: st, id: dep.ID})
	rec = doRequest(h, http.MethodGet, "/apps/demo/url", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("url %d %s", rec.Code, rec.Body.String())
	}
	var body urlResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.URL != publicURL {
		t.Fatalf("url %q", body.URL)
	}
	rec = doRequest(h, http.MethodGet, "/deployments/"+dep.ID, "", "")
	var depBody store.Deployment
	if err := json.Unmarshal(rec.Body.Bytes(), &depBody); err != nil {
		t.Fatal(err)
	}
	if depBody.Status != store.DeploymentStatusRunning {
		t.Fatalf("deployment %#v", depBody)
	}

	rec = doRequest(h, http.MethodPost, "/apps/demo/stop", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("stop %d %s", rec.Code, rec.Body.String())
	}
	var app store.App
	if err := json.Unmarshal(rec.Body.Bytes(), &app); err != nil {
		t.Fatal(err)
	}
	if app.Status != store.AppStatusStopped || app.URL != "" {
		t.Fatalf("app %#v", app)
	}
	rec = doRequest(h, http.MethodPost, "/apps/demo/stop", "", "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("second stop %d %s", rec.Code, rec.Body.String())
	}

	unconfigured := NewHandler(st, nil, logger, nil)
	rec = doRequest(unconfigured, http.MethodPost, "/apps/demo/stop", "", "")
	if rec.Code != http.StatusServiceUnavailable || decodeError(t, rec) != errRuntimeUnconfigured {
		t.Fatalf("unconfigured %d %s", rec.Code, rec.Body.String())
	}
}

func TestStopRuntimeError(t *testing.T) {
	t.Parallel()
	st := store.NewMemory()
	if _, err := st.CreateApp(context.Background(), "demo", "https://github.com/acme/demo"); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := NewHandler(st, nil, logger, stopStub{err: errors.New("docker rm failed")})
	rec := doRequest(h, http.MethodPost, "/apps/demo/stop", "", "")
	if rec.Code != http.StatusInternalServerError || decodeError(t, rec) != errInternal {
		t.Fatalf("stop %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "docker rm") {
		t.Fatal("runtime error leaked")
	}
}
