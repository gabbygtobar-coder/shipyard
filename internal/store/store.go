// Package store defines the application and deployment persistence interface.
//
// M0 ships an in-memory implementation. Deployment records are stored for
// later milestones and are not exposed by the HTTP API yet.
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
	// DeploymentStatusPending is the status recorded before a deploy is executed.
	DeploymentStatusPending = "pending"
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
	ID        string    `json:"id"`
	AppName   string    `json:"app_name"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
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
	// GetDeployment returns a deployment by its store-wide ID.
	GetDeployment(ctx context.Context, id string) (Deployment, error)
	// ListDeployments returns deployments for one app in creation order.
	ListDeployments(ctx context.Context, appName string) ([]Deployment, error)
}
