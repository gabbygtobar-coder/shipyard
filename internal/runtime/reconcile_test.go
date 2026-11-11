package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/gabbygtobar-coder/shipyard/internal/store"
)

func TestReconcileLocalRuntime(t *testing.T) {
	t.Parallel()
	st := store.NewMemory()
	ctx := context.Background()
	if _, err := st.CreateApp(ctx, "demo", "https://github.com/acme/demo"); err != nil {
		t.Fatal(err)
	}
	running, err := st.CreateDeployment(ctx, "demo", store.DeploymentStatusRunning)
	if err != nil {
		t.Fatal(err)
	}
	queued, err := st.CreateBuild(ctx, "demo", "main")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetAppStatus(ctx, "demo", store.AppStatusRunning, "http://demo.localhost:8081/"); err != nil {
		t.Fatal(err)
	}
	if err := ReconcileLocalRuntime(ctx, st); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetDeployment(ctx, running.ID)
	if err != nil || got.Status != store.DeploymentStatusStopped {
		t.Fatalf("running %#v %v", got, err)
	}
	got, err = st.GetDeployment(ctx, queued.ID)
	if err != nil || got.Status != store.DeploymentStatusFailed || got.Error != "interrupted by restart" || got.FinishedAt == nil {
		t.Fatalf("queued %#v %v", got, err)
	}
	app, err := st.GetApp(ctx, "demo")
	if err != nil || app.Status != store.AppStatusStopped || app.URL != "" {
		t.Fatalf("app %#v %v", app, err)
	}
	if time.Since(*got.FinishedAt) > time.Minute {
		t.Fatalf("finished %s", got.FinishedAt)
	}
}
