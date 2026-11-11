package runtime

import (
	"context"
	"time"

	"github.com/gabbygtobar-coder/shipyard/internal/store"
)

const msgInterrupted = "interrupted by restart"

// ReconcileLocalRuntime marks deployments that were still in progress or
// receiving traffic as stopped or failed. shipyardd calls it on startup
// because containers from the previous process are not reattached. A runtime
// that keeps tasks running, such as ECS, uses ReconcileInterruptedBuilds.
func ReconcileLocalRuntime(ctx context.Context, st store.Store) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	apps, err := st.ListApps(ctx)
	if err != nil {
		return err
	}
	failed := store.DeploymentStatusFailed
	stopped := store.DeploymentStatusStopped
	msg := msgInterrupted
	now := time.Now().UTC()
	for _, app := range apps {
		deps, err := st.ListDeployments(ctx, app.Name)
		if err != nil {
			return err
		}
		for _, dep := range deps {
			switch dep.Status {
			case store.DeploymentStatusRunning:
				if _, err := st.UpdateDeployment(ctx, dep.ID, store.DeploymentUpdate{Status: &stopped}); err != nil {
					return err
				}
			case store.DeploymentStatusQueued, store.DeploymentStatusBuilding, store.DeploymentStatusStarting:
				if _, err := st.UpdateDeployment(ctx, dep.ID, store.DeploymentUpdate{
					Status:     &failed,
					Error:      &msg,
					FinishedAt: &now,
				}); err != nil {
					return err
				}
			}
		}
		if app.Status == store.AppStatusRunning {
			if _, err := st.SetAppStatus(ctx, app.Name, store.AppStatusStopped, ""); err != nil {
				return err
			}
		}
	}
	return nil
}

// ReconcileInterruptedBuilds marks queued, building, and starting deployments
// failed. Running deployments stay running, because an ECS task is still up
// after shipyardd restarts.
func ReconcileInterruptedBuilds(ctx context.Context, st store.Store) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	apps, err := st.ListApps(ctx)
	if err != nil {
		return err
	}
	failed := store.DeploymentStatusFailed
	msg := msgInterrupted
	now := time.Now().UTC()
	for _, app := range apps {
		deps, err := st.ListDeployments(ctx, app.Name)
		if err != nil {
			return err
		}
		for _, dep := range deps {
			switch dep.Status {
			case store.DeploymentStatusQueued, store.DeploymentStatusBuilding, store.DeploymentStatusStarting:
				if _, err := st.UpdateDeployment(ctx, dep.ID, store.DeploymentUpdate{
					Status:     &failed,
					Error:      &msg,
					FinishedAt: &now,
				}); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
