package store

import (
	"bytes"
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gabbygtobar-coder/shipyard/internal/giturl"
)

const logPollInterval = 25 * time.Millisecond

// Memory is an in-memory Store safe for concurrent use.
type Memory struct {
	mu       sync.Mutex
	now      func() time.Time
	apps     map[string]App
	appOrder []string
	deps     map[string]Deployment
	depOrder []string
	logs     map[string]*bytes.Buffer
	logTrunc map[string]bool
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
		now:      now,
		apps:     make(map[string]App),
		deps:     make(map[string]Deployment),
		logs:     make(map[string]*bytes.Buffer),
		logTrunc: make(map[string]bool),
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

	dep, err := m.appendDeployment(appName, status, "")
	if err != nil {
		return Deployment{}, err
	}
	return cloneDeployment(dep), nil
}

// CreateBuild implements Store.
func (m *Memory) CreateBuild(ctx context.Context, appName, ref string) (Deployment, error) {
	if err := ctx.Err(); err != nil {
		return Deployment{}, err
	}
	appName = strings.TrimSpace(appName)
	ref = strings.TrimSpace(ref)
	if appName == "" || giturl.ValidateRef(ref) != nil {
		return Deployment{}, ErrInvalid
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	dep, err := m.appendDeployment(appName, DeploymentStatusQueued, ref)
	if err != nil {
		return Deployment{}, err
	}
	return cloneDeployment(dep), nil
}

// appendDeployment inserts a deployment. The caller holds m.mu.
func (m *Memory) appendDeployment(appName, status, ref string) (Deployment, error) {
	if _, ok := m.apps[appName]; !ok {
		return Deployment{}, ErrNotFound
	}
	m.nextDep++
	dep := Deployment{
		ID:        "dep_" + strconv.Itoa(m.nextDep),
		AppName:   appName,
		Ref:       ref,
		Status:    status,
		CreatedAt: m.now().UTC(),
	}
	m.deps[dep.ID] = dep
	m.depOrder = append(m.depOrder, dep.ID)
	m.logs[dep.ID] = &bytes.Buffer{}
	return dep, nil
}

// UpdateDeployment implements Store.
func (m *Memory) UpdateDeployment(ctx context.Context, id string, upd DeploymentUpdate) (Deployment, error) {
	if err := ctx.Err(); err != nil {
		return Deployment{}, err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return Deployment{}, ErrInvalid
	}
	if upd.Ref != nil {
		if err := giturl.ValidateRef(strings.TrimSpace(*upd.Ref)); err != nil {
			return Deployment{}, ErrInvalid
		}
	}
	if upd.Error != nil && len(*upd.Error) > 4096 {
		trimmed := (*upd.Error)[:4096]
		upd.Error = &trimmed
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	dep, ok := m.deps[id]
	if !ok {
		return Deployment{}, ErrNotFound
	}
	if IsTerminalStatus(dep.Status) {
		if deploymentUpdateChanges(dep, upd) {
			return Deployment{}, ErrInvalid
		}
		return cloneDeployment(dep), nil
	}
	if upd.Status != nil {
		status := strings.TrimSpace(*upd.Status)
		if status != dep.Status {
			if !validDeploymentStatus(status) || !CanTransition(dep.Status, status) {
				return Deployment{}, ErrInvalid
			}
			dep.Status = status
		}
	}
	if upd.Ref != nil {
		dep.Ref = strings.TrimSpace(*upd.Ref)
	}
	if upd.CommitSHA != nil {
		dep.CommitSHA = strings.TrimSpace(*upd.CommitSHA)
	}
	if upd.ImageTag != nil {
		dep.ImageTag = strings.TrimSpace(*upd.ImageTag)
	}
	if upd.Error != nil {
		dep.Error = strings.TrimSpace(*upd.Error)
	}
	if upd.StartedAt != nil {
		t := upd.StartedAt.UTC()
		dep.StartedAt = &t
	}
	if upd.FinishedAt != nil {
		t := upd.FinishedAt.UTC()
		dep.FinishedAt = &t
	}
	m.deps[id] = dep
	return cloneDeployment(dep), nil
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
	return cloneDeployment(dep), nil
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
			out = append(out, cloneDeployment(dep))
		}
	}
	return out, nil
}

// AppendDeploymentLog implements Store.
func (m *Memory) AppendDeploymentLog(ctx context.Context, id string, p []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(p) == 0 {
		return nil
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return ErrInvalid
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.deps[id]; !ok {
		return ErrNotFound
	}
	buf := m.logs[id]
	if buf == nil {
		buf = &bytes.Buffer{}
		m.logs[id] = buf
	}
	if buf.Len() >= MaxDeploymentLogBytes {
		return nil
	}
	remain := MaxDeploymentLogBytes - buf.Len()
	if len(p) <= remain {
		_, _ = buf.Write(p)
		return nil
	}
	_, _ = buf.Write(p[:remain])
	if !m.logTrunc[id] {
		_, _ = buf.WriteString("\n[log truncated]\n")
		m.logTrunc[id] = true
	}
	return nil
}

// ReadDeploymentLog implements Store.
func (m *Memory) ReadDeploymentLog(ctx context.Context, id string, offset int) (LogRead, error) {
	if err := ctx.Err(); err != nil {
		return LogRead{}, err
	}
	if offset < 0 {
		return LogRead{}, ErrInvalid
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return LogRead{}, ErrInvalid
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	dep, ok := m.deps[id]
	if !ok {
		return LogRead{}, ErrNotFound
	}
	var raw []byte
	if buf := m.logs[id]; buf != nil {
		raw = buf.Bytes()
	}
	total := len(raw)
	if offset > total {
		offset = total
	}
	return LogRead{
		Data:     append([]byte(nil), raw[offset:]...),
		Next:     total,
		Total:    total,
		Status:   dep.Status,
		Terminal: IsTerminalStatus(dep.Status),
	}, nil
}

// WaitDeploymentLog implements Store.
func (m *Memory) WaitDeploymentLog(ctx context.Context, id string, offset int) error {
	ticker := time.NewTicker(logPollInterval)
	defer ticker.Stop()
	for {
		snap, err := m.ReadDeploymentLog(ctx, id, offset)
		if err != nil {
			return err
		}
		if len(snap.Data) > 0 || snap.Terminal {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func deploymentUpdateChanges(dep Deployment, upd DeploymentUpdate) bool {
	if upd.Status != nil && strings.TrimSpace(*upd.Status) != dep.Status {
		return true
	}
	if upd.Ref != nil && strings.TrimSpace(*upd.Ref) != dep.Ref {
		return true
	}
	if upd.CommitSHA != nil && strings.TrimSpace(*upd.CommitSHA) != dep.CommitSHA {
		return true
	}
	if upd.ImageTag != nil && strings.TrimSpace(*upd.ImageTag) != dep.ImageTag {
		return true
	}
	if upd.Error != nil && strings.TrimSpace(*upd.Error) != dep.Error {
		return true
	}
	if upd.StartedAt != nil {
		return true
	}
	if upd.FinishedAt != nil {
		return true
	}
	return false
}

func cloneDeployment(dep Deployment) Deployment {
	if dep.StartedAt != nil {
		t := dep.StartedAt.UTC()
		dep.StartedAt = &t
	}
	if dep.FinishedAt != nil {
		t := dep.FinishedAt.UTC()
		dep.FinishedAt = &t
	}
	return dep
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
