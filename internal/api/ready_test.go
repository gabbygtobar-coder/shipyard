package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gabbygtobar-coder/shipyard/internal/store"
)

type readyFunc func(context.Context) error

func (f readyFunc) Ready(ctx context.Context) error { return f(ctx) }

type pingStore struct {
	store.Store
	err error
}

func (p pingStore) Ping(context.Context) error { return p.err }

func TestReadyzChecksStoreAndRuntime(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st := store.NewMemory()

	okHandler := NewHandler(st, nil, logger, nil, WithReadyCheck(readyFunc(func(context.Context) error { return nil })))
	rec := doRequest(okHandler, http.MethodGet, "/readyz", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("ready %d %s", rec.Code, rec.Body.String())
	}
	var body readyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Status != "ok" {
		t.Fatalf("body %s", rec.Body.String())
	}

	runtimeDown := errors.New("docker info failed")
	badRuntime := NewHandler(st, nil, logger, nil, WithReadyCheck(readyFunc(func(context.Context) error { return runtimeDown })))
	rec = doRequest(badRuntime, http.MethodGet, "/readyz", "", "")
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "runtime") || strings.Contains(rec.Body.String(), "docker info") {
		t.Fatalf("runtime down %d %s", rec.Code, rec.Body.String())
	}

	badStore := NewHandler(pingStore{Store: st, err: errors.New("db closed")}, nil, logger, nil, WithReadyCheck(readyFunc(func(context.Context) error { return nil })))
	rec = doRequest(badStore, http.MethodGet, "/readyz", "", "")
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "store") || strings.Contains(rec.Body.String(), "db closed") {
		t.Fatalf("store down %d %s", rec.Code, rec.Body.String())
	}

	live := doRequest(badStore, http.MethodGet, "/healthz", "", "")
	if live.Code != http.StatusOK || !strings.Contains(live.Body.String(), `"ok"`) {
		t.Fatalf("liveness should ignore the store, got %d %s", live.Code, live.Body.String())
	}
}

func TestRequestIDIsLoggedAndEchoed(t *testing.T) {
	t.Parallel()
	var logs strings.Builder
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	handler := NewHandler(store.NewMemory(), nil, logger, nil)
	req := httptestNew(http.MethodGet, "/healthz", "")
	req.Header.Set("X-Request-ID", "abc12345")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Header().Get("X-Request-ID") != "abc12345" {
		t.Fatalf("response id %q", rec.Header().Get("X-Request-ID"))
	}
	if !strings.Contains(logs.String(), "request_id=abc12345") {
		t.Fatalf("log %s", logs.String())
	}

	logs.Reset()
	req = httptestNew(http.MethodGet, "/healthz", "")
	req.Header.Set("X-Request-ID", "bad id")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	got := rec.Header().Get("X-Request-ID")
	if len(got) < 16 || strings.Contains(got, " ") {
		t.Fatalf("generated id %q", got)
	}
	if !strings.Contains(logs.String(), "request_id="+got) {
		t.Fatalf("log %s", logs.String())
	}
}
