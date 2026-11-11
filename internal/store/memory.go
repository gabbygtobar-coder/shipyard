package store

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Memory is an in-memory Store safe for concurrent use.
type Memory struct {
	mu       sync.Mutex
	now      func() time.Time
	apps     map[string]App
	appOrder []string
	deps     map[string]Deployment
	depOrder []string
	nextDep  int
}

// NewMemory returns an empty in-memory store that timestamps with time.Now.
func NewMemory() *Memory {
	return NewMemoryWithClock(time.Now)
}

// NewMemoryWithClock returns an empty in-memory store that timestamps with now.
// A nil clock falls back to time.Now.
func NewMemoryWithClock(now func() time.Time) *Memory {
	if now == nil {
		now = time.Now
	}
	return &Memory{
		now:  now,
		apps: make(map[string]App),
		deps: make(map[string]Deployment),
	}
}

// CreateApp implements Store.
func (m *Memory) CreateApp(ctx context.Context, name, repoURL string) (App, error) {
	if err := ctx.Err(); err != nil {
		return App{}, err
	}
	name = strings.TrimSpace(name)
	repoURL = strings.TrimSpace(repoURL)
	if name == "" || repoURL == "" {
		return App{}, ErrInvalid
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.apps[name]; ok {
		return App{}, ErrAlreadyExists
	}
	app := App{
		Name:      name,
		RepoURL:   repoURL,
		CreatedAt: m.now().UTC(),
	}
	m.apps[name] = app
	m.appOrder = append(m.appOrder, name)
	return app, nil
}

// GetApp implements Store.
func (m *Memory) GetApp(ctx context.Context, name string) (App, error) {
	if err := ctx.Err(); err != nil {
		return App{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return App{}, ErrInvalid
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	app, ok := m.apps[name]
	if !ok {
		return App{}, ErrNotFound
	}
	return app, nil
}

// ListApps implements Store.
func (m *Memory) ListApps(ctx context.Context) ([]App, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	apps := make([]App, 0, len(m.appOrder))
	for _, name := range m.appOrder {
		apps = append(apps, m.apps[name])
	}
	return apps, nil
}

// CreateDeployment implements Store.
func (m *Memory) CreateDeployment(ctx context.Context, appName, status string) (Deployment, error) {
	if err := ctx.Err(); err != nil {
		return Deployment{}, err
	}
	appName = strings.TrimSpace(appName)
	status = strings.TrimSpace(status)
	if appName == "" || !validDeploymentStatus(status) {
		return Deployment{}, ErrInvalid
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.apps[appName]; !ok {
		return Deployment{}, ErrNotFound
	}
	m.nextDep++
	dep := Deployment{
		ID:        "dep_" + strconv.Itoa(m.nextDep),
		AppName:   appName,
		Status:    status,
		CreatedAt: m.now().UTC(),
	}
	m.deps[dep.ID] = dep
	m.depOrder = append(m.depOrder, dep.ID)
	return dep, nil
}

// GetDeployment implements Store.
func (m *Memory) GetDeployment(ctx context.Context, id string) (Deployment, error) {
	if err := ctx.Err(); err != nil {
		return Deployment{}, err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return Deployment{}, ErrInvalid
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	dep, ok := m.deps[id]
	if !ok {
		return Deployment{}, ErrNotFound
	}
	return dep, nil
}

// ListDeployments implements Store.
func (m *Memory) ListDeployments(ctx context.Context, appName string) ([]Deployment, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	appName = strings.TrimSpace(appName)
	if appName == "" {
		return nil, ErrInvalid
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.apps[appName]; !ok {
		return nil, ErrNotFound
	}
	out := make([]Deployment, 0)
	for _, id := range m.depOrder {
		dep := m.deps[id]
		if dep.AppName == appName {
			out = append(out, dep)
		}
	}
	return out, nil
}

func validDeploymentStatus(status string) bool {
	if status == "" || len(status) > 32 {
		return false
	}
	for _, r := range status {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '-':
		default:
			return false
		}
	}
	return true
}

// Compile-time check that Memory implements Store.
var _ Store = (*Memory)(nil)
