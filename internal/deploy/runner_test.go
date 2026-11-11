package deploy

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gabbygtobar-coder/shipyard/internal/build"
	"github.com/gabbygtobar-coder/shipyard/internal/runtime"
	"github.com/gabbygtobar-coder/shipyard/internal/source"
	"github.com/gabbygtobar-coder/shipyard/internal/store"
)

const (
	testSHA = "0123456789abcdef0123456789abcdef01234567"
	testTag = "shipyard/demo:0123456789ab"
)

func dockerfile() map[string]string {
	return map[string]string{"Dockerfile": "FROM scratch\n"}
}

func startRunner(t *testing.T, st store.Store, cloner source.Cloner, builder build.Builder, mutate func(*Config)) (*Runner, context.CancelFunc) {
	t.Helper()
	if st == nil {
		st = store.NewMemory()
	}
	cfg := Config{
		Store:        st,
		Cloner:       cloner,
		Builder:      builder,
		Concurrency:  2,
		QueueSize:    8,
		BuildTimeout: 5 * time.Second,
		CloneTimeout: 2 * time.Second,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	runner, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := runner.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		runner.Stop()
	})
	return runner, cancel
}

func mustApp(t *testing.T, st store.Store) {
	t.Helper()
	if _, err := st.CreateApp(context.Background(), "demo", "https://github.com/acme/demo"); err != nil {
		t.Fatal(err)
	}
}

func waitStatus(t *testing.T, st store.Store, id, want string) store.Deployment {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last store.Deployment
	for time.Now().Before(deadline) {
		dep, err := st.GetDeployment(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		last = dep
		if dep.Status == want {
			return dep
		}
		if store.IsTerminalStatus(dep.Status) && dep.Status != want {
			t.Fatalf("deployment %s reached %s (%s), want %s", id, dep.Status, dep.Error, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("deployment %s stuck in %s (%s), want %s", id, last.Status, last.Error, want)
	return store.Deployment{}
}

func logText(t *testing.T, st store.Store, id string) string {
	t.Helper()
	snap, err := st.ReadDeploymentLog(context.Background(), id, 0)
	if err != nil {
		t.Fatal(err)
	}
	return string(snap.Data)
}

func TestRunnerSuccess(t *testing.T) {
	t.Parallel()
	st := store.NewMemory()
	cloner := &source.Fake{Files: dockerfile(), SHA: strings.ToUpper(testSHA)}
	builder := &build.Fake{Log: "docker output\n"}
	runner, _ := startRunner(t, st, cloner, builder, nil)
	mustApp(t, st)

	queued, err := runner.StartBuild(context.Background(), "demo", "main")
	if err != nil {
		t.Fatal(err)
	}
	if queued.Status != store.DeploymentStatusQueued || queued.Ref != "main" || queued.ID != "dep_1" {
		t.Fatalf("queued = %#v", queued)
	}
	dep := waitStatus(t, st, queued.ID, store.DeploymentStatusSucceeded)
	if dep.ImageTag != testTag || dep.CommitSHA != testSHA || dep.Error != "" {
		t.Fatalf("deployment = %#v", dep)
	}
	if dep.StartedAt == nil || dep.FinishedAt == nil || dep.FinishedAt.Before(*dep.StartedAt) {
		t.Fatalf("timestamps start=%v finish=%v", dep.StartedAt, dep.FinishedAt)
	}
	if dep.StartedAt.Location() != time.UTC || dep.FinishedAt.Location() != time.UTC {
		t.Fatal("timestamps are not UTC")
	}
	log := logText(t, st, dep.ID)
	for _, want := range []string{"cloning https://github.com/acme/demo ref main", "commit " + testSHA, "building " + testTag, "docker output", "built " + testTag} {
		if !strings.Contains(log, want) {
			t.Fatalf("log %q missing %q", log, want)
		}
	}
	_, dests, _ := cloner.Snapshot()
	if len(dests) != 1 {
		t.Fatalf("clone dests = %#v", dests)
	}
	if _, err := os.Stat(dests[0]); !os.IsNotExist(err) {
		t.Fatalf("temp dir %s still exists (%v)", dests[0], err)
	}
	calls, tags := builder.Snapshot()
	if calls != 1 || len(tags) != 1 || tags[0] != testTag {
		t.Fatalf("builder calls=%d tags=%v", calls, tags)
	}
}

func TestRunnerDefaultRef(t *testing.T) {
	t.Parallel()
	st := store.NewMemory()
	cloner := &source.Fake{Files: dockerfile()}
	runner, _ := startRunner(t, st, cloner, &build.Fake{}, nil)
	mustApp(t, st)
	dep, err := runner.StartBuild(context.Background(), "demo", "  ")
	if err != nil {
		t.Fatal(err)
	}
	waitStatus(t, st, dep.ID, store.DeploymentStatusSucceeded)
	if !strings.Contains(logText(t, st, dep.ID), "ref default") {
		t.Fatalf("log = %s", logText(t, st, dep.ID))
	}
	_, _, refs := cloner.Snapshot()
	if len(refs) != 1 || refs[0] != "" {
		t.Fatalf("refs = %#v", refs)
	}
}

func TestRunnerMissingDockerfile(t *testing.T) {
	t.Parallel()
	st := store.NewMemory()
	builder := &build.Fake{}
	cloner := &source.Fake{Files: map[string]string{"README.md": "hi\n"}}
	runner, _ := startRunner(t, st, cloner, builder, nil)
	mustApp(t, st)
	dep, err := runner.StartBuild(context.Background(), "demo", "main")
	if err != nil {
		t.Fatal(err)
	}
	got := waitStatus(t, st, dep.ID, store.DeploymentStatusFailed)
	if got.Error != msgDockerfileMissing || got.CommitSHA != testSHA || got.ImageTag != "" {
		t.Fatalf("deployment = %#v", got)
	}
	calls, _ := builder.Snapshot()
	if calls != 0 {
		t.Fatalf("builder calls = %d", calls)
	}
	if !strings.Contains(logText(t, st, dep.ID), msgDockerfileMissing) {
		t.Fatalf("log = %s", logText(t, st, dep.ID))
	}
}

func TestRunnerDockerfileSymlink(t *testing.T) {
	t.Parallel()
	st := store.NewMemory()
	builder := &build.Fake{}
	runner, _ := startRunner(t, st, symlinkDockerfile{}, builder, nil)
	mustApp(t, st)
	dep, err := runner.StartBuild(context.Background(), "demo", "main")
	if err != nil {
		t.Fatal(err)
	}
	got := waitStatus(t, st, dep.ID, store.DeploymentStatusFailed)
	if got.Error != msgDockerfileNotFile {
		t.Fatalf("error = %s", got.Error)
	}
	calls, _ := builder.Snapshot()
	if calls != 0 {
		t.Fatal("builder ran")
	}
}

func TestRunnerRejectsOutsideSymlink(t *testing.T) {
	t.Parallel()
	st := store.NewMemory()
	builder := &build.Fake{}
	runner, _ := startRunner(t, st, outsideSymlink{}, builder, nil)
	mustApp(t, st)
	dep, err := runner.StartBuild(context.Background(), "demo", "main")
	if err != nil {
		t.Fatal(err)
	}
	got := waitStatus(t, st, dep.ID, store.DeploymentStatusFailed)
	if got.Error != errOutsideSymlink.Error() {
		t.Fatalf("error = %s", got.Error)
	}
	calls, _ := builder.Snapshot()
	if calls != 0 {
		t.Fatal("builder ran")
	}
}

func TestRunnerAllowsInsideSymlink(t *testing.T) {
	t.Parallel()
	st := store.NewMemory()
	runner, _ := startRunner(t, st, insideSymlink{}, &build.Fake{}, nil)
	mustApp(t, st)
	dep, err := runner.StartBuild(context.Background(), "demo", "main")
	if err != nil {
		t.Fatal(err)
	}
	waitStatus(t, st, dep.ID, store.DeploymentStatusSucceeded)
}

func TestRunnerCloneFailure(t *testing.T) {
	t.Parallel()
	st := store.NewMemory()
	builder := &build.Fake{}
	cloner := &source.Fake{Err: errors.New("network down")}
	runner, _ := startRunner(t, st, cloner, builder, nil)
	mustApp(t, st)
	dep, err := runner.StartBuild(context.Background(), "demo", "main")
	if err != nil {
		t.Fatal(err)
	}
	got := waitStatus(t, st, dep.ID, store.DeploymentStatusFailed)
	if got.Error != msgCloneFailed || got.CommitSHA != "" {
		t.Fatalf("deployment = %#v", got)
	}
	if !strings.Contains(logText(t, st, dep.ID), "network down") {
		t.Fatalf("log = %s", logText(t, st, dep.ID))
	}
	calls, _ := builder.Snapshot()
	if calls != 0 {
		t.Fatal("builder ran")
	}
	_, dests, _ := cloner.Snapshot()
	dest := ""
	if len(dests) == 1 {
		dest = dests[0]
	}
	if dest == "" {
		t.Fatal("clone dest was not recorded")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(dest); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("temp dir %s was not removed", dest)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestRunnerBuildFailure(t *testing.T) {
	t.Parallel()
	st := store.NewMemory()
	builder := &build.Fake{Err: errors.New("dockerfile syntax"), Log: "step 1\n"}
	runner, _ := startRunner(t, st, &source.Fake{Files: dockerfile()}, builder, nil)
	mustApp(t, st)
	dep, err := runner.StartBuild(context.Background(), "demo", "main")
	if err != nil {
		t.Fatal(err)
	}
	got := waitStatus(t, st, dep.ID, store.DeploymentStatusFailed)
	if got.Error != msgBuildFailed || got.ImageTag != "" || got.CommitSHA != testSHA {
		t.Fatalf("deployment = %#v", got)
	}
	log := logText(t, st, dep.ID)
	if !strings.Contains(log, "step 1") || !strings.Contains(log, "dockerfile syntax") {
		t.Fatalf("log = %s", log)
	}
}

func TestRunnerInvalidSHA(t *testing.T) {
	t.Parallel()
	st := store.NewMemory()
	builder := &build.Fake{}
	runner, _ := startRunner(t, st, &source.Fake{SHA: "nope", Files: dockerfile()}, builder, nil)
	mustApp(t, st)
	dep, err := runner.StartBuild(context.Background(), "demo", "main")
	if err != nil {
		t.Fatal(err)
	}
	got := waitStatus(t, st, dep.ID, store.DeploymentStatusFailed)
	if got.Error != msgInvalidSHA {
		t.Fatalf("error = %s", got.Error)
	}
	calls, _ := builder.Snapshot()
	if calls != 0 {
		t.Fatal("builder ran")
	}
}

func TestRunnerBuildTimeout(t *testing.T) {
	t.Parallel()
	st := store.NewMemory()
	blocker := &build.Fake{Block: make(chan struct{})}
	runner, _ := startRunner(t, st, &source.Fake{Files: dockerfile()}, blocker, func(cfg *Config) {
		cfg.BuildTimeout = 200 * time.Millisecond
		cfg.CloneTimeout = 2 * time.Second
	})
	mustApp(t, st)
	dep, err := runner.StartBuild(context.Background(), "demo", "main")
	if err != nil {
		t.Fatal(err)
	}
	got := waitStatus(t, st, dep.ID, store.DeploymentStatusFailed)
	if got.Error != msgBuildTimedOut {
		t.Fatalf("error = %s log=%s", got.Error, logText(t, st, dep.ID))
	}
}

func TestRunnerCloneTimeout(t *testing.T) {
	t.Parallel()
	st := store.NewMemory()
	builder := &build.Fake{}
	cloner := &source.Fake{Block: make(chan struct{}), Files: dockerfile()}
	runner, _ := startRunner(t, st, cloner, builder, func(cfg *Config) {
		cfg.BuildTimeout = 5 * time.Second
		cfg.CloneTimeout = 200 * time.Millisecond
	})
	mustApp(t, st)
	dep, err := runner.StartBuild(context.Background(), "demo", "main")
	if err != nil {
		t.Fatal(err)
	}
	got := waitStatus(t, st, dep.ID, store.DeploymentStatusFailed)
	if got.Error != msgCloneTimedOut {
		t.Fatalf("error = %s", got.Error)
	}
	calls, _ := builder.Snapshot()
	if calls != 0 {
		t.Fatal("builder ran")
	}
}

func TestRunnerCancel(t *testing.T) {
	t.Parallel()
	st := store.NewMemory()
	entered := make(chan struct{})
	builder := &build.Fake{Hook: func(ctx context.Context, _, _ string, _ io.Writer) error {
		close(entered)
		<-ctx.Done()
		return ctx.Err()
	}}
	runner, cancel := startRunner(t, st, &source.Fake{Files: dockerfile()}, builder, nil)
	mustApp(t, st)
	dep, err := runner.StartBuild(context.Background(), "demo", "main")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("build did not start")
	}
	cancel()
	got := waitStatus(t, st, dep.ID, store.DeploymentStatusFailed)
	if got.Error != msgCanceled {
		t.Fatalf("error = %s", got.Error)
	}
}

func TestRunnerConcurrencyAndQueue(t *testing.T) {
	t.Parallel()
	st := store.NewMemory()
	entered := make(chan string, 2)
	release := make(chan struct{})
	builder := &build.Fake{Hook: func(ctx context.Context, _, tag string, logs io.Writer) error {
		entered <- tag
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-release:
		}
		_, _ = io.WriteString(logs, "done\n")
		return nil
	}}
	runner, _ := startRunner(t, st, &source.Fake{Files: dockerfile()}, builder, func(cfg *Config) {
		cfg.Concurrency = 1
		cfg.QueueSize = 1
	})
	mustApp(t, st)
	first, err := runner.StartBuild(context.Background(), "demo", "main")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first build did not start")
	}
	second, err := runner.StartBuild(context.Background(), "demo", "v1")
	if err != nil {
		t.Fatal(err)
	}
	queued := waitStatus(t, st, second.ID, store.DeploymentStatusQueued)
	if queued.Status != store.DeploymentStatusQueued {
		t.Fatalf("second = %s", queued.Status)
	}
	calls, _ := builder.Snapshot()
	if calls != 1 {
		t.Fatalf("builder calls = %d, want 1 while the queue holds the second job", calls)
	}
	if _, err := runner.StartBuild(context.Background(), "demo", "v2"); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("third error = %v, want ErrQueueFull", err)
	}
	listed, err := st.ListDeployments(context.Background(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 {
		t.Fatalf("stored %d deployments, want 2", len(listed))
	}
	close(release)
	waitStatus(t, st, first.ID, store.DeploymentStatusSucceeded)
	waitStatus(t, st, second.ID, store.DeploymentStatusSucceeded)
}

func TestRunnerStopCancelsQueuedWork(t *testing.T) {
	t.Parallel()
	st := store.NewMemory()
	entered := make(chan struct{})
	builder := &build.Fake{Hook: func(ctx context.Context, _, _ string, _ io.Writer) error {
		close(entered)
		<-ctx.Done()
		return ctx.Err()
	}}
	runner, _ := startRunner(t, st, &source.Fake{Files: dockerfile()}, builder, func(cfg *Config) {
		cfg.Concurrency = 1
		cfg.QueueSize = 2
	})
	mustApp(t, st)
	first, err := runner.StartBuild(context.Background(), "demo", "main")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("build did not start")
	}
	second, err := runner.StartBuild(context.Background(), "demo", "other")
	if err != nil {
		t.Fatal(err)
	}
	waitStatus(t, st, second.ID, store.DeploymentStatusQueued)
	runner.Stop()
	firstDone := waitStatus(t, st, first.ID, store.DeploymentStatusFailed)
	secondDone := waitStatus(t, st, second.ID, store.DeploymentStatusFailed)
	if firstDone.Error != msgCanceled || secondDone.Error != msgCanceled {
		t.Fatalf("errors = %q %q", firstDone.Error, secondDone.Error)
	}
	if _, err := runner.StartBuild(context.Background(), "demo", "main"); !errors.Is(err, ErrStopped) {
		t.Fatalf("enqueue after stop = %v", err)
	}
}

func TestRunnerPanicBecomesInternalError(t *testing.T) {
	t.Parallel()
	st := store.NewMemory()
	runner, _ := startRunner(t, st, panicCloner{}, &build.Fake{}, nil)
	mustApp(t, st)
	dep, err := runner.StartBuild(context.Background(), "demo", "main")
	if err != nil {
		t.Fatal(err)
	}
	got := waitStatus(t, st, dep.ID, store.DeploymentStatusFailed)
	if got.Error != msgInternal || strings.Contains(got.Error, "boom") {
		t.Fatalf("error = %s", got.Error)
	}
	again, err := runner.StartBuild(context.Background(), "demo", "main")
	if err != nil {
		t.Fatal(err)
	}
	second := waitStatus(t, st, again.ID, store.DeploymentStatusFailed)
	if second.Error != msgInternal {
		t.Fatalf("second error = %s", second.Error)
	}
}

func TestRunnerRejectsBadRefAndMissingApp(t *testing.T) {
	t.Parallel()
	st := store.NewMemory()
	runner, _ := startRunner(t, st, &source.Fake{Files: dockerfile()}, &build.Fake{}, nil)
	mustApp(t, st)
	if _, err := runner.StartBuild(context.Background(), "demo", "--upload-pack=evil"); err == nil {
		t.Fatal("expected invalid ref")
	}
	listed, err := st.ListDeployments(context.Background(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 0 {
		t.Fatalf("invalid ref created %#v", listed)
	}
	if _, err := runner.StartBuild(context.Background(), "missing", "main"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing app error = %v", err)
	}
}

type scriptReleaser struct {
	st    store.Store
	err   error
	calls int
}

func (s *scriptReleaser) Release(_ context.Context, appName, id, _ string, logs io.Writer) error {
	s.calls++
	_, _ = io.WriteString(logs, "released "+appName+"\n")
	if s.err != nil {
		return s.err
	}
	status := store.DeploymentStatusRunning
	url := "http://" + appName + ".localhost:8081/"
	now := time.Now().UTC()
	_, err := s.st.UpdateDeployment(context.Background(), id, store.DeploymentUpdate{
		Status:     &status,
		URL:        &url,
		FinishedAt: &now,
	})
	if err != nil {
		return err
	}
	_, err = s.st.SetAppStatus(context.Background(), appName, store.AppStatusRunning, url)
	return err
}

func TestRunnerReleaseRunning(t *testing.T) {
	t.Parallel()
	st := store.NewMemory()
	rel := &scriptReleaser{st: st}
	runner, _ := startRunner(t, st, &source.Fake{Files: dockerfile()}, &build.Fake{}, func(cfg *Config) {
		cfg.Releaser = rel
	})
	mustApp(t, st)
	dep, err := runner.StartBuild(context.Background(), "demo", "main")
	if err != nil {
		t.Fatal(err)
	}
	got := waitStatus(t, st, dep.ID, store.DeploymentStatusRunning)
	if got.URL != "http://demo.localhost:8081/" || got.ImageTag != testTag || rel.calls != 1 {
		t.Fatalf("deployment %#v calls %d", got, rel.calls)
	}
	log := logText(t, st, dep.ID)
	if !strings.Contains(log, "starting "+testTag) || !strings.Contains(log, "released demo") {
		t.Fatalf("log %s", log)
	}
	app, err := st.GetApp(context.Background(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	if app.Status != store.AppStatusRunning || app.URL != got.URL {
		t.Fatalf("app %#v", app)
	}
}

func TestRunnerReleaseHealthTimeout(t *testing.T) {
	t.Parallel()
	st := store.NewMemory()
	rel := &scriptReleaser{st: st, err: runtime.ErrHealthTimeout}
	runner, _ := startRunner(t, st, &source.Fake{Files: dockerfile()}, &build.Fake{}, func(cfg *Config) {
		cfg.Releaser = rel
	})
	mustApp(t, st)
	dep, err := runner.StartBuild(context.Background(), "demo", "main")
	if err != nil {
		t.Fatal(err)
	}
	got := waitStatus(t, st, dep.ID, store.DeploymentStatusFailed)
	if got.Error != runtime.ErrHealthTimeout.Error() || got.ImageTag != testTag {
		t.Fatalf("deployment %#v", got)
	}
	app, err := st.GetApp(context.Background(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	if app.Status != store.AppStatusIdle {
		t.Fatalf("app status %s", app.Status)
	}
}

func TestNewRequiresDependencies(t *testing.T) {
	t.Parallel()
	if _, err := New(Config{}); err == nil {
		t.Fatal("expected error")
	}
}

type panicCloner struct{}

func (panicCloner) Clone(context.Context, string, string, string, io.Writer) (string, error) {
	panic("boom")
}

type symlinkDockerfile struct{}

func (symlinkDockerfile) Clone(_ context.Context, _, _, dest string, _ io.Writer) (string, error) {
	target := filepath.Join(dest, "real.Dockerfile")
	if err := os.WriteFile(target, []byte("FROM scratch\n"), 0o600); err != nil {
		return "", err
	}
	if err := os.Symlink(target, filepath.Join(dest, "Dockerfile")); err != nil {
		return "", err
	}
	return testSHA, nil
}

type outsideSymlink struct{}

func (outsideSymlink) Clone(_ context.Context, _, _, dest string, _ io.Writer) (string, error) {
	if err := os.WriteFile(filepath.Join(dest, "Dockerfile"), []byte("FROM scratch\n"), 0o600); err != nil {
		return "", err
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(dest, "escape")); err != nil {
		return "", err
	}
	return testSHA, nil
}

type insideSymlink struct{}

func (insideSymlink) Clone(_ context.Context, _, _, dest string, _ io.Writer) (string, error) {
	if err := os.WriteFile(filepath.Join(dest, "Dockerfile"), []byte("FROM scratch\n"), 0o600); err != nil {
		return "", err
	}
	if err := os.Symlink("Dockerfile", filepath.Join(dest, "link")); err != nil {
		return "", err
	}
	return testSHA, nil
}
