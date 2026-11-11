package deploy

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gabbygtobar-coder/shipyard/internal/build"
	"github.com/gabbygtobar-coder/shipyard/internal/runtime"
	"github.com/gabbygtobar-coder/shipyard/internal/source"
	"github.com/gabbygtobar-coder/shipyard/internal/store"
)

func TestRunnerCoalescesQueuedPushes(t *testing.T) {
	t.Parallel()
	st := store.NewMemory()
	block := make(chan struct{})
	cloner := &source.Fake{Files: dockerfile(), Block: block, SHA: testSHA}
	runner, _ := startRunner(t, st, cloner, &build.Fake{}, func(cfg *Config) {
		cfg.Concurrency = 1
	})
	mustApp(t, st)
	const (
		sha1 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		sha2 = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		sha3 = "cccccccccccccccccccccccccccccccccccccccc"
	)
	first, coalesced, err := runner.StartPush(context.Background(), "demo", "main", sha1)
	if err != nil || coalesced {
		t.Fatalf("first coal %v err %v", coalesced, err)
	}
	waitClone(t, cloner, 1)
	second, coalesced, err := runner.StartPush(context.Background(), "demo", "main", sha2)
	if err != nil || coalesced || second.ID == first.ID {
		t.Fatalf("second %#v coal %v err %v", second, coalesced, err)
	}
	third, coalesced, err := runner.StartPush(context.Background(), "demo", "main", sha3)
	if err != nil || !coalesced || third.ID != second.ID || third.CommitSHA != sha3 || third.Ref != "main" {
		t.Fatalf("third %#v coal %v err %v", third, coalesced, err)
	}
	close(block)
	waitStatus(t, st, first.ID, store.DeploymentStatusSucceeded)
	waitStatus(t, st, second.ID, store.DeploymentStatusSucceeded)
	calls, _, refs := cloner.Snapshot()
	if calls != 2 || len(refs) != 2 || refs[0] != sha1 || refs[1] != sha3 {
		t.Fatalf("calls %d refs %#v", calls, refs)
	}
	listed, err := st.ListDeployments(context.Background(), "demo")
	if err != nil || len(listed) != 2 {
		t.Fatalf("deployments %#v %v", listed, err)
	}
}

func TestRunnerRollbackUsesPreviousImage(t *testing.T) {
	t.Parallel()
	st := store.NewMemory()
	cloner := &source.Fake{Files: dockerfile()}
	runner, _ := startRunner(t, st, cloner, &build.Fake{}, nil)
	mustApp(t, st)
	const (
		sha1 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		sha2 = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	)
	first := makeRunning(t, st, sha1, "shipyard/demo:"+sha1[:12])
	stopped := store.DeploymentStatusStopped
	if _, err := st.UpdateDeployment(context.Background(), first.ID, store.DeploymentUpdate{Status: &stopped}); err != nil {
		t.Fatal(err)
	}
	second := makeRunning(t, st, sha2, "shipyard/demo:"+sha2[:12])
	if _, err := runner.Rollback(context.Background(), "demo", second.ID); !errors.Is(err, ErrAlreadyCurrent) {
		t.Fatalf("current %v", err)
	}
	if _, err := runner.Rollback(context.Background(), "demo", "dep_9"); !errors.Is(err, ErrReleaseNotFound) {
		t.Fatalf("missing %v", err)
	}
	before, _, _ := cloner.Snapshot()
	rolled, err := runner.Rollback(context.Background(), "demo", "")
	if err != nil {
		t.Fatal(err)
	}
	got := waitStatus(t, st, rolled.ID, store.DeploymentStatusSucceeded)
	if got.RollbackOf != first.ID || got.ImageTag != first.ImageTag || got.CommitSHA != sha1 {
		t.Fatalf("rollback %#v", got)
	}
	if calls, _, _ := cloner.Snapshot(); calls != before {
		t.Fatalf("rollback cloned %d times", calls-before)
	}
	log := logText(t, st, got.ID)
	if !strings.Contains(log, "rolling back to "+first.ImageTag+" from "+first.ID) {
		t.Fatalf("log %s", log)
	}
	explicit, err := runner.Rollback(context.Background(), "demo", first.ID)
	if err != nil {
		t.Fatal(err)
	}
	got = waitStatus(t, st, explicit.ID, store.DeploymentStatusSucceeded)
	if got.ImageTag != first.ImageTag || got.RollbackOf != first.ID {
		t.Fatalf("explicit %#v", got)
	}
}

func TestRunnerFailedHealthKeepsCurrentDeployment(t *testing.T) {
	t.Parallel()
	st := store.NewMemory()
	rel := &gateReleaser{st: st}
	runner, _ := startRunner(t, st, &source.Fake{Files: dockerfile()}, &build.Fake{}, func(cfg *Config) {
		cfg.Releaser = rel
	})
	mustApp(t, st)
	first, err := runner.StartBuild(context.Background(), "demo", "main")
	if err != nil {
		t.Fatal(err)
	}
	running := waitStatus(t, st, first.ID, store.DeploymentStatusRunning)
	second, err := runner.StartBuild(context.Background(), "demo", "main")
	if err != nil {
		t.Fatal(err)
	}
	failed := waitStatus(t, st, second.ID, store.DeploymentStatusFailed)
	if failed.Error != runtime.ErrHealthTimeout.Error() {
		t.Fatalf("second %#v", failed)
	}
	still, err := st.GetDeployment(context.Background(), first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if still.Status != store.DeploymentStatusRunning || still.URL != running.URL {
		t.Fatalf("current %#v", still)
	}
	app, err := st.GetApp(context.Background(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	if app.Status != store.AppStatusRunning || app.URL != running.URL {
		t.Fatalf("app %#v", app)
	}
}

func TestRunnerPrunesOldImages(t *testing.T) {
	t.Parallel()
	st := store.NewMemory()
	cloner := &seqCloner{shas: []string{
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}}
	bin := &imageBin{}
	runner, _ := startRunner(t, st, cloner, &build.Fake{}, func(cfg *Config) {
		cfg.ReleaseHistory = 1
		cfg.Images = bin
		cfg.Concurrency = 1
	})
	mustApp(t, st)
	first, err := runner.StartBuild(context.Background(), "demo", "main")
	if err != nil {
		t.Fatal(err)
	}
	waitStatus(t, st, first.ID, store.DeploymentStatusSucceeded)
	second, err := runner.StartBuild(context.Background(), "demo", "main")
	if err != nil {
		t.Fatal(err)
	}
	waitStatus(t, st, second.ID, store.DeploymentStatusSucceeded)
	deadline := time.Now().Add(2 * time.Second)
	for len(bin.tags()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	got := bin.tags()
	if len(got) != 1 || got[0] != "shipyard/demo:aaaaaaaaaaaa" {
		t.Fatalf("removed %#v", got)
	}
	releases, err := st.ListReleases(context.Background(), "demo")
	if err != nil || len(releases) != 1 || releases[0].DeploymentID != second.ID {
		t.Fatalf("releases %#v %v", releases, err)
	}
}

type gateReleaser struct {
	st    store.Store
	calls int
}

func (g *gateReleaser) Release(_ context.Context, appName, id, _ string, logs io.Writer) error {
	g.calls++
	if g.calls > 1 {
		return runtime.ErrHealthTimeout
	}
	_, _ = io.WriteString(logs, "released "+appName+"\n")
	status := store.DeploymentStatusRunning
	appURL := "http://" + appName + ".localhost:8081/"
	now := time.Now().UTC()
	if _, err := g.st.UpdateDeployment(context.Background(), id, store.DeploymentUpdate{
		Status:     &status,
		URL:        &appURL,
		FinishedAt: &now,
	}); err != nil {
		return err
	}
	_, err := g.st.SetAppStatus(context.Background(), appName, store.AppStatusRunning, appURL)
	return err
}

type imageBin struct {
	mu      sync.Mutex
	removed []string
}

func (b *imageBin) RemoveImage(_ context.Context, tag string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.removed = append(b.removed, tag)
	return nil
}

func (b *imageBin) tags() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, len(b.removed))
	copy(out, b.removed)
	return out
}

type seqCloner struct {
	shas []string
}

func (s *seqCloner) Clone(_ context.Context, _, _, dest string, _ io.Writer) (string, error) {
	if len(s.shas) == 0 {
		return "", errors.New("no sha")
	}
	sha := s.shas[0]
	s.shas = s.shas[1:]
	if err := os.WriteFile(filepath.Join(dest, "Dockerfile"), []byte("FROM scratch\n"), 0o600); err != nil {
		return "", err
	}
	return sha, nil
}

func makeRunning(t *testing.T, st store.Store, sha, tag string) store.Deployment {
	t.Helper()
	ctx := context.Background()
	dep, err := st.CreateBuild(ctx, "demo", "main")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateDeployment(ctx, dep.ID, store.DeploymentUpdate{CommitSHA: &sha, ImageTag: &tag}); err != nil {
		t.Fatal(err)
	}
	building := store.DeploymentStatusBuilding
	now := time.Now().UTC()
	if _, err := st.UpdateDeployment(ctx, dep.ID, store.DeploymentUpdate{Status: &building, StartedAt: &now}); err != nil {
		t.Fatal(err)
	}
	starting := store.DeploymentStatusStarting
	if _, err := st.UpdateDeployment(ctx, dep.ID, store.DeploymentUpdate{Status: &starting}); err != nil {
		t.Fatal(err)
	}
	running := store.DeploymentStatusRunning
	appURL := "http://demo.localhost:8081/"
	if _, err := st.UpdateDeployment(ctx, dep.ID, store.DeploymentUpdate{Status: &running, URL: &appURL, FinishedAt: &now}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetAppStatus(ctx, "demo", store.AppStatusRunning, appURL); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RecordRelease(ctx, dep.ID, 5); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetDeployment(ctx, dep.ID)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func waitClone(t *testing.T, cloner *source.Fake, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		calls, _, _ := cloner.Snapshot()
		if calls >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("clone calls stayed below %d", want)
}
