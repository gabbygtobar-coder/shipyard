package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/gabbygtobar-coder/shipyard/internal/route"
	"github.com/gabbygtobar-coder/shipyard/internal/store"
)

const (
	defaultHealthTimeout = 30 * time.Second
	defaultStartTimeout  = 60 * time.Second
	defaultStopTimeout   = 15 * time.Second
)

// Supervisor starts a container after a build, waits for health, then swaps
// traffic off the previous container.
type Supervisor struct {
	store         store.Store
	rt            Runtime
	health        Checker
	routes        *route.Table
	port          int
	memory        int64
	cpu           int
	network       string
	healthTimeout time.Duration
	startTimeout  time.Duration
	publicURL     func(app string) string
	log           *slog.Logger
	now           func() time.Time

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// SupervisorConfig configures a Supervisor.
// Zero health and start timeouts use the defaults.
type SupervisorConfig struct {
	Store         store.Store
	Runtime       Runtime
	Checker       Checker
	Routes        *route.Table
	Port          int
	MemoryBytes   int64
	CPUMilli      int
	Network       string
	HealthTimeout time.Duration
	// StartTimeout bounds Runtime.Start. Zero uses 60s, which fits a local
	// container. ECS sets a longer timeout because Fargate startup is slower.
	StartTimeout time.Duration
	PublicURL    func(app string) string
	Logger       *slog.Logger
	Now          func() time.Time
}

// NewSupervisor returns a supervisor that has not started any containers.
func NewSupervisor(cfg SupervisorConfig) (*Supervisor, error) {
	if cfg.Store == nil || cfg.Runtime == nil || cfg.Checker == nil || cfg.Routes == nil || cfg.PublicURL == nil {
		return nil, errors.New("store, runtime, checker, routes, and public url are required")
	}
	if cfg.Port == 0 {
		cfg.Port = DefaultContainerPort
	}
	if cfg.MemoryBytes <= 0 {
		cfg.MemoryBytes = 256 << 20
	}
	if cfg.CPUMilli <= 0 {
		cfg.CPUMilli = 500
	}
	if cfg.HealthTimeout <= 0 {
		cfg.HealthTimeout = defaultHealthTimeout
	}
	startTimeout := defaultStartTimeout
	if cfg.StartTimeout > 0 {
		startTimeout = cfg.StartTimeout
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Supervisor{
		store:         cfg.Store,
		rt:            cfg.Runtime,
		health:        cfg.Checker,
		routes:        cfg.Routes,
		port:          cfg.Port,
		memory:        cfg.MemoryBytes,
		cpu:           cfg.CPUMilli,
		network:       cfg.Network,
		healthTimeout: cfg.HealthTimeout,
		startTimeout:  startTimeout,
		publicURL:     cfg.PublicURL,
		log:           cfg.Logger,
		now:           cfg.Now,
		locks:         make(map[string]*sync.Mutex),
	}, nil
}

// Release starts image for deploymentID, and on a healthy response routes
// traffic to it and stops the previous container.
// The deployment must already be starting. On success it is running.
// On failure the new container is stopped and the deployment is left starting.
func (s *Supervisor) Release(ctx context.Context, appName, deploymentID, image string, logs io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if logs == nil {
		logs = io.Discard
	}
	unlock := s.lockApp(appName)
	defer unlock()

	name, err := ContainerName(appName, deploymentID)
	if err != nil {
		return err
	}
	spec := Spec{
		Name:          name,
		Image:         image,
		AppName:       appName,
		DeploymentID:  deploymentID,
		ContainerPort: s.port,
		MemoryBytes:   s.memory,
		CPUMilli:      s.cpu,
		Network:       s.network,
	}
	_, _ = fmt.Fprintf(logs, "starting container %s\n", name)
	startCtx, startCancel := context.WithTimeout(ctx, s.startTimeout)
	inst, err := s.rt.Start(startCtx, spec)
	startCancel()
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(startCtx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("%w: %w", ErrStartTimeout, err)
		}
		return err
	}

	_, _ = fmt.Fprintf(logs, "health %s\n", inst.Endpoint)
	healthCtx, healthCancel := context.WithTimeout(ctx, s.healthTimeout)
	healthErr := s.health.Wait(healthCtx, inst.Endpoint)
	healthCancel()
	if healthErr != nil {
		s.stopContainer(inst.ID)
		if errors.Is(healthErr, ErrHealthTimeout) || errors.Is(healthErr, context.DeadlineExceeded) || errors.Is(healthCtx.Err(), context.DeadlineExceeded) {
			return ErrHealthTimeout
		}
		return healthErr
	}
	public := s.publicURL(appName)
	if public == "" {
		s.stopContainer(inst.ID)
		return errors.New("public url is empty")
	}
	prev, _ := s.routes.Lookup(appName)
	_, _ = fmt.Fprintf(logs, "healthy\n")
	_, _ = fmt.Fprintf(logs, "routing %s\n", public)
	if prev.DeploymentID != "" && prev.DeploymentID != deploymentID {
		_, _ = fmt.Fprintf(logs, "stopping %s\n", prev.DeploymentID)
	}
	if err := s.markRunning(ctx, deploymentID, appName, public); err != nil {
		s.stopContainer(inst.ID)
		return err
	}
	s.routes.Swap(appName, route.Target{
		Endpoint:     inst.Endpoint,
		InstanceID:   inst.ID,
		DeploymentID: deploymentID,
	})
	if prev.InstanceID != "" && prev.InstanceID != inst.ID {
		s.stopContainer(prev.InstanceID)
		s.markStopped(prev.DeploymentID)
	}
	return nil
}

// Stop removes the app's route and stops its container.
// The deployment that was receiving traffic is marked stopped.
func (s *Supervisor) Stop(ctx context.Context, appName string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	unlock := s.lockApp(appName)
	defer unlock()
	prev, ok := s.routes.Clear(appName)
	if !ok || prev.InstanceID == "" {
		return "", ErrNotRunning
	}
	s.stopContainer(prev.InstanceID)
	s.markStopped(prev.DeploymentID)
	if _, err := s.store.SetAppStatus(context.Background(), appName, store.AppStatusStopped, ""); err != nil {
		return "", err
	}
	return prev.DeploymentID, nil
}

// Shutdown stops every container that is still routed.
func (s *Supervisor) Shutdown(ctx context.Context) {
	for _, app := range s.routes.Apps() {
		if _, err := s.Stop(ctx, app); err != nil && !errors.Is(err, ErrNotRunning) {
			s.log.Error("shutdown app", "app", app, "err", err)
		}
	}
}

func (s *Supervisor) markRunning(ctx context.Context, deploymentID, appName, public string) error {
	status := store.DeploymentStatusRunning
	now := s.now().UTC()
	if _, err := s.store.UpdateDeployment(ctx, deploymentID, store.DeploymentUpdate{
		Status:     &status,
		URL:        &public,
		FinishedAt: &now,
	}); err != nil {
		return err
	}
	if _, err := s.store.SetAppStatus(ctx, appName, store.AppStatusRunning, public); err != nil {
		return err
	}
	return nil
}

func (s *Supervisor) markStopped(deploymentID string) {
	if deploymentID == "" {
		return
	}
	status := store.DeploymentStatusStopped
	if _, err := s.store.UpdateDeployment(context.Background(), deploymentID, store.DeploymentUpdate{Status: &status}); err != nil && !errors.Is(err, store.ErrInvalid) && !errors.Is(err, store.ErrNotFound) {
		s.log.Error("mark stopped", "deployment", deploymentID, "err", err)
	}
}

func (s *Supervisor) stopContainer(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultStopTimeout)
	defer cancel()
	if err := s.rt.Stop(ctx, id); err != nil {
		s.log.Error("stop container", "id", id, "err", err)
	}
}

func (s *Supervisor) lockApp(app string) func() {
	s.mu.Lock()
	lock := s.locks[app]
	if lock == nil {
		lock = &sync.Mutex{}
		s.locks[app] = lock
	}
	s.mu.Unlock()
	lock.Lock()
	return lock.Unlock
}
