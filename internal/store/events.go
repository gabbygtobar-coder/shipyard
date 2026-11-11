package store

import (
	"regexp"
	"strings"
	"time"
)

const (
	// EventDeployed is recorded when a deployment is serving or its image is built.
	EventDeployed = "deployed"
	// EventRolledBack is recorded when a rollback deployment becomes current.
	EventRolledBack = "rolled_back"
	// EventHealthFailed is recorded when repeated health checks mark an app unhealthy.
	EventHealthFailed = "health_failed"
	// EventWebhookReceived is recorded when a verified webhook names the app.
	EventWebhookReceived = "webhook_received"

	// MaxEventMessageBytes is the longest event message that is stored.
	MaxEventMessageBytes = 512
	// DefaultEventLimit is how many events ListEvents returns when limit is zero.
	DefaultEventLimit = 100
	// MaxEventLimit is the most events one ListEvents call returns.
	MaxEventLimit = 500
)

// Event is one append-only record in an application's timeline.
type Event struct {
	ID           int64     `json:"id"`
	AppName      string    `json:"app_name"`
	Type         string    `json:"type"`
	Message      string    `json:"message"`
	DeploymentID string    `json:"deployment_id,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

var eventDeploymentPattern = regexp.MustCompile(`^dep_[1-9][0-9]{0,17}$`)

func validEventType(kind string) bool {
	switch kind {
	case EventDeployed, EventRolledBack, EventHealthFailed, EventWebhookReceived:
		return true
	default:
		return false
	}
}

func normalizeEvent(ev Event) (Event, error) {
	ev.AppName = strings.TrimSpace(ev.AppName)
	ev.Type = strings.TrimSpace(ev.Type)
	ev.Message = strings.TrimSpace(ev.Message)
	ev.DeploymentID = strings.TrimSpace(ev.DeploymentID)
	if ev.AppName == "" || !validEventType(ev.Type) {
		return Event{}, ErrInvalid
	}
	if len(ev.Message) > MaxEventMessageBytes || strings.ContainsRune(ev.Message, 0) {
		return Event{}, ErrInvalid
	}
	if ev.DeploymentID != "" && !eventDeploymentPattern.MatchString(ev.DeploymentID) {
		return Event{}, ErrInvalid
	}
	return ev, nil
}

func clampEventLimit(limit int) int {
	if limit <= 0 {
		return DefaultEventLimit
	}
	if limit > MaxEventLimit {
		return MaxEventLimit
	}
	return limit
}

func activeDeployment(status string) bool {
	switch status {
	case DeploymentStatusQueued, DeploymentStatusBuilding, DeploymentStatusStarting, DeploymentStatusRunning:
		return true
	default:
		return false
	}
}
