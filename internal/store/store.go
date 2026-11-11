// Package store defines the application and deployment persistence interface.
//
// M0 ships an in-memory implementation. M1 records image builds in the same
// store: status, git ref, commit, image tag, timestamps, error text, and the
// captured build log. M2 adds the running and stopped statuses, a public URL,
// and the app's current routing status. M4 adds the branch, auto-deploy
// setting, webhook delivery ids, and the releases kept for rollback. M5 adds
// an append-only per-app event log. Memory is still what tests use. SQLite is
// the durable implementation.
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
	// DeploymentStatusSucceeded means the image was built and no runtime is
	// configured to start it. With a runtime, a successful build moves to
	// starting instead.
	DeploymentStatusSucceeded = "succeeded"
	// DeploymentStatusStarting means the image was built and a container is
	// waiting for the health check.
	DeploymentStatusStarting = "starting"
	// DeploymentStatusRunning means the container is healthy and receiving traffic.
	DeploymentStatusRunning = "running"
	// DeploymentStatusStopped means a running container was replaced or stopped.
	DeploymentStatusStopped = "stopped"
	// DeploymentStatusFailed means the build or the health gate stopped with an error.
	DeploymentStatusFailed = "failed"

	// AppStatusIdle means the app has no container receiving traffic.
	AppStatusIdle = "idle"
	// AppStatusRunning means traffic is routed to a healthy container.
	AppStatusRunning = "running"
	// AppStatusStopped means the last running container was stopped.
	AppStatusStopped = "stopped"
	// AppStatusUnhealthy means the running container failed repeated health checks.
	// The public URL is kept so the proxy and the dashboard still name the app.
	AppStatusUnhealthy = "unhealthy"

	// MaxDeploymentLogBytes is the captured build log kept per deployment.
	MaxDeploymentLogBytes = 1 << 20

	// DefaultBranch is the branch push events deploy when an app does not set one.
	DefaultBranch = "main"
	// MaxWebhookSecretBytes is the longest per-app webhook secret that is stored.
	MaxWebhookSecretBytes = 256
	// MaxDeliveryIDBytes is the longest GitHub delivery id that is stored.
	MaxDeliveryIDBytes = 128
)

// App is a registered source repository.
// Status and URL describe where traffic is routed.
// WebhookSecret is the per-app GitHub HMAC secret. It is never serialized.
type App struct {
	Name          string    `json:"name"`
	RepoURL       string    `json:"repo_url"`
	Branch        string    `json:"branch"`
	AutoDeploy    bool      `json:"auto_deploy"`
	Status        string    `json:"status"`
	URL           string    `json:"url,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	WebhookSecret string    `json:"-"`
}

// AppSettings is the push-to-deploy configuration for an app.
// An empty Branch means DefaultBranch. An empty WebhookSecret clears nothing
// by itself: ConfigureApp stores the value it is given, including empty.
type AppSettings struct {
	Branch        string
	AutoDeploy    bool
	WebhookSecret string
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
	URL        string     `json:"url,omitempty"`
	Error      string     `json:"error"`
	RollbackOf string     `json:"rollback_of,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// Release is one successful image kept for rollback.
// CreatedAt is when the release was recorded, which is when the deployment
// became running or succeeded.
type Release struct {
	AppName      string    `json:"app_name"`
	DeploymentID string    `json:"deployment_id"`
	Ref          string    `json:"ref"`
	CommitSHA    string    `json:"commit_sha"`
	ImageTag     string    `json:"image_tag"`
	CreatedAt    time.Time `json:"created_at"`
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
	URL        *string
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
	// SetAppStatus records whether an app is idle, running, or stopped.
	// A running app requires a public URL. Idle and stopped clear it.
	SetAppStatus(ctx context.Context, name, status, appURL string) (App, error)
	// ConfigureApp sets the branch, auto-deploy flag, and per-app webhook secret.
	ConfigureApp(ctx context.Context, name string, settings AppSettings) (App, error)
	// ListAppsByRepo returns apps whose stored repository URL matches repoURL
	// after canonicalization, in creation order.
	ListAppsByRepo(ctx context.Context, repoURL string) ([]App, error)

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

	// ClaimDelivery records a GitHub delivery id.
	// The first call returns true. A repeat of the same id returns false.
	ClaimDelivery(ctx context.Context, id string) (bool, error)
	// ForgetDelivery removes a delivery id so the sender can retry it.
	ForgetDelivery(ctx context.Context, id string) error

	// CreateRollback stores a queued deployment that redeploys source's image.
	CreateRollback(ctx context.Context, appName string, source Release) (Deployment, error)
	// RecordRelease keeps a successful deployment and drops the oldest releases
	// beyond keep. keep must be at least 1. Recording the same deployment again
	// does not change the list. Dropped releases are returned oldest first.
	RecordRelease(ctx context.Context, deploymentID string, keep int) ([]Release, error)
	// ListReleases returns retained releases for one app, newest first.
	ListReleases(ctx context.Context, appName string) ([]Release, error)

	// Ping reports whether the store can answer a query.
	Ping(ctx context.Context) error
	// AppendEvent adds one append-only event for an existing app.
	AppendEvent(ctx context.Context, ev Event) (Event, error)
	// ListEvents returns events for one app, newest first.
	// limit is clamped to the range 1..MaxEventLimit. Zero uses DefaultEventLimit.
	ListEvents(ctx context.Context, appName string, limit int) ([]Event, error)
	// CountActiveDeployments counts deployments that are queued, building,
	// starting, or running.
	CountActiveDeployments(ctx context.Context) (int, error)
}

// IsTerminalStatus reports whether log streaming can stop.
// running can still move to stopped; succeeded, failed, and stopped cannot.
func IsTerminalStatus(status string) bool {
	switch status {
	case DeploymentStatusSucceeded, DeploymentStatusFailed, DeploymentStatusRunning, DeploymentStatusStopped:
		return true
	default:
		return false
	}
}

// CanTransition reports whether a deployment may move from one status to another.
func CanTransition(from, to string) bool {
	switch from {
	case DeploymentStatusQueued:
		return to == DeploymentStatusBuilding || to == DeploymentStatusFailed
	case DeploymentStatusBuilding:
		return to == DeploymentStatusSucceeded || to == DeploymentStatusStarting || to == DeploymentStatusFailed
	case DeploymentStatusStarting:
		return to == DeploymentStatusRunning || to == DeploymentStatusFailed
	case DeploymentStatusRunning:
		return to == DeploymentStatusStopped
	default:
		return false
	}
}
