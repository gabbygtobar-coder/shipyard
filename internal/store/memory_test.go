package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMemoryCreateGetList(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 11, 3, 4, 5, 0, time.FixedZone("EST", -5*3600))
	st := NewMemoryWithClock(func() time.Time { return now })
	ctx := context.Background()

	apps, err := st.ListApps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if apps == nil || len(apps) != 0 {
		t.Fatalf("ListApps() = %#v, want empty slice", apps)
	}

	first, err := st.CreateApp(ctx, "  demo  ", "  https://github.com/acme/demo  ")
	if err != nil {
		t.Fatal(err)
	}
	second, err := st.CreateApp(ctx, "api", "https://github.com/acme/api")
	if err != nil {
		t.Fatal(err)
	}

	wantCreated := now.UTC()
	if first.Name != "demo" || first.RepoURL != "https://github.com/acme/demo" || !first.CreatedAt.Equal(wantCreated) {
		t.Fatalf("first = %#v, want trimmed fields at %s", first, wantCreated)
	}
	if second.Name != "api" {
		t.Fatalf("second name = %q", second.Name)
	}

	got, err := st.GetApp(ctx, " demo ")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "demo" || got.RepoURL != first.RepoURL || !got.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("GetApp() = %#v, want %#v", got, first)
	}

	first.Name = "mutated"
	got, err = st.GetApp(ctx, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "demo" {
		t.Fatalf("stored name changed to %q", got.Name)
	}

	apps, err = st.ListApps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) != 2 || apps[0].Name != "demo" || apps[1].Name != "api" {
		t.Fatalf("ListApps() = %#v, want demo then api", apps)
	}
}

func TestMemoryCreateConflictsAndInvalid(t *testing.T) {
	t.Parallel()
	st := NewMemory()
	ctx := context.Background()

	if _, err := st.CreateApp(ctx, "demo", "https://github.com/acme/demo"); err != nil {
		t.Fatal(err)
	}
	_, err := st.CreateApp(ctx, "demo", "https://github.com/acme/other")
	if !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("duplicate error = %v, want ErrAlreadyExists", err)
	}

	cases := []struct {
		name    string
		repoURL string
	}{
		{name: "", repoURL: "https://github.com/acme/demo"},
		{name: "   ", repoURL: "https://github.com/acme/demo"},
		{name: "demo", repoURL: ""},
		{name: "demo", repoURL: "   "},
	}
	for _, tc := range cases {
		_, err := st.CreateApp(ctx, tc.name, tc.repoURL)
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("CreateApp(%q, %q) error = %v, want ErrInvalid", tc.name, tc.repoURL, err)
		}
	}

	_, err = st.GetApp(ctx, "missing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing error = %v, want ErrNotFound", err)
	}
	_, err = st.GetApp(ctx, "  ")
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("blank get error = %v, want ErrInvalid", err)
	}
}

func TestMemoryDeployments(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	st := NewMemoryWithClock(func() time.Time { return now })
	ctx := context.Background()

	if _, err := st.CreateApp(ctx, "alpha", "https://github.com/acme/alpha"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateApp(ctx, "beta", "https://github.com/acme/beta"); err != nil {
		t.Fatal(err)
	}

	if _, err := st.CreateDeployment(ctx, "missing", DeploymentStatusPending); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing app error = %v", err)
	}

	first, err := st.CreateDeployment(ctx, " alpha ", " "+DeploymentStatusPending+" ")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != "dep_1" || first.AppName != "alpha" || first.Status != DeploymentStatusPending || !first.CreatedAt.Equal(now) {
		t.Fatalf("first deployment = %#v", first)
	}
	second, err := st.CreateDeployment(ctx, "beta", "rolled_back")
	if err != nil {
		t.Fatal(err)
	}
	third, err := st.CreateDeployment(ctx, "alpha", "failed")
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != "dep_2" || third.ID != "dep_3" {
		t.Fatalf("ids = %s %s, want dep_2 dep_3", second.ID, third.ID)
	}

	got, err := st.GetDeployment(ctx, " dep_2 ")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "dep_2" || got.AppName != "beta" {
		t.Fatalf("GetDeployment() = %#v", got)
	}
	got.Status = "mutated"
	got, err = st.GetDeployment(ctx, "dep_2")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "rolled_back" {
		t.Fatalf("status changed to %q", got.Status)
	}

	alpha, err := st.ListDeployments(ctx, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if len(alpha) != 2 || alpha[0].ID != "dep_1" || alpha[1].ID != "dep_3" {
		t.Fatalf("alpha deployments = %#v", alpha)
	}
	beta, err := st.ListDeployments(ctx, "beta")
	if err != nil {
		t.Fatal(err)
	}
	if len(beta) != 1 || beta[0].ID != "dep_2" {
		t.Fatalf("beta deployments = %#v", beta)
	}

	if _, err := st.CreateApp(ctx, "gamma", "https://github.com/acme/gamma"); err != nil {
		t.Fatal(err)
	}
	gamma, err := st.ListDeployments(ctx, "gamma")
	if err != nil {
		t.Fatal(err)
	}
	if gamma == nil || len(gamma) != 0 {
		t.Fatalf("gamma deployments = %#v, want empty slice", gamma)
	}
}

func TestMemoryDeploymentValidation(t *testing.T) {
	t.Parallel()
	st := NewMemory()
	ctx := context.Background()
	if _, err := st.CreateApp(ctx, "demo", "https://github.com/acme/demo"); err != nil {
		t.Fatal(err)
	}

	valid := []string{"pending", "a", "rolled_back", "a-b", "a1", strings.Repeat("a", 32)}
	for _, status := range valid {
		if _, err := st.CreateDeployment(ctx, "demo", status); err != nil {
			t.Fatalf("status %q: %v", status, err)
		}
	}

	invalid := []string{"", "   ", "Pending", "has space", "nope!", strings.Repeat("a", 33)}
	for _, status := range invalid {
		_, err := st.CreateDeployment(ctx, "demo", status)
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("status %q error = %v, want ErrInvalid", status, err)
		}
	}

	if _, err := st.CreateDeployment(ctx, " ", "pending"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("blank app error = %v", err)
	}
	if _, err := st.GetDeployment(ctx, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("blank id error = %v", err)
	}
	if _, err := st.GetDeployment(ctx, "dep_missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing id error = %v", err)
	}
	if _, err := st.ListDeployments(ctx, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("blank list error = %v", err)
	}
	if _, err := st.ListDeployments(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing list error = %v", err)
	}

	fresh := NewMemory()
	if _, err := fresh.CreateApp(ctx, "demo", "https://github.com/acme/demo"); err != nil {
		t.Fatal(err)
	}
	dep, err := fresh.CreateDeployment(ctx, "demo", "pending")
	if err != nil {
		t.Fatal(err)
	}
	if dep.ID != "dep_1" {
		t.Fatalf("id = %s, want dep_1", dep.ID)
	}
}

func TestMemoryRejectedDeploymentDoesNotConsumeID(t *testing.T) {
	t.Parallel()
	st := NewMemory()
	ctx := context.Background()
	if _, err := st.CreateApp(ctx, "demo", "https://github.com/acme/demo"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateDeployment(ctx, "missing", "pending"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := st.CreateDeployment(ctx, "demo", "NOPE"); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	dep, err := st.CreateDeployment(ctx, "demo", "pending")
	if err != nil {
		t.Fatal(err)
	}
	if dep.ID != "dep_1" {
		t.Fatalf("id = %s, want dep_1", dep.ID)
	}
}

func TestMemoryContextCanceled(t *testing.T) {
	t.Parallel()
	st := NewMemory()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := st.CreateApp(ctx, "demo", "https://github.com/acme/demo"); !errors.Is(err, context.Canceled) {
		t.Fatalf("CreateApp error = %v", err)
	}
	if _, err := st.GetApp(ctx, "demo"); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetApp error = %v", err)
	}
	if _, err := st.ListApps(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("ListApps error = %v", err)
	}
	if _, err := st.CreateDeployment(ctx, "demo", "pending"); !errors.Is(err, context.Canceled) {
		t.Fatalf("CreateDeployment error = %v", err)
	}
	if _, err := st.GetDeployment(ctx, "dep_1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetDeployment error = %v", err)
	}
	if _, err := st.ListDeployments(ctx, "demo"); !errors.Is(err, context.Canceled) {
		t.Fatalf("ListDeployments error = %v", err)
	}
	if _, err := st.CreateBuild(ctx, "demo", "main"); !errors.Is(err, context.Canceled) {
		t.Fatalf("CreateBuild error = %v", err)
	}
	if _, err := st.UpdateDeployment(ctx, "dep_1", DeploymentUpdate{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("UpdateDeployment error = %v", err)
	}
	if err := st.AppendDeploymentLog(ctx, "dep_1", []byte("x")); !errors.Is(err, context.Canceled) {
		t.Fatalf("AppendDeploymentLog error = %v", err)
	}
	if _, err := st.ReadDeploymentLog(ctx, "dep_1", 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("ReadDeploymentLog error = %v", err)
	}
	if err := st.WaitDeploymentLog(ctx, "dep_1", 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("WaitDeploymentLog error = %v", err)
	}
}

func TestMemoryNilClock(t *testing.T) {
	t.Parallel()
	st := NewMemoryWithClock(nil)
	before := time.Now().Add(-time.Second)
	app, err := st.CreateApp(context.Background(), "demo", "https://github.com/acme/demo")
	if err != nil {
		t.Fatal(err)
	}
	if app.CreatedAt.Before(before) || app.CreatedAt.Location() != time.UTC {
		t.Fatalf("CreatedAt = %s", app.CreatedAt)
	}
}

func TestMemoryBuildLifecycle(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 11, 3, 4, 5, 0, time.UTC)
	st := NewMemoryWithClock(func() time.Time { return now })
	ctx := context.Background()
	if _, err := st.CreateApp(ctx, "demo", "https://github.com/acme/demo"); err != nil {
		t.Fatal(err)
	}
	dep, err := st.CreateBuild(ctx, " demo ", " feature/one ")
	if err != nil {
		t.Fatal(err)
	}
	if dep.ID != "dep_1" || dep.Status != DeploymentStatusQueued || dep.Ref != "feature/one" || dep.AppName != "demo" {
		t.Fatalf("build = %#v", dep)
	}
	if !dep.CreatedAt.Equal(now) || dep.StartedAt != nil || dep.FinishedAt != nil {
		t.Fatalf("timestamps = %#v", dep)
	}

	if _, err := st.CreateBuild(ctx, "demo", "--evil"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad ref error = %v", err)
	}
	if _, err := st.CreateBuild(ctx, "missing", "main"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing app error = %v", err)
	}
	next, err := st.CreateBuild(ctx, "demo", "")
	if err != nil {
		t.Fatal(err)
	}
	if next.ID != "dep_2" || next.Ref != "" {
		t.Fatalf("second = %#v", next)
	}

	building := DeploymentStatusBuilding
	started := now.Add(time.Minute)
	updated, err := st.UpdateDeployment(ctx, "dep_1", DeploymentUpdate{Status: &building, StartedAt: &started})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != DeploymentStatusBuilding || updated.StartedAt == nil || !updated.StartedAt.Equal(started) {
		t.Fatalf("building = %#v", updated)
	}
	started = started.Add(time.Hour)
	got, err := st.GetDeployment(ctx, "dep_1")
	if err != nil {
		t.Fatal(err)
	}
	if got.StartedAt == nil || !got.StartedAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("stored start changed to %v", got.StartedAt)
	}

	if err := st.AppendDeploymentLog(ctx, "dep_1", []byte("alpha")); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendDeploymentLog(ctx, "dep_1", []byte("beta")); err != nil {
		t.Fatal(err)
	}
	snap, err := st.ReadDeploymentLog(ctx, "dep_1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if string(snap.Data) != "alphabeta" || snap.Terminal || snap.Next != len("alphabeta") {
		t.Fatalf("log = %#v", snap)
	}
	part, err := st.ReadDeploymentLog(ctx, "dep_1", 5)
	if err != nil {
		t.Fatal(err)
	}
	if string(part.Data) != "beta" {
		t.Fatalf("partial = %q", part.Data)
	}

	sha := "0123456789abcdef0123456789abcdef01234567"
	tag := "shipyard/demo:0123456789ab"
	okStatus := DeploymentStatusSucceeded
	finished := now.Add(2 * time.Minute)
	done, err := st.UpdateDeployment(ctx, "dep_1", DeploymentUpdate{
		Status:     &okStatus,
		CommitSHA:  &sha,
		ImageTag:   &tag,
		FinishedAt: &finished,
	})
	if err != nil {
		t.Fatal(err)
	}
	if done.CommitSHA != sha || done.ImageTag != tag || done.FinishedAt == nil || !done.FinishedAt.Equal(finished) {
		t.Fatalf("done = %#v", done)
	}
	if _, err := st.UpdateDeployment(ctx, "dep_1", DeploymentUpdate{Error: strPtr("later")}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("terminal update error = %v", err)
	}
	if err := st.AppendDeploymentLog(ctx, "dep_1", []byte("!")); err != nil {
		t.Fatal(err)
	}
	snap, err = st.ReadDeploymentLog(ctx, "dep_1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if string(snap.Data) != "alphabeta!" || !snap.Terminal {
		t.Fatalf("final log = %#v", snap)
	}
}

func TestMemoryDeploymentTransitions(t *testing.T) {
	t.Parallel()
	st := NewMemory()
	ctx := context.Background()
	if _, err := st.CreateApp(ctx, "demo", "https://github.com/acme/demo"); err != nil {
		t.Fatal(err)
	}
	dep, err := st.CreateBuild(ctx, "demo", "main")
	if err != nil {
		t.Fatal(err)
	}

	illegal := []string{DeploymentStatusSucceeded, DeploymentStatusPending, "nope"}
	for _, status := range illegal {
		status := status
		_, err := st.UpdateDeployment(ctx, dep.ID, DeploymentUpdate{Status: &status})
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("queued -> %s error = %v", status, err)
		}
	}
	building := DeploymentStatusBuilding
	if _, err := st.UpdateDeployment(ctx, dep.ID, DeploymentUpdate{Status: &building}); err != nil {
		t.Fatal(err)
	}
	back := DeploymentStatusQueued
	if _, err := st.UpdateDeployment(ctx, dep.ID, DeploymentUpdate{Status: &back}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("building -> queued error = %v", err)
	}
	failed := DeploymentStatusFailed
	if _, err := st.UpdateDeployment(ctx, dep.ID, DeploymentUpdate{Status: &failed, Error: strPtr("boom")}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateDeployment(ctx, dep.ID, DeploymentUpdate{Status: &building}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("failed -> building error = %v", err)
	}
	got, err := st.GetDeployment(ctx, dep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != DeploymentStatusFailed || got.Error != "boom" {
		t.Fatalf("got = %#v", got)
	}
}

func TestMemoryLogTruncationAndWait(t *testing.T) {
	t.Parallel()
	st := NewMemory()
	ctx := context.Background()
	if _, err := st.CreateApp(ctx, "demo", "https://github.com/acme/demo"); err != nil {
		t.Fatal(err)
	}
	dep, err := st.CreateBuild(ctx, "demo", "main")
	if err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("a"), MaxDeploymentLogBytes+32)
	if err := st.AppendDeploymentLog(ctx, dep.ID, payload); err != nil {
		t.Fatal(err)
	}
	snap, err := st.ReadDeploymentLog(ctx, dep.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(snap.Data, []byte("[log truncated]")) {
		t.Fatal("expected truncation notice")
	}
	if err := st.AppendDeploymentLog(ctx, dep.ID, []byte("more")); err != nil {
		t.Fatal(err)
	}

	waitCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- st.WaitDeploymentLog(waitCtx, dep.ID, len(snap.Data))
	}()
	time.Sleep(40 * time.Millisecond)
	failed := DeploymentStatusFailed
	if _, err := st.UpdateDeployment(ctx, dep.ID, DeploymentUpdate{Status: &failed}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("wait did not notice the terminal status")
	}

	if _, err := st.ReadDeploymentLog(ctx, dep.ID, -1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("negative offset error = %v", err)
	}
	if _, err := st.ReadDeploymentLog(ctx, "missing", 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing log error = %v", err)
	}
}

func strPtr(s string) *string { return &s }

func TestMemoryRunStatusAndAppURL(t *testing.T) {
	t.Parallel()
	st := NewMemory()
	ctx := context.Background()
	app, err := st.CreateApp(ctx, "demo", "https://github.com/acme/demo")
	if err != nil {
		t.Fatal(err)
	}
	if app.Status != AppStatusIdle || app.URL != "" {
		t.Fatalf("new app = %#v", app)
	}
	dep, err := st.CreateDeployment(ctx, "demo", DeploymentStatusStarting)
	if err != nil {
		t.Fatal(err)
	}
	running := DeploymentStatusRunning
	url := "http://demo.localhost:8081/"
	now := time.Now().UTC()
	got, err := st.UpdateDeployment(ctx, dep.ID, DeploymentUpdate{
		Status:     &running,
		URL:        &url,
		FinishedAt: &now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != DeploymentStatusRunning || got.URL != url || got.FinishedAt == nil {
		t.Fatalf("running = %#v", got)
	}
	if _, err := st.UpdateDeployment(ctx, dep.ID, DeploymentUpdate{URL: strPtr("http://other")}); !errors.Is(err, ErrInvalid) {
		t.Fatal("running deployment accepted a field change")
	}
	updated, err := st.SetAppStatus(ctx, "demo", AppStatusRunning, url)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != AppStatusRunning || updated.URL != url {
		t.Fatalf("app = %#v", updated)
	}
	if _, err := st.SetAppStatus(ctx, "demo", AppStatusRunning, ""); !errors.Is(err, ErrInvalid) {
		t.Fatal("running app without a url was accepted")
	}
	if _, err := st.SetAppStatus(ctx, "demo", AppStatusIdle, url); !errors.Is(err, ErrInvalid) {
		t.Fatal("idle app with a url was accepted")
	}
	stopped := DeploymentStatusStopped
	if _, err := st.UpdateDeployment(ctx, dep.ID, DeploymentUpdate{Status: &stopped}); err != nil {
		t.Fatal(err)
	}
	final, err := st.GetDeployment(ctx, dep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Status != DeploymentStatusStopped || final.URL != url {
		t.Fatalf("stopped = %#v", final)
	}
	if _, err := st.UpdateDeployment(ctx, dep.ID, DeploymentUpdate{Status: &running}); !errors.Is(err, ErrInvalid) {
		t.Fatal("stopped deployment changed status")
	}
	snap, err := st.ReadDeploymentLog(ctx, dep.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !snap.Terminal {
		t.Fatal("stopped deployment is not terminal for logs")
	}
	if _, err := st.SetAppStatus(ctx, "missing", AppStatusStopped, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing app error = %v", err)
	}
}

func TestMemoryConcurrentCreates(t *testing.T) {
	t.Parallel()
	st := NewMemory()
	const n = 32
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := fmt.Sprintf("app-%d", i)
			_, err := st.CreateApp(context.Background(), name, "https://github.com/acme/repo")
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	apps, err := st.ListApps(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) != n {
		t.Fatalf("len(apps) = %d, want %d", len(apps), n)
	}
}

func TestMemoryDuplicateCreateRace(t *testing.T) {
	t.Parallel()
	st := NewMemory()
	var wg sync.WaitGroup
	errs := make([]error, 8)
	wg.Add(len(errs))
	for i := range errs {
		go func(i int) {
			defer wg.Done()
			_, errs[i] = st.CreateApp(context.Background(), "demo", "https://github.com/acme/demo")
		}(i)
	}
	wg.Wait()

	successes, conflicts := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrAlreadyExists):
			conflicts++
		default:
			t.Fatalf("unexpected error %v", err)
		}
	}
	if successes != 1 || conflicts != len(errs)-1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
}
