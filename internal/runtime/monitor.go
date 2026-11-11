package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/gabbygtobar-coder/shipyard/internal/route"
	"github.com/gabbygtobar-coder/shipyard/internal/store"
)

const defaultProbeTimeout = 2 * time.Second

// Restarter restarts one local container without building a new image.
// The Docker runtime implements it. The ECS runtime does not.
type Restarter interface {
	Restart(ctx context.Context, id string) error
}

// Monitor probes apps that are running or unhealthy.
// After Threshold consecutive failures it marks the app unhealthy and appends
// a health_failed event. When AutoRestart is set and Restarter is non-nil, it
// restarts that container once for the incident.
type Monitor struct {
	store        store.Store
	routes       *route.Table
	path         string
	interval     time.Duration
	threshold    int
	autoRestart  bool
	restarter    Restarter
	probeTimeout time.Duration
	client       *http.Client
	log          *slog.Logger

	mu        sync.Mutex
	incidents map[string]*incident
}

type incident struct {
	failures     int
	deploymentID string
	reported     bool
	restarted    bool
}

// MonitorConfig configures a Monitor.
type MonitorConfig struct {
	Store        store.Store
	Routes       *route.Table
	Path         string
	Interval     time.Duration
	Threshold    int
	AutoRestart  bool
	Restarter    Restarter
	ProbeTimeout time.Duration
	Logger       *slog.Logger
}

// NewMonitor returns a monitor that has not started.
func NewMonitor(cfg MonitorConfig) (*Monitor, error) {
	if cfg.Store == nil || cfg.Routes == nil {
		return nil, errors.New("store and routes are required")
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 10 * time.Second
	}
	if cfg.Threshold < 1 {
		cfg.Threshold = 3
	}
	if cfg.Path == "" {
		cfg.Path = "/"
	}
	if cfg.ProbeTimeout <= 0 {
		cfg.ProbeTimeout = defaultProbeTimeout
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Monitor{
		store:        cfg.Store,
		routes:       cfg.Routes,
		path:         cfg.Path,
		interval:     cfg.Interval,
		threshold:    cfg.Threshold,
		autoRestart:  cfg.AutoRestart,
		restarter:    cfg.Restarter,
		probeTimeout: cfg.ProbeTimeout,
		client: &http.Client{
			Timeout: cfg.ProbeTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		log:       cfg.Logger,
		incidents: make(map[string]*incident),
	}, nil
}

// Run probes until ctx is canceled.
func (m *Monitor) Run(ctx context.Context) {
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.Tick(ctx)
		}
	}
}

// Tick probes every running or unhealthy app once.
func (m *Monitor) Tick(ctx context.Context) {
	if err := ctx.Err(); err != nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	apps, err := m.store.ListApps(ctx)
	if err != nil {
		m.log.Error("health monitor list", "err", err)
		return
	}
	seen := make(map[string]bool, len(apps))
	for _, app := range apps {
		if app.Status != store.AppStatusRunning && app.Status != store.AppStatusUnhealthy {
			delete(m.incidents, app.Name)
			continue
		}
		seen[app.Name] = true
		m.probe(ctx, app)
	}
	for name := range m.incidents {
		if !seen[name] {
			delete(m.incidents, name)
		}
	}
}

func (m *Monitor) probe(ctx context.Context, app store.App) {
	target, ok := m.routes.Lookup(app.Name)
	probeCtx, cancel := context.WithTimeout(ctx, m.probeTimeout)
	var err error
	if !ok || target.Endpoint == "" {
		err = errors.New("no route")
	} else {
		err = ProbeOnce(probeCtx, target.Endpoint, m.path, m.client)
	}
	cancel()
	if ctx.Err() != nil {
		return
	}
	if err == nil {
		m.recover(ctx, app)
		return
	}
	m.fail(ctx, app, target, err)
}

func (m *Monitor) recover(ctx context.Context, app store.App) {
	delete(m.incidents, app.Name)
	if app.Status != store.AppStatusUnhealthy || app.URL == "" {
		return
	}
	if _, err := m.store.SetAppStatus(ctx, app.Name, store.AppStatusRunning, app.URL); err != nil {
		m.log.Error("health monitor recover", "app", app.Name, "err", err)
	}
}

func (m *Monitor) fail(ctx context.Context, app store.App, target route.Target, cause error) {
	inc := m.incidents[app.Name]
	if inc == nil || (target.DeploymentID != "" && inc.deploymentID != target.DeploymentID) {
		inc = &incident{deploymentID: target.DeploymentID}
		m.incidents[app.Name] = inc
	}
	inc.failures++
	if inc.failures < m.threshold {
		return
	}
	if app.URL != "" && app.Status != store.AppStatusUnhealthy {
		if _, err := m.store.SetAppStatus(ctx, app.Name, store.AppStatusUnhealthy, app.URL); err != nil {
			m.log.Error("health monitor status", "app", app.Name, "err", err)
		} else {
			app.Status = store.AppStatusUnhealthy
		}
	}
	restartNote := ""
	if m.autoRestart && !inc.restarted {
		restartNote = m.restart(ctx, app.Name, target.InstanceID)
		inc.restarted = true
	}
	if inc.reported {
		return
	}
	message := fmt.Sprintf("health check failed %d times: %s", inc.failures, cause.Error())
	if restartNote != "" {
		message = message + "; " + restartNote
	}
	if len(message) > store.MaxEventMessageBytes {
		message = message[:store.MaxEventMessageBytes]
	}
	if _, err := m.store.AppendEvent(ctx, store.Event{
		AppName:      app.Name,
		Type:         store.EventHealthFailed,
		Message:      message,
		DeploymentID: target.DeploymentID,
	}); err != nil {
		m.log.Error("health monitor event", "app", app.Name, "err", err)
		return
	}
	inc.reported = true
	m.log.Info("app unhealthy", "app", app.Name, "failures", inc.failures, "err", cause)
}

func (m *Monitor) restart(ctx context.Context, app, instanceID string) string {
	if m.restarter == nil {
		return "auto-restart skipped"
	}
	if instanceID == "" {
		return "auto-restart skipped"
	}
	if err := m.restarter.Restart(ctx, instanceID); err != nil {
		m.log.Error("auto-restart", "app", app, "id", instanceID, "err", err)
		return "auto-restart failed"
	}
	m.log.Info("auto-restart", "app", app, "id", instanceID)
	return "auto-restarted"
}
