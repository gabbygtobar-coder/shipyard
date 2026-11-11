package deploy

import (
	"errors"
	"time"

	"github.com/gabbygtobar-coder/shipyard/internal/store"
)

var (
	// ErrNoRelease is returned when an app has no retained image to redeploy.
	ErrNoRelease = errors.New("no release to roll back to")
	// ErrAlreadyCurrent is returned when the requested deployment is already receiving traffic.
	ErrAlreadyCurrent = errors.New("deployment is already receiving traffic")
	// ErrReleaseNotFound is returned when the requested deployment is not a retained release.
	ErrReleaseNotFound = errors.New("release not found")
)

// SelectRelease chooses the image to redeploy.
// releases is newest first. An empty requestedID selects the newest release
// that is not currentID, or the newest release when currentID is empty.
func SelectRelease(releases []store.Release, currentID, requestedID string) (store.Release, error) {
	if len(releases) == 0 {
		return store.Release{}, ErrNoRelease
	}
	if requestedID != "" {
		for _, rel := range releases {
			if rel.DeploymentID != requestedID {
				continue
			}
			if currentID != "" && requestedID == currentID {
				return store.Release{}, ErrAlreadyCurrent
			}
			return rel, nil
		}
		return store.Release{}, ErrReleaseNotFound
	}
	if currentID == "" {
		return releases[0], nil
	}
	for _, rel := range releases {
		if rel.DeploymentID != currentID {
			return rel, nil
		}
	}
	return store.Release{}, ErrNoRelease
}

func currentRunning(deps []store.Deployment) string {
	var current string
	var created time.Time
	for _, dep := range deps {
		if dep.Status != store.DeploymentStatusRunning {
			continue
		}
		if current == "" || dep.CreatedAt.After(created) {
			current = dep.ID
			created = dep.CreatedAt
		}
	}
	return current
}
