package ecs

import (
	"context"
	"errors"
	"time"

	"github.com/gabbygtobar-coder/shipyard/internal/route"
	"github.com/gabbygtobar-coder/shipyard/internal/store"
)

// RestoreRoutes puts running apps back into the route table after a restart.
// ECS tasks keep running when shipyardd exits, so Stop still has an instance
// id to scale the service to zero. The id uses the active sentinel because
// the task definition revision is not stored.
func RestoreRoutes(ctx context.Context, st store.Store, routes *route.Table) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if st == nil || routes == nil {
		return errors.New("store and routes are required")
	}
	apps, err := st.ListApps(ctx)
	if err != nil {
		return err
	}
	for _, app := range apps {
		if app.Status != store.AppStatusRunning || app.URL == "" {
			continue
		}
		deps, err := st.ListDeployments(ctx, app.Name)
		if err != nil {
			return err
		}
		routes.Swap(app.Name, route.Target{
			Endpoint:     app.URL,
			InstanceID:   activeInstanceID(app.Name),
			DeploymentID: newestRunning(deps),
		})
	}
	return nil
}

func newestRunning(deps []store.Deployment) string {
	var (
		id      string
		created time.Time
	)
	for _, dep := range deps {
		if dep.Status != store.DeploymentStatusRunning {
			continue
		}
		if id == "" || dep.CreatedAt.After(created) {
			id = dep.ID
			created = dep.CreatedAt
		}
	}
	return id
}
