package runtime_test

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
	"github.com/gabbygtobar-coder/shipyard/internal/deploy"
	"github.com/gabbygtobar-coder/shipyard/internal/proc"
	"github.com/gabbygtobar-coder/shipyard/internal/proxy"
	"github.com/gabbygtobar-coder/shipyard/internal/route"
	"github.com/gabbygtobar-coder/shipyard/internal/runtime"
	"github.com/gabbygtobar-coder/shipyard/internal/source"
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
		tagV1 = "shipyard/hello:111111111111"
		tagV2 = "shipyard/hello:222222222222"
	)
	names := []string{"shipyard-hello-dep_1", "shipyard-hello-dep_2", "shipyard-hello-dep_3", "shipyard-hello-dep_4"}
	cleanup := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		args := append([]string{"rm", "-f"}, names...)
		_ = proc.Run(ctx, "docker", args, proc.Spec{})
		_ = proc.Run(ctx, "docker", []string{"rmi", "-f", tagV1, tagV2}, proc.Spec{})
	}
	cleanup()
	t.Cleanup(cleanup)

	buildHello(t, abs, tagV1, "v1")
	buildHello(t, abs, tagV2, "v2")

	st := store.NewMemory()
	if _, err := st.CreateApp(context.Background(), "hello", "https://github.com/acme/hello"); err != nil {
		t.Fatal(err)
	}
	routes := route.NewTable()
	sup, err := runtime.NewSupervisor(runtime.SupervisorConfig{
		Store:         st,
		Runtime:       runtime.NewDocker(),
		Checker:       runtime.HTTPChecker{Path: "/", Interval: 200 * time.Millisecond},
		Routes:        routes,
		Port:          runtime.DefaultContainerPort,
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
	handler := proxy.New(routes, nil)
	first := releaseHello(t, st, sup, tagV1)
	if first.ID != "dep_1" {
		t.Fatalf("deployment id %s", first.ID)
	}
	assertProxy(t, handler, "http://hello.localhost/", "hello.localhost", "hello from shipyard")
	assertProxy(t, handler, "http://hello.localhost/", "hello.localhost", "version v1")
	assertProxy(t, handler, "http://127.0.0.1:8081/apps/hello/", "127.0.0.1:8081", "version v1")
	if _, err := st.RecordRelease(context.Background(), first.ID, 5); err != nil {
		t.Fatal(err)
	}

	second := releaseHello(t, st, sup, tagV2)
	assertProxy(t, handler, "http://hello.localhost/", "hello.localhost", "version v2")
	if _, err := st.RecordRelease(context.Background(), second.ID, 5); err != nil {
		t.Fatal(err)
	}

	runner, err := deploy.New(deploy.Config{
		Store:          st,
		Cloner:         &source.Fake{},
		Builder:        &build.Fake{},
		Releaser:       sup,
		ReleaseHistory: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	if err := runner.Start(runCtx); err != nil {
		t.Fatal(err)
	}
	defer runner.Stop()
	rolled, err := runner.Rollback(context.Background(), "hello", first.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitRunning(t, st, rolled.ID)
	waitRoute(t, routes, "hello", rolled.ID)
	assertProxy(t, handler, "http://hello.localhost/", "hello.localhost", "version v1")
	body := proxyBody(t, handler, "http://hello.localhost/", "hello.localhost")
	if strings.Contains(body, "version v2") {
		t.Fatalf("rollback still served v2: %s", body)
	}

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

func buildHello(t *testing.T, dir, tag, version string) {
	t.Helper()
	buildCtx, buildCancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer buildCancel()
	var buildLogs strings.Builder
	err := build.NewDocker().BuildWithArgs(buildCtx, dir, tag, map[string]string{"VERSION": version}, &buildLogs)
	if err != nil {
		t.Fatalf("build %s: %v\n%s", version, err, buildLogs.String())
	}
	if buildLogs.Len() == 0 {
		t.Fatal("docker build produced no log output")
	}
}

func releaseHello(t *testing.T, st store.Store, sup *runtime.Supervisor, tag string) store.Deployment {
	t.Helper()
	dep, err := st.CreateDeployment(context.Background(), "hello", store.DeploymentStatusStarting)
	if err != nil {
		t.Fatal(err)
	}
	releaseCtx, releaseCancel := context.WithTimeout(context.Background(), time.Minute)
	defer releaseCancel()
	var runLogs strings.Builder
	if err := sup.Release(releaseCtx, "hello", dep.ID, tag, &runLogs); err != nil {
		name := "shipyard-hello-" + dep.ID
		logCtx, logCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer logCancel()
		out, _ := proc.Output(logCtx, "docker", []string{"logs", name}, proc.Spec{})
		t.Fatalf("release %s: %v\n%s\ncontainer logs:\n%s", tag, err, runLogs.String(), out)
	}
	return dep
}

func waitRunning(t *testing.T, st store.Store, id string) {
	t.Helper()
	deadline := time.Now().Add(time.Minute)
	var last store.Deployment
	for time.Now().Before(deadline) {
		dep, err := st.GetDeployment(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		last = dep
		if dep.Status == store.DeploymentStatusRunning {
			return
		}
		if dep.Status == store.DeploymentStatusFailed {
			t.Fatalf("rollback failed: %s", dep.Error)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("rollback stuck in %s (%s)", last.Status, last.Error)
}

func waitRoute(t *testing.T, routes *route.Table, app, deploymentID string) {
	t.Helper()
	deadline := time.Now().Add(time.Minute)
	for time.Now().Before(deadline) {
		if current, ok := routes.Lookup(app); ok && current.DeploymentID == deploymentID {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("route did not move to %s", deploymentID)
}

func proxyBody(t *testing.T, handler http.Handler, rawURL, host string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, rawURL, nil)
	req.Host = host
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s %d %s", rawURL, rec.Code, rec.Body.String())
	}
	return rec.Body.String()
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
