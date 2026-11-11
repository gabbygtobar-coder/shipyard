// Package store defines the application and deployment persistence interface.
//
// M0 ships an in-memory implementation. M1 records image builds in the same
// store: status, git ref, commit, image tag, timestamps, error text, and the
// captured build log.
package store

import (
	"context"
	"errors"
	"time"
)

var (
	// ErrNotFound is returned when an app or deployment does not exist.
	ErrNotFound = errors.New("not found")
	// ErrAlreadyExists is returned when an app name is already registered.
	ErrAlreadyExists = errors.New("already exists")
	// ErrInvalid is returned when a value fails a store invariant.
	ErrInvalid = errors.New("invalid")
)

const (
	// DeploymentStatusPending is the M0 placeholder status.
	// The build pipeline does not use it.
	DeploymentStatusPending = "pending"
	// DeploymentStatusQueued means a build is waiting for a worker.
	DeploymentStatusQueued = "queued"
	// DeploymentStatusBuilding means a worker is cloning and building.
	DeploymentStatusBuilding = "building"
	// DeploymentStatusSucceeded means the image was built.
	DeploymentStatusSucceeded = "succeeded"
	// DeploymentStatusFailed means the build stopped with an error.
	DeploymentStatusFailed = "failed"

	// MaxDeploymentLogBytes is the captured build log kept per deployment.
	MaxDeploymentLogBytes = 1 << 20
)

// App is a registered source repository.
type App struct {
	Name      string    `json:"name"`
	RepoURL   string    `json:"repo_url"`
	CreatedAt time.Time `json:"created_at"`
}

// Deployment is a stored deploy record for an app.
// IDs are unique across the store, not only within one app.
type Deployment struct {
	ID         string     `json:"id"`
	AppName    string     `json:"app_name"`
	Ref        string     `json:"ref"`
	CommitSHA  string     `json:"commit_sha"`
	Status     string     `json:"status"`
	ImageTag   string     `json:"image_tag"`
	Error      string     `json:"error"`
	CreatedAt  time.Time  `json:"created_at"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// DeploymentUpdate patches a deployment.
// A nil field is left unchanged. Status changes must follow CanTransition.
// Fields are applied together, so a transition to a terminal status can record
// the image tag, commit, error, and finish time in the same call.
type DeploymentUpdate struct {
	Status     *string
	Ref        *string
	CommitSHA  *string
	ImageTag   *string
	Error      *string
	StartedAt  *time.Time
	FinishedAt *time.Time
}

// LogRead is a snapshot of a deployment log from an offset.
type LogRead struct {
	Data     []byte
	Next     int
	Total    int
	Status   string
	Terminal bool
}

// Store persists apps and deployments.
// Implementations must be safe for concurrent use.
type Store interface {
	// CreateApp stores an app. name and repoURL are trimmed.
	// Empty values return ErrInvalid. A duplicate name returns ErrAlreadyExists.
	CreateApp(ctx context.Context, name, repoURL string) (App, error)
	// GetApp returns the app with the given name.
	GetApp(ctx context.Context, name string) (App, error)
	// ListApps returns apps in creation order.
	ListApps(ctx context.Context) ([]App, error)

	// CreateDeployment stores a deployment for an existing app.
	// status must be 1-32 characters of lowercase letters, digits, '_' or '-'.
	CreateDeployment(ctx context.Context, appName, status string) (Deployment, error)
	// CreateBuild stores a queued build for an existing app.
	// ref is empty for the repository default branch. Invalid refs return ErrInvalid.
	CreateBuild(ctx context.Context, appName, ref string) (Deployment, error)
	// UpdateDeployment applies upd to an existing deployment.
	// An illegal status change or an update of a terminal deployment returns ErrInvalid.
	UpdateDeployment(ctx context.Context, id string, upd DeploymentUpdate) (Deployment, error)
	// GetDeployment returns a deployment by its store-wide ID.
	GetDeployment(ctx context.Context, id string) (Deployment, error)
	// ListDeployments returns deployments for one app in creation order.
	ListDeployments(ctx context.Context, appName string) ([]Deployment, error)
	// AppendDeploymentLog adds bytes to the captured build log.
	// Writes past MaxDeploymentLogBytes are truncated.
	AppendDeploymentLog(ctx context.Context, id string, p []byte) error
	// ReadDeploymentLog returns the log from offset and the current status.
	ReadDeploymentLog(ctx context.Context, id string, offset int) (LogRead, error)
	// WaitDeploymentLog returns when the log grows past offset, the deployment
	// reaches a terminal status, or ctx is done.
	WaitDeploymentLog(ctx context.Context, id string, offset int) error
}

// IsTerminalStatus reports whether a build has finished.
func IsTerminalStatus(status string) bool {
	return status == DeploymentStatusSucceeded || status == DeploymentStatusFailed
}

// CanTransition reports whether a build may move from one status to another.
func CanTransition(from, to string) bool {
	switch from {
	case DeploymentStatusQueued:
		return to == DeploymentStatusBuilding || to == DeploymentStatusFailed
	case DeploymentStatusBuilding:
		return to == DeploymentStatusSucceeded || to == DeploymentStatusFailed
	default:
		return false
	}
}
