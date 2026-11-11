package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestSQLitePersistsAcrossReopen(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 11, 4, 5, 6, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "shipyard.db")
	ctx := context.Background()

	st, err := OpenWithClock(path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	app, err := st.CreateApp(ctx, " demo ", " https://github.com/acme/demo ")
	if err != nil {
		t.Fatal(err)
	}
	if app.Branch != DefaultBranch || app.AutoDeploy || app.Status != AppStatusIdle {
		t.Fatalf("created %#v", app)
	}
	secret := "super-secret-value"
	app, err = st.ConfigureApp(ctx, "demo", AppSettings{Branch: "release", AutoDeploy: true, WebhookSecret: secret})
	if err != nil {
		t.Fatal(err)
	}
	if app.Branch != "release" || !app.AutoDeploy || app.WebhookSecret != secret {
		t.Fatalf("configured %#v", app)
	}

	dep, err := st.CreateBuild(ctx, "demo", "release")
	if err != nil {
		t.Fatal(err)
	}
	sha := "0123456789abcdef0123456789abcdef01234567"
	tag := "shipyard/demo:0123456789ab"
	if _, err := st.UpdateDeployment(ctx, dep.ID, DeploymentUpdate{CommitSHA: &sha, ImageTag: &tag}); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendDeploymentLog(ctx, dep.ID, []byte("building\n")); err != nil {
		t.Fatal(err)
	}
	building := DeploymentStatusBuilding
	started := now
	if _, err := st.UpdateDeployment(ctx, dep.ID, DeploymentUpdate{Status: &building, StartedAt: &started}); err != nil {
		t.Fatal(err)
	}
	succeeded := DeploymentStatusSucceeded
	finished := now.Add(time.Second)
	if _, err := st.UpdateDeployment(ctx, dep.ID, DeploymentUpdate{Status: &succeeded, FinishedAt: &finished}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RecordRelease(ctx, dep.ID, 5); err != nil {
		t.Fatal(err)
	}
	claimed, err := st.ClaimDelivery(ctx, "delivery-1")
	if err != nil || !claimed {
		t.Fatalf("claim %v %v", claimed, err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	again, err := OpenWithClock(path, func() time.Time { return now.Add(time.Minute) })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = again.Close() })

	got, err := again.GetApp(ctx, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if got.RepoURL != "https://github.com/acme/demo" || got.Branch != "release" || !got.AutoDeploy || got.WebhookSecret != secret {
		t.Fatalf("reopened app %#v", got)
	}
	if !got.CreatedAt.Equal(now) {
		t.Fatalf("created_at %s", got.CreatedAt)
	}
	deps, err := again.ListDeployments(ctx, "demo")
	if err != nil || len(deps) != 1 {
		t.Fatalf("deployments %#v %v", deps, err)
	}
	if deps[0].Status != DeploymentStatusSucceeded || deps[0].ImageTag != tag || deps[0].CommitSHA != sha {
		t.Fatalf("deployment %#v", deps[0])
	}
	if deps[0].StartedAt == nil || deps[0].FinishedAt == nil || !deps[0].FinishedAt.Equal(finished) {
		t.Fatalf("timestamps %#v", deps[0])
	}
	log, err := again.ReadDeploymentLog(ctx, dep.ID, 0)
	if err != nil || string(log.Data) != "building\n" || !log.Terminal {
		t.Fatalf("log %#v %v", log, err)
	}
	releases, err := again.ListReleases(ctx, "demo")
	if err != nil || len(releases) != 1 || releases[0].DeploymentID != dep.ID || releases[0].ImageTag != tag {
		t.Fatalf("releases %#v %v", releases, err)
	}
	second, err := again.ClaimDelivery(ctx, "delivery-1")
	if err != nil || second {
		t.Fatalf("second claim %v %v", second, err)
	}
	apps, err := again.ListAppsByRepo(ctx, "https://github.com/acme/demo.git")
	if err != nil || len(apps) != 1 || apps[0].Name != "demo" {
		t.Fatalf("by repo %#v %v", apps, err)
	}

	var version int
	if err := again.db.QueryRow(`SELECT COUNT(1) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 2 {
		t.Fatalf("migrations %d", version)
	}
}

func TestSQLiteReleaseRetention(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "nested", "shipyard.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	testReleaseRetention(t, st)
}

func TestMemoryReleaseRetention(t *testing.T) {
	t.Parallel()
	testReleaseRetention(t, NewMemory())
}

func testReleaseRetention(t *testing.T, st Store) {
	t.Helper()
	ctx := context.Background()
	if _, err := st.CreateApp(ctx, "demo", "https://github.com/acme/demo"); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for i, sha := range []string{
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"cccccccccccccccccccccccccccccccccccccccc",
	} {
		dep := succeedDeployment(t, st, "main", sha, "shipyard/demo:"+sha[:12])
		ids = append(ids, dep.ID)
		dropped, err := st.RecordRelease(ctx, dep.ID, 2)
		if err != nil {
			t.Fatal(err)
		}
		if i < 2 && len(dropped) != 0 {
			t.Fatalf("release %d dropped %#v", i, dropped)
		}
		if i == 2 {
			if len(dropped) != 1 || dropped[0].DeploymentID != ids[0] {
				t.Fatalf("dropped %#v", dropped)
			}
		}
	}
	again, err := st.RecordRelease(ctx, ids[2], 2)
	if err != nil || len(again) != 0 {
		t.Fatalf("repeat %#v %v", again, err)
	}
	list, err := st.ListReleases(ctx, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].DeploymentID != ids[2] || list[1].DeploymentID != ids[1] {
		t.Fatalf("releases %#v", list)
	}
	rollback, err := st.CreateRollback(ctx, "demo", list[1])
	if err != nil {
		t.Fatal(err)
	}
	if rollback.RollbackOf != ids[1] || rollback.ImageTag != list[1].ImageTag || rollback.Status != DeploymentStatusQueued {
		t.Fatalf("rollback %#v", rollback)
	}
	if _, err := st.RecordRelease(ctx, rollback.ID, 2); !errors.Is(err, ErrInvalid) {
		t.Fatalf("queued release error %v", err)
	}
}

func succeedDeployment(t *testing.T, st Store, ref, sha, tag string) Deployment {
	t.Helper()
	ctx := context.Background()
	dep, err := st.CreateBuild(ctx, "demo", ref)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateDeployment(ctx, dep.ID, DeploymentUpdate{CommitSHA: &sha, ImageTag: &tag}); err != nil {
		t.Fatal(err)
	}
	building := DeploymentStatusBuilding
	now := time.Now().UTC()
	if _, err := st.UpdateDeployment(ctx, dep.ID, DeploymentUpdate{Status: &building, StartedAt: &now}); err != nil {
		t.Fatal(err)
	}
	succeeded := DeploymentStatusSucceeded
	if _, err := st.UpdateDeployment(ctx, dep.ID, DeploymentUpdate{Status: &succeeded, FinishedAt: &now}); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetDeployment(ctx, dep.ID)
	if err != nil {
		t.Fatal(err)
	}
	return got
}
