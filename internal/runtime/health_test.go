package runtime

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestHTTPCheckerBecomesHealthy(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.Error(w, "path", http.StatusBadRequest)
			return
		}
		if hits.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	checker := HTTPChecker{Path: "/healthz", Interval: 10 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := checker.Wait(ctx, srv.URL); err != nil {
		t.Fatal(err)
	}
	if hits.Load() < 2 {
		t.Fatalf("hits %d", hits.Load())
	}
}

func TestHTTPCheckerTimesOut(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)

	checker := HTTPChecker{Path: "/", Interval: 15 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	err := checker.Wait(ctx, srv.URL)
	if !errors.Is(err, ErrHealthTimeout) {
		t.Fatalf("error %v", err)
	}
}

func TestHTTPCheckerRejectsBadEndpoint(t *testing.T) {
	t.Parallel()
	err := HTTPChecker{}.Wait(context.Background(), "not a url")
	if !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("error %v", err)
	}
}

func TestFakeChecker(t *testing.T) {
	t.Parallel()
	checker := &FakeChecker{Err: ErrHealthTimeout}
	err := checker.Wait(context.Background(), "http://127.0.0.1:9")
	if !errors.Is(err, ErrHealthTimeout) || len(checker.Calls) != 1 {
		t.Fatalf("error %v calls %#v", err, checker.Calls)
	}
}
