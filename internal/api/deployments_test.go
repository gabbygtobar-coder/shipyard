package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gabbygtobar-coder/shipyard/internal/build"
	"github.com/gabbygtobar-coder/shipyard/internal/deploy"
	"github.com/gabbygtobar-coder/shipyard/internal/giturl"
	"github.com/gabbygtobar-coder/shipyard/internal/source"
	"github.com/gabbygtobar-coder/shipyard/internal/store"
)

func newBuildAPI(t *testing.T, cloner source.Cloner, builder build.Builder, mutate func(*deploy.Config)) (http.Handler, *store.Memory) {
	t.Helper()
	st := store.NewMemoryWithClock(func() time.Time {
		return time.Date(2026, 11, 11, 3, 4, 5, 0, time.UTC)
	})
	if _, err := st.CreateApp(context.Background(), "demo", "https://github.com/acme/demo"); err != nil {
		t.Fatal(err)
	}
	if cloner == nil {
		cloner = &source.Fake{Files: map[string]string{"Dockerfile": "FROM scratch\n"}}
	}
	if builder == nil {
		builder = &build.Fake{Log: "ok\n"}
	}
	cfg := deploy.Config{
		Store:        st,
		Cloner:       cloner,
		Builder:      builder,
		Concurrency:  2,
		QueueSize:    4,
		BuildTimeout: 5 * time.Second,
		CloneTimeout: 2 * time.Second,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	runner, err := deploy.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runner.Stop)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewHandler(st, runner, logger), st
}

func waitDeployment(t *testing.T, st store.Store, id, status string) store.Deployment {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last store.Deployment
	for time.Now().Before(deadline) {
		dep, err := st.GetDeployment(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		last = dep
		if dep.Status == status {
			return dep
		}
		if store.IsTerminalStatus(dep.Status) && dep.Status != status {
			t.Fatalf("deployment %s is %s (%s), want %s", id, dep.Status, dep.Error, status)
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("deployment %s stuck in %s, want %s", id, last.Status, status)
	return store.Deployment{}
}

func TestCreateListGetDeployment(t *testing.T) {
	t.Parallel()
	h, st := newBuildAPI(t, nil, nil, nil)

	rec := doRequest(h, http.MethodGet, "/apps/demo/deployments", "", "")
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "null") {
		t.Fatalf("empty list %d %s", rec.Code, rec.Body.String())
	}
	var empty deploymentListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &empty); err != nil {
		t.Fatal(err)
	}
	if empty.Deployments == nil || len(empty.Deployments) != 0 {
		t.Fatalf("deployments = %#v", empty.Deployments)
	}

	rec = doRequest(h, http.MethodPost, "/apps/demo/deployments", `{"ref":"  main  "}`, jsonContentType)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create %d %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Location") != "/deployments/dep_1" {
		t.Fatalf("location %q", rec.Header().Get("Location"))
	}
	var queued store.Deployment
	if err := json.Unmarshal(rec.Body.Bytes(), &queued); err != nil {
		t.Fatal(err)
	}
	if queued.Status != store.DeploymentStatusQueued || queued.Ref != "main" || queued.AppName != "demo" {
		t.Fatalf("queued = %#v", queued)
	}
	if queued.StartedAt != nil || queued.CommitSHA != "" || queued.ImageTag != "" {
		t.Fatalf("queued fields = %#v", queued)
	}
	wantTime := time.Date(2026, 11, 11, 3, 4, 5, 0, time.UTC)
	if !queued.CreatedAt.Equal(wantTime) {
		t.Fatalf("created_at = %s", queued.CreatedAt)
	}

	done := waitDeployment(t, st, "dep_1", store.DeploymentStatusSucceeded)
	if done.ImageTag != "shipyard/demo:0123456789ab" || done.CommitSHA != "0123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("done = %#v", done)
	}
	if done.StartedAt == nil || done.FinishedAt == nil {
		t.Fatal("missing timestamps")
	}

	rec = doRequest(h, http.MethodGet, "/deployments/dep_1", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get %d %s", rec.Code, rec.Body.String())
	}
	var got store.Deployment
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ImageTag != done.ImageTag || got.Status != store.DeploymentStatusSucceeded {
		t.Fatalf("get = %#v", got)
	}

	rec = doRequest(h, http.MethodGet, "/apps/demo/deployments", "", "")
	var list deploymentListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Deployments) != 1 || list.Deployments[0].ID != "dep_1" {
		t.Fatalf("list = %#v", list.Deployments)
	}

	rec = doRequest(h, http.MethodGet, "/deployments/dep_1/logs", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("logs %d %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Fatalf("content type %q", rec.Header().Get("Content-Type"))
	}
	if !strings.Contains(rec.Body.String(), "ok\n") || !strings.Contains(rec.Body.String(), "shipyard/demo:0123456789ab") {
		t.Fatalf("logs = %q", rec.Body.String())
	}
}

func TestCreateDeploymentEmptyBodyUsesDefaultRef(t *testing.T) {
	t.Parallel()
	h, st := newBuildAPI(t, nil, nil, nil)
	rec := doRequest(h, http.MethodPost, "/apps/demo/deployments", "", "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
	var dep store.Deployment
	if err := json.Unmarshal(rec.Body.Bytes(), &dep); err != nil {
		t.Fatal(err)
	}
	if dep.Ref != "" {
		t.Fatalf("ref = %q", dep.Ref)
	}
	waitDeployment(t, st, dep.ID, store.DeploymentStatusSucceeded)
}

func TestCreateDeploymentRejected(t *testing.T) {
	t.Parallel()
	h, st := newBuildAPI(t, nil, nil, nil)

	rec := doRequest(h, http.MethodPost, "/apps/missing/deployments", `{}`, jsonContentType)
	if rec.Code != http.StatusNotFound || decodeError(t, rec) != errAppNotFound {
		t.Fatalf("missing app %d %s", rec.Code, rec.Body.String())
	}
	rec = doRequest(h, http.MethodPost, "/apps/Demo/deployments", `{}`, jsonContentType)
	if rec.Code != http.StatusBadRequest || decodeError(t, rec) != ErrNameInvalid.Error() {
		t.Fatalf("bad name %d %s", rec.Code, rec.Body.String())
	}
	rec = doRequest(h, http.MethodPost, "/apps/demo/deployments", `{"ref":"--upload-pack=evil"}`, jsonContentType)
	if rec.Code != http.StatusBadRequest || decodeError(t, rec) != giturl.ErrRefInvalid.Error() {
		t.Fatalf("bad ref %d %s", rec.Code, rec.Body.String())
	}
	listed, err := st.ListDeployments(context.Background(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 0 {
		t.Fatalf("rejected ref stored %#v", listed)
	}
	rec = doRequest(h, http.MethodPost, "/apps/demo/deployments", `{"ref":"main","image":"nope"}`, jsonContentType)
	if rec.Code != http.StatusBadRequest || decodeError(t, rec) != errInvalidJSON {
		t.Fatalf("unknown field %d %s", rec.Code, rec.Body.String())
	}
	rec = doRequest(h, http.MethodPost, "/apps/demo/deployments", `{"ref":"main"}`, "text/plain")
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("content type %d %s", rec.Code, rec.Body.String())
	}

	plain := newTestHandler(t)
	rec = doRequest(plain, http.MethodPost, "/apps/demo/deployments", `{}`, jsonContentType)
	if rec.Code != http.StatusServiceUnavailable || decodeError(t, rec) != errBuildsUnconfigured {
		t.Fatalf("nil builder %d %s", rec.Code, rec.Body.String())
	}
}

func TestDeploymentReads(t *testing.T) {
	t.Parallel()
	h := newTestHandler(t)
	rec := doRequest(h, http.MethodGet, "/apps/missing/deployments", "", "")
	if rec.Code != http.StatusNotFound || decodeError(t, rec) != errAppNotFound {
		t.Fatalf("list %d %s", rec.Code, rec.Body.String())
	}
	rec = doRequest(h, http.MethodGet, "/deployments/nope", "", "")
	if rec.Code != http.StatusBadRequest || decodeError(t, rec) != ErrDeploymentID.Error() {
		t.Fatalf("bad id %d %s", rec.Code, rec.Body.String())
	}
	rec = doRequest(h, http.MethodGet, "/deployments/dep_1", "", "")
	if rec.Code != http.StatusNotFound || decodeError(t, rec) != errDeploymentNotFound {
		t.Fatalf("missing %d %s", rec.Code, rec.Body.String())
	}
	rec = doRequest(h, http.MethodGet, "/deployments/dep_1/logs", "", "")
	if rec.Code != http.StatusNotFound || decodeError(t, rec) != errDeploymentNotFound {
		t.Fatalf("logs %d %s", rec.Code, rec.Body.String())
	}
}

func TestDeploymentFailureIsVisible(t *testing.T) {
	t.Parallel()
	h, st := newBuildAPI(t, &source.Fake{Files: map[string]string{"README.md": "no image\n"}}, nil, nil)
	rec := doRequest(h, http.MethodPost, "/apps/demo/deployments", `{"ref":"main"}`, jsonContentType)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create %d %s", rec.Code, rec.Body.String())
	}
	got := waitDeployment(t, st, "dep_1", store.DeploymentStatusFailed)
	if got.Error != "no Dockerfile at repository root" {
		t.Fatalf("error = %s", got.Error)
	}
	rec = doRequest(h, http.MethodGet, "/deployments/dep_1/logs", "", "")
	if !strings.Contains(rec.Body.String(), "no Dockerfile at repository root") {
		t.Fatalf("logs = %q", rec.Body.String())
	}
}

func TestDeploymentQueueFull(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{})
	release := make(chan struct{})
	builder := &build.Fake{Hook: func(ctx context.Context, _, _ string, _ io.Writer) error {
		select {
		case <-entered:
		default:
			close(entered)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-release:
			return nil
		}
	}}
	h, _ := newBuildAPI(t, nil, builder, func(cfg *deploy.Config) {
		cfg.Concurrency = 1
		cfg.QueueSize = 1
	})
	t.Cleanup(func() { close(release) })

	first := doRequest(h, http.MethodPost, "/apps/demo/deployments", `{}`, jsonContentType)
	if first.Code != http.StatusAccepted {
		t.Fatalf("first %d %s", first.Code, first.Body.String())
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("build did not start")
	}
	second := doRequest(h, http.MethodPost, "/apps/demo/deployments", `{}`, jsonContentType)
	if second.Code != http.StatusAccepted {
		t.Fatalf("second %d %s", second.Code, second.Body.String())
	}
	third := doRequest(h, http.MethodPost, "/apps/demo/deployments", `{}`, jsonContentType)
	if third.Code != http.StatusServiceUnavailable || decodeError(t, third) != deploy.ErrQueueFull.Error() {
		t.Fatalf("third %d %s", third.Code, third.Body.String())
	}
}

func TestDeploymentLogFollow(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	release := make(chan struct{})
	builder := &build.Fake{Hook: func(ctx context.Context, _, _ string, logs io.Writer) error {
		if _, err := io.WriteString(logs, "alpha\n"); err != nil {
			return err
		}
		close(started)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-release:
		}
		_, err := io.WriteString(logs, "beta\n")
		return err
	}}
	h, _ := newBuildAPI(t, nil, builder, nil)
	rec := doRequest(h, http.MethodPost, "/apps/demo/deployments", `{"ref":"main"}`, jsonContentType)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create %d %s", rec.Code, rec.Body.String())
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("build did not start")
	}

	lw := &bodyRecorder{ResponseRecorder: httptest.NewRecorder(), got: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		defer close(done)
		req := httptest.NewRequest(http.MethodGet, "/deployments/dep_1/logs?follow=1", nil)
		h.ServeHTTP(lw, req)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(lw.body(), "alpha\n") {
		time.Sleep(5 * time.Millisecond)
	}
	if !strings.Contains(lw.body(), "alpha\n") {
		t.Fatalf("stream = %q", lw.body())
	}
	if strings.Contains(lw.body(), "beta\n") {
		t.Fatal("beta was written before the build resumed")
	}
	close(release)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("follow request did not finish")
	}
	if lw.Code != http.StatusOK || !strings.Contains(lw.body(), "beta\n") {
		t.Fatalf("final %d %q", lw.Code, lw.body())
	}
}

type bodyRecorder struct {
	*httptest.ResponseRecorder
	mu   sync.Mutex
	got  chan struct{}
	once sync.Once
}

func (b *bodyRecorder) WriteHeader(code int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ResponseRecorder.WriteHeader(code)
}

func (b *bodyRecorder) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n, err := b.ResponseRecorder.Write(p)
	if n > 0 {
		b.once.Do(func() { close(b.got) })
	}
	return n, err
}

func (b *bodyRecorder) Flush() {}

func (b *bodyRecorder) body() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Body.String()
}
