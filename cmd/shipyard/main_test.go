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
	"github.com/gabbygtobar-coder/shipyard/internal/config"
	"github.com/gabbygtobar-coder/shipyard/internal/store"
)

func testAPI(t *testing.T) *httptest.Server {
	t.Helper()
	st := store.NewMemoryWithClock(func() time.Time {
		return time.Date(2026, 11, 11, 3, 4, 5, 0, time.UTC)
	})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(api.NewHandler(st, logger))
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
		{name: "unknown", args: []string{"deploy"}, want: 2},
		{name: "apps only", args: []string{"apps"}, want: 2},
		{name: "unknown apps", args: []string{"apps", "delete"}, want: 2},
		{name: "create help", args: []string{"apps", "create", "-h"}, want: 0},
		{name: "create missing flags", args: []string{"apps", "create"}, want: 2},
		{name: "create missing repo", args: []string{"apps", "create", "--name", "demo"}, want: 2},
		{name: "list extra", args: []string{"apps", "list", "extra"}, want: 2},
		{name: "get missing", args: []string{"apps", "get"}, want: 2},
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
