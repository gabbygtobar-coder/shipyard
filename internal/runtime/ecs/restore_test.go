package ecs

import (
	"context"
	"testing"

	"github.com/gabbygtobar-coder/shipyard/internal/route"
	"github.com/gabbygtobar-coder/shipyard/internal/store"
)

func TestRestoreRoutesKeepsRunningApps(t *testing.T) {
	t.Parallel()
	st := store.NewMemory()
	ctx := context.Background()
	if _, err := st.CreateApp(ctx, "demo", "https://github.com/acme/demo"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateApp(ctx, "idle", "https://github.com/acme/idle"); err != nil {
		t.Fatal(err)
	}
	running, err := st.CreateDeployment(ctx, "demo", store.DeploymentStatusRunning)
	if err != nil {
		t.Fatal(err)
	}
	const public = "http://shipyard-alb.example.com/apps/demo/"
	if _, err := st.SetAppStatus(ctx, "demo", store.AppStatusRunning, public); err != nil {
		t.Fatal(err)
	}
	routes := route.NewTable()
	if err := RestoreRoutes(ctx, st, routes); err != nil {
		t.Fatal(err)
	}
	got, ok := routes.Lookup("demo")
	if !ok || got.Endpoint != public || got.InstanceID != "ecs/demo/active" || got.DeploymentID != running.ID {
		t.Fatalf("route %+v ok %v", got, ok)
	}
	if _, ok := routes.Lookup("idle"); ok {
		t.Fatal("idle app was restored")
	}
}
