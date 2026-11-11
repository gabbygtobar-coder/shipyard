package proxy

import (
	"bytes"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gabbygtobar-coder/shipyard/internal/route"
)

func TestProxyHostAndPathRouting(t *testing.T) {
	t.Parallel()
	var gotPath, gotQuery, gotHost string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotHost = r.Host
		_, _ = io.WriteString(w, "hello from "+r.URL.Path)
	}))
	t.Cleanup(backend.Close)

	routes := route.NewTable()
	routes.Swap("hello", route.Target{Endpoint: backend.URL, InstanceID: "abc", DeploymentID: "dep_1"})
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	handler := New(routes, logger)

	req := httptest.NewRequest(http.MethodGet, "http://hello.localhost/hi?x=1", nil)
	req.Host = "hello.localhost"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "hello from /hi") {
		t.Fatalf("host route %d %s", rec.Code, rec.Body.String())
	}
	if gotPath != "/hi" || gotQuery != "x=1" {
		t.Fatalf("backend saw path %q query %q", gotPath, gotQuery)
	}
	if !strings.Contains(logs.String(), "app=hello") || !strings.Contains(logs.String(), "status=200") || !strings.Contains(logs.String(), "latency_ms=") {
		t.Fatalf("log %s", logs.String())
	}

	logs.Reset()
	req = httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8081/apps/hello/hi?x=2", nil)
	req.Host = "127.0.0.1:8081"
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || gotPath != "/hi" || gotQuery != "x=2" {
		t.Fatalf("path route %d backend %s?%s body %s", rec.Code, gotPath, gotQuery, rec.Body.String())
	}
	if gotHost == "hello.localhost" {
		t.Fatal("path route kept the public host")
	}

	req = httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8081/apps/hello", nil)
	req.Host = "127.0.0.1:8081"
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || gotPath != "/" {
		t.Fatalf("bare app path %d %s", rec.Code, gotPath)
	}
}

func TestProxyMissingAndUnreachable(t *testing.T) {
	t.Parallel()
	routes := route.NewTable()
	var logs bytes.Buffer
	handler := New(routes, slog.New(slog.NewTextHandler(&logs, nil)))

	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "no application") {
		t.Fatalf("missing %d %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "http://demo.localhost/", nil)
	req.Host = "demo.localhost:8081"
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "app demo is not running") {
		t.Fatalf("idle %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(logs.String(), "app=demo") || !strings.Contains(logs.String(), "status=503") {
		t.Fatalf("log %s", logs.String())
	}

	req = httptest.NewRequest(http.MethodGet, "http://127.0.0.1/apps/No-Name/", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad name %d %s", rec.Code, rec.Body.String())
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	routes.Swap("demo", route.Target{Endpoint: "http://" + addr, InstanceID: "gone", DeploymentID: "dep_1"})
	req = httptest.NewRequest(http.MethodGet, "http://demo.localhost/", nil)
	req.Host = "demo.localhost"
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "bad gateway") {
		t.Fatalf("down %d %s", rec.Code, rec.Body.String())
	}
}

func TestProxySwapsBackend(t *testing.T) {
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
	handler := New(routes, nil)
	routes.Swap("demo", route.Target{Endpoint: blue.URL, InstanceID: "blue", DeploymentID: "dep_1"})
	if body := proxyBody(t, handler, "demo"); body != "blue" {
		t.Fatalf("body %q", body)
	}
	routes.Swap("demo", route.Target{Endpoint: green.URL, InstanceID: "green", DeploymentID: "dep_2"})
	if body := proxyBody(t, handler, "demo"); body != "green" {
		t.Fatalf("body %q", body)
	}
}

func TestPublicURL(t *testing.T) {
	t.Parallel()
	got, err := PublicURL(":8081", "hello")
	if err != nil || got != "http://hello.localhost:8081/" {
		t.Fatalf("url %q %v", got, err)
	}
	got, err = PublicURL("127.0.0.1:80", "hello")
	if err != nil || got != "http://hello.localhost/" {
		t.Fatalf("port 80 %q %v", got, err)
	}
	if _, err := PublicURL("8081", "hello"); err == nil {
		t.Fatal("missing host was accepted")
	}
	if _, err := PublicURL(":8081", "Hello"); err == nil {
		t.Fatal("bad app was accepted")
	}
}

func proxyBody(t *testing.T, handler http.Handler, app string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "http://"+app+".localhost/", nil)
	req.Host = app + ".localhost"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}
