package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gabbygtobar-coder/shipyard/internal/api"
	"github.com/gabbygtobar-coder/shipyard/internal/build"
	"github.com/gabbygtobar-coder/shipyard/internal/config"
	"github.com/gabbygtobar-coder/shipyard/internal/deploy"
	"github.com/gabbygtobar-coder/shipyard/internal/source"
	"github.com/gabbygtobar-coder/shipyard/internal/store"
)

func testAPI(t *testing.T) *httptest.Server {
	t.Helper()
	st := store.NewMemoryWithClock(func() time.Time {
		return time.Date(2026, 11, 11, 3, 4, 5, 0, time.UTC)
	})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	runner, err := deploy.New(deploy.Config{
		Store:        st,
		Cloner:       &source.Fake{Files: map[string]string{"Dockerfile": "FROM scratch\n"}},
		Builder:      &build.Fake{Log: "built\n"},
		BuildTimeout: 5 * time.Second,
		CloneTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runner.Stop)
	srv := httptest.NewServer(api.NewHandler(st, runner, logger, nil))
	t.Cleanup(srv.Close)
	return srv
}

func runCLI(t *testing.T, args []string, getenv func(string) string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(args, getenv, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestCLICreateListGet(t *testing.T) {
	t.Parallel()
	srv := testAPI(t)
	getenv := func(string) string { return "" }
	args := []string{"--api-url", srv.URL, "apps", "create", "--name", " demo ", "--repo", "https://github.com/acme/demo.git"}
	code, stdout, stderr := runCLI(t, args, getenv)
	if code != 0 {
		t.Fatalf("create code %d stderr %s", code, stderr)
	}
	var created store.App
	if err := json.Unmarshal([]byte(stdout), &created); err != nil {
		t.Fatalf("stdout %s: %v", stdout, err)
	}
	if created.Name != "demo" || created.RepoURL != "https://github.com/acme/demo" {
		t.Fatalf("created %#v", created)
	}

	code, stdout, stderr = runCLI(t, []string{"--api-url", srv.URL, "apps", "list"}, getenv)
	if code != 0 {
		t.Fatalf("list code %d stderr %s", code, stderr)
	}
	var list appListResponse
	if err := json.Unmarshal([]byte(stdout), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Apps) != 1 || list.Apps[0].Name != "demo" {
		t.Fatalf("list %#v", list)
	}

	code, stdout, stderr = runCLI(t, []string{"--api-url", srv.URL, "apps", "get", "demo"}, getenv)
	if code != 0 {
		t.Fatalf("get code %d stderr %s", code, stderr)
	}
	var got store.App
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "demo" || !got.CreatedAt.Equal(created.CreatedAt) {
		t.Fatalf("get %#v", got)
	}
}

func TestCLIEvents(t *testing.T) {
	t.Parallel()
	srv := testAPI(t)
	getenv := func(string) string { return "" }
	code, _, stderr := runCLI(t, []string{"--api-url", srv.URL, "apps", "create", "--name", "demo", "--repo", "https://github.com/acme/demo"}, getenv)
	if code != 0 {
		t.Fatalf("create %d %s", code, stderr)
	}
	code, stdout, stderr := runCLI(t, []string{"--api-url", srv.URL, "events", "demo"}, getenv)
	if code != 0 || !strings.Contains(stdout, `"events"`) {
		t.Fatalf("events code %d stdout %s stderr %s", code, stdout, stderr)
	}
	code, _, stderr = runCLI(t, []string{"events"}, getenv)
	if code != 2 || !strings.Contains(stderr, "usage:") {
		t.Fatalf("usage code %d stderr %s", code, stderr)
	}
}

func TestCLIAPIURLFlagOverridesEnv(t *testing.T) {
	t.Parallel()
	srv := testAPI(t)
	getenv := func(key string) string {
		if key == config.EnvAPIURL {
			return "http://127.0.0.1:1"
		}
		return ""
	}
	code, _, stderr := runCLI(t, []string{"--api-url", srv.URL, "apps", "list"}, getenv)
	if code != 0 {
		t.Fatalf("code %d stderr %s", code, stderr)
	}

	code, _, stderr = runCLI(t, []string{"apps", "list"}, getenv)
	if code != 1 || !strings.Contains(stderr, "error:") {
		t.Fatalf("env failure code %d stderr %s", code, stderr)
	}
}

func TestCLIUsesEnvURL(t *testing.T) {
	t.Parallel()
	srv := testAPI(t)
	getenv := func(key string) string {
		if key == config.EnvAPIURL {
			return srv.URL + "/"
		}
		return ""
	}
	code, stdout, stderr := runCLI(t, []string{"apps", "create", "--name", "demo", "--repo", "https://github.com/acme/demo"}, getenv)
	if code != 0 {
		t.Fatalf("code %d stderr %s", code, stderr)
	}
	if !strings.Contains(stdout, `"name": "demo"`) {
		t.Fatalf("stdout %s", stdout)
	}
}

func TestCLIUsage(t *testing.T) {
	t.Parallel()
	getenv := func(string) string { return "" }
	tests := []struct {
		name string
		args []string
		want int
	}{
		{name: "no args", args: nil, want: 2},
		{name: "help", args: []string{"-h"}, want: 0},
		{name: "unknown", args: []string{"reboot"}, want: 2},
		{name: "deploy missing", args: []string{"deploy"}, want: 2},
		{name: "deploy help", args: []string{"deploy", "-h"}, want: 0},
		{name: "deploy extra", args: []string{"deploy", "demo", "extra"}, want: 2},
		{name: "deployments missing", args: []string{"deployments"}, want: 2},
		{name: "logs missing", args: []string{"logs"}, want: 2},
		{name: "logs help", args: []string{"logs", "-h"}, want: 0},
		{name: "apps only", args: []string{"apps"}, want: 2},
		{name: "unknown apps", args: []string{"apps", "delete"}, want: 2},
		{name: "create help", args: []string{"apps", "create", "-h"}, want: 0},
		{name: "create missing flags", args: []string{"apps", "create"}, want: 2},
		{name: "create missing repo", args: []string{"apps", "create", "--name", "demo"}, want: 2},
		{name: "list extra", args: []string{"apps", "list", "extra"}, want: 2},
		{name: "get missing", args: []string{"apps", "get"}, want: 2},
		{name: "url missing", args: []string{"apps", "url"}, want: 2},
		{name: "url help", args: []string{"apps", "url", "-h"}, want: 0},
		{name: "stop missing", args: []string{"stop"}, want: 2},
		{name: "stop help", args: []string{"stop", "-h"}, want: 0},
		{name: "flag after command", args: []string{"apps", "list", "--api-url", "http://example.com"}, want: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			code, _, stderr := runCLI(t, tt.args, getenv)
			if code != tt.want {
				t.Fatalf("code %d stderr %s, want %d", code, stderr, tt.want)
			}
			if tt.want == 0 && !strings.Contains(stderr, "usage:") && !strings.Contains(stderr, "Usage:") {
				t.Fatalf("help stderr %s", stderr)
			}
		})
	}
}

func TestCLIServerError(t *testing.T) {
	t.Parallel()
	srv := testAPI(t)
	getenv := func(string) string { return "" }
	code, _, stderr := runCLI(t, []string{"--api-url", srv.URL, "apps", "create", "--name", "Demo", "--repo", "https://github.com/acme/demo"}, getenv)
	if code != 1 || !strings.Contains(stderr, "name must be") {
		t.Fatalf("code %d stderr %s", code, stderr)
	}
	code, _, stderr = runCLI(t, []string{"--api-url", srv.URL, "apps", "get", "missing"}, getenv)
	if code != 1 || !strings.Contains(stderr, "app not found") || !strings.Contains(stderr, "HTTP 404") {
		t.Fatalf("code %d stderr %s", code, stderr)
	}
}

func TestCLIDeployListAndLogs(t *testing.T) {
	t.Parallel()
	srv := testAPI(t)
	getenv := func(string) string { return "" }
	code, _, stderr := runCLI(t, []string{"--api-url", srv.URL, "apps", "create", "--name", "demo", "--repo", "https://github.com/acme/demo"}, getenv)
	if code != 0 {
		t.Fatalf("create code %d stderr %s", code, stderr)
	}
	code, stdout, stderr := runCLI(t, []string{"--api-url", srv.URL, "deploy", "demo", "--ref", "main"}, getenv)
	if code != 0 {
		t.Fatalf("deploy code %d stderr %s", code, stderr)
	}
	var dep store.Deployment
	if err := json.Unmarshal([]byte(stdout), &dep); err != nil {
		t.Fatalf("stdout %s: %v", stdout, err)
	}
	if dep.ID != "dep_1" || dep.Status != store.DeploymentStatusQueued || dep.Ref != "main" {
		t.Fatalf("deploy = %#v", dep)
	}

	code, stdout, stderr = runCLI(t, []string{"--api-url", srv.URL, "deployments", "list", "demo"}, getenv)
	if code != 0 {
		t.Fatalf("list code %d stderr %s", code, stderr)
	}
	var list deploymentListResponse
	if err := json.Unmarshal([]byte(stdout), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Deployments) != 1 || list.Deployments[0].ID != "dep_1" {
		t.Fatalf("list %#v", list)
	}

	code, stdout, stderr = runCLI(t, []string{"--api-url", srv.URL, "logs", dep.ID, "--follow"}, getenv)
	if code != 0 {
		t.Fatalf("logs code %d stderr %s stdout %s", code, stderr, stdout)
	}
	if !strings.Contains(stdout, "built") || !strings.Contains(stdout, "shipyard/demo:0123456789ab") {
		t.Fatalf("logs stdout %s", stdout)
	}

	code, _, stderr = runCLI(t, []string{"--api-url", srv.URL, "logs", "dep_9"}, getenv)
	if code != 1 || !strings.Contains(stderr, "deployment not found") {
		t.Fatalf("missing logs code %d stderr %s", code, stderr)
	}
}

type stopStub struct {
	st *store.Memory
	id string
}

func (s stopStub) Stop(ctx context.Context, app string) (string, error) {
	stopped := store.DeploymentStatusStopped
	if _, err := s.st.UpdateDeployment(ctx, s.id, store.DeploymentUpdate{Status: &stopped}); err != nil {
		return "", err
	}
	if _, err := s.st.SetAppStatus(ctx, app, store.AppStatusStopped, ""); err != nil {
		return "", err
	}
	return s.id, nil
}

func TestCLIURLAndStop(t *testing.T) {
	t.Parallel()
	st := store.NewMemory()
	if _, err := st.CreateApp(context.Background(), "demo", "https://github.com/acme/demo"); err != nil {
		t.Fatal(err)
	}
	dep, err := st.CreateDeployment(context.Background(), "demo", store.DeploymentStatusRunning)
	if err != nil {
		t.Fatal(err)
	}
	const publicURL = "http://demo.localhost:8081/"
	if _, err := st.SetAppStatus(context.Background(), "demo", store.AppStatusRunning, publicURL); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(api.NewHandler(st, nil, logger, stopStub{st: st, id: dep.ID}))
	t.Cleanup(srv.Close)
	getenv := func(string) string { return "" }

	code, stdout, stderr := runCLI(t, []string{"--api-url", srv.URL, "apps", "url", "demo"}, getenv)
	if code != 0 || stdout != publicURL+"\n" {
		t.Fatalf("url code %d stdout %q stderr %s", code, stdout, stderr)
	}
	code, stdout, stderr = runCLI(t, []string{"--api-url", srv.URL, "stop", "demo"}, getenv)
	if code != 0 {
		t.Fatalf("stop code %d stderr %s", code, stderr)
	}
	var app store.App
	if err := json.Unmarshal([]byte(stdout), &app); err != nil {
		t.Fatal(err)
	}
	if app.Status != store.AppStatusStopped || app.URL != "" {
		t.Fatalf("stopped %#v", app)
	}
	code, _, stderr = runCLI(t, []string{"--api-url", srv.URL, "apps", "url", "demo"}, getenv)
	if code != 1 || !strings.Contains(stderr, "not running") {
		t.Fatalf("url after stop code %d stderr %s", code, stderr)
	}
}

func TestAPIClientErrorBody(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTeapot)
		_, _ = io.WriteString(w, `{"error":"nope"}`)
	}))
	t.Cleanup(srv.Close)

	client := newAPIClient(srv.URL)
	_, err := client.getApp(context.Background(), "demo")
	var apiErr *apiError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error %T %v", err, err)
	}
	if apiErr.Status != http.StatusTeapot || apiErr.Message != "nope" {
		t.Fatalf("api error %#v", apiErr)
	}
}
