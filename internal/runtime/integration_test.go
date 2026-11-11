package runtime

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"github.com/gabbygtobar-coder/shipyard/internal/build"
	"github.com/gabbygtobar-coder/shipyard/internal/proc"
	"github.com/gabbygtobar-coder/shipyard/internal/proxy"
	"github.com/gabbygtobar-coder/shipyard/internal/route"
	"github.com/gabbygtobar-coder/shipyard/internal/store"
)

func TestIntegrationHello(t *testing.T) {
	if os.Getenv("SHIPYARD_INTEGRATION") == "" {
		t.Skip("set SHIPYARD_INTEGRATION=1 to build and run examples/hello")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatalf("SHIPYARD_INTEGRATION=1 but docker is not available: %v", err)
	}
	_, file, _, ok := goruntime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Join(filepath.Dir(file), "..", "..", "examples", "hello")
	info, err := os.Stat(filepath.Join(dir, "Dockerfile"))
	if err != nil || info.IsDir() {
		t.Fatalf("examples/hello Dockerfile: %v", err)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}

	const (
		tag  = "shipyard/hello:0123456789ab"
		name = "shipyard-hello-dep_1"
	)
	cleanup := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = proc.Run(ctx, "docker", []string{"rm", "-f", name}, proc.Spec{})
		_ = proc.Run(ctx, "docker", []string{"rmi", "-f", tag}, proc.Spec{})
	}
	cleanup()
	t.Cleanup(cleanup)

	buildCtx, buildCancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer buildCancel()
	var buildLogs strings.Builder
	if err := build.NewDocker().Build(buildCtx, abs, tag, &buildLogs); err != nil {
		t.Fatalf("build: %v\n%s", err, buildLogs.String())
	}
	if buildLogs.Len() == 0 {
		t.Fatal("docker build produced no log output")
	}

	st := store.NewMemory()
	if _, err := st.CreateApp(context.Background(), "hello", "https://github.com/acme/hello"); err != nil {
		t.Fatal(err)
	}
	dep, err := st.CreateDeployment(context.Background(), "hello", store.DeploymentStatusStarting)
	if err != nil {
		t.Fatal(err)
	}
	if dep.ID != "dep_1" {
		t.Fatalf("deployment id %s", dep.ID)
	}
	routes := route.NewTable()
	sup, err := NewSupervisor(SupervisorConfig{
		Store:         st,
		Runtime:       NewDocker(),
		Checker:       HTTPChecker{Path: "/", Interval: 200 * time.Millisecond},
		Routes:        routes,
		Port:          DefaultContainerPort,
		MemoryBytes:   256 << 20,
		CPUMilli:      500,
		HealthTimeout: 45 * time.Second,
		PublicURL: func(app string) string {
			url, urlErr := proxy.PublicURL(":8081", app)
			if urlErr != nil {
				return ""
			}
			return url
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	releaseCtx, releaseCancel := context.WithTimeout(context.Background(), time.Minute)
	defer releaseCancel()
	var runLogs strings.Builder
	if err := sup.Release(releaseCtx, "hello", dep.ID, tag, &runLogs); err != nil {
		logCtx, logCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer logCancel()
		out, _ := proc.Output(logCtx, "docker", []string{"logs", name}, proc.Spec{})
		t.Fatalf("release: %v\n%s\ncontainer logs:\n%s", err, runLogs.String(), out)
	}

	handler := proxy.New(routes, nil)
	assertProxy(t, handler, "http://hello.localhost/", "hello.localhost", "hello from shipyard")
	assertProxy(t, handler, "http://127.0.0.1:8081/apps/hello/", "127.0.0.1:8081", "hello from shipyard")

	if _, err := sup.Stop(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "http://hello.localhost/", nil)
	req.Host = "hello.localhost"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("after stop %d %s", rec.Code, rec.Body.String())
	}
}

func assertProxy(t *testing.T, handler http.Handler, rawURL, host, want string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, rawURL, nil)
	req.Host = host
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), want) {
		t.Fatalf("%s %d %s", rawURL, rec.Code, rec.Body.String())
	}
}
