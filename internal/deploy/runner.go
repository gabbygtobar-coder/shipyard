// Package deploy builds container images for registered applications.
//
// A bounded worker pool clones the GitHub repository, requires a Dockerfile
// at the repository root, and tags the image shipyard/<app>:<short-sha>.
package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gabbygtobar-coder/shipyard/internal/build"
	"github.com/gabbygtobar-coder/shipyard/internal/giturl"
	"github.com/gabbygtobar-coder/shipyard/internal/source"
	"github.com/gabbygtobar-coder/shipyard/internal/store"
)

const (
	defaultConcurrency  = 2
	defaultQueueSize    = 16
	defaultBuildTimeout = 10 * time.Minute
	defaultCloneTimeout = 2 * time.Minute

	msgCloneFailed       = "git clone failed"
	msgCloneTimedOut     = "git clone timed out"
	msgBuildFailed       = "docker build failed"
	msgBuildTimedOut     = "build timed out"
	msgCanceled          = "build canceled"
	msgDockerfileMissing = "no Dockerfile at repository root"
	msgDockerfileNotFile = "dockerfile must be a regular file at the repository root"
	msgInvalidSHA        = "invalid commit sha"
	msgInternal          = "internal error"
)

var (
	// ErrQueueFull is returned when every worker is busy and the queue is full.
	ErrQueueFull = errors.New("build queue is full")
	// ErrStopped is returned when the runner is not accepting builds.
	ErrStopped = errors.New("builds are not accepted")
)

// BuildService starts image builds.
type BuildService interface {
	// StartBuild queues a build of appName at ref.
	// An empty ref selects the repository default branch.
	StartBuild(ctx context.Context, appName, ref string) (store.Deployment, error)
}

// Config configures a Runner.
// Zero concurrency, queue size, and timeouts are replaced with defaults.
type Config struct {
	Store        store.Store
	Cloner       source.Cloner
	Builder      build.Builder
	Concurrency  int
	QueueSize    int
	BuildTimeout time.Duration
	CloneTimeout time.Duration
	Logger       *slog.Logger
	Now          func() time.Time
}

// Runner executes builds on a fixed set of workers.
type Runner struct {
	store        store.Store
	cloner       source.Cloner
	builder      build.Builder
	concurrency  int
	queueCap     int
	buildTimeout time.Duration
	cloneTimeout time.Duration
	log          *slog.Logger
	now          func() time.Time

	mu      sync.Mutex
	jobs    chan string
	started bool
	stopped bool
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

// New returns a runner that has not started.
func New(cfg Config) (*Runner, error) {
	if cfg.Store == nil || cfg.Cloner == nil || cfg.Builder == nil {
		return nil, errors.New("store, cloner, and builder are required")
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = defaultConcurrency
	}
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = defaultQueueSize
	}
	if cfg.BuildTimeout <= 0 {
		cfg.BuildTimeout = defaultBuildTimeout
	}
	if cfg.CloneTimeout <= 0 {
		cfg.CloneTimeout = defaultCloneTimeout
	}
	if cfg.CloneTimeout > cfg.BuildTimeout {
		cfg.CloneTimeout = cfg.BuildTimeout
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Runner{
		store:        cfg.Store,
		cloner:       cfg.Cloner,
		builder:      cfg.Builder,
		concurrency:  cfg.Concurrency,
		queueCap:     cfg.QueueSize,
		buildTimeout: cfg.BuildTimeout,
		cloneTimeout: cfg.CloneTimeout,
		log:          cfg.Logger,
		now:          cfg.Now,
	}, nil
}

// Start launches the worker pool. The pool stops when ctx is canceled or Stop is called.
func (r *Runner) Start(ctx context.Context) error {
	if ctx == nil {
		return errors.New("context is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started {
		return errors.New("runner already started")
	}
	r.started = true
	r.ctx, r.cancel = context.WithCancel(ctx)
	r.jobs = make(chan string, r.queueCap)
	for i := 0; i < r.concurrency; i++ {
		r.wg.Add(1)
		go r.worker()
	}
	return nil
}

// Stop cancels queued and in-flight builds and waits for workers to exit.
func (r *Runner) Stop() {
	r.mu.Lock()
	if !r.started {
		r.mu.Unlock()
		return
	}
	if !r.stopped {
		r.stopped = true
		close(r.jobs)
		r.cancel()
	}
	r.mu.Unlock()
	r.wg.Wait()
}

// StartBuild implements BuildService.
func (r *Runner) StartBuild(ctx context.Context, appName, ref string) (store.Deployment, error) {
	if err := ctx.Err(); err != nil {
		return store.Deployment{}, err
	}
	ref = strings.TrimSpace(ref)
	if err := giturl.ValidateRef(ref); err != nil {
		return store.Deployment{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.started || r.stopped {
		return store.Deployment{}, ErrStopped
	}
	if len(r.jobs) >= cap(r.jobs) {
		return store.Deployment{}, ErrQueueFull
	}
	dep, err := r.store.CreateBuild(ctx, appName, ref)
	if err != nil {
		return store.Deployment{}, err
	}
	r.jobs <- dep.ID
	return dep, nil
}

func (r *Runner) worker() {
	defer r.wg.Done()
	for id := range r.jobs {
		r.execute(id)
	}
}

func (r *Runner) execute(id string) {
	defer func() {
		if rec := recover(); rec != nil {
			r.log.Error("build panic", "deployment", id, "err", fmt.Sprint(rec))
			r.finish(id, store.DeploymentStatusFailed, "", "", msgInternal)
		}
	}()
	if r.ctx.Err() != nil {
		r.finish(id, store.DeploymentStatusFailed, "", "", msgCanceled)
		return
	}

	started := r.now().UTC()
	building := store.DeploymentStatusBuilding
	if _, err := r.store.UpdateDeployment(context.Background(), id, store.DeploymentUpdate{
		Status:    &building,
		StartedAt: &started,
	}); err != nil {
		r.log.Error("mark building", "deployment", id, "err", err)
		r.finish(id, store.DeploymentStatusFailed, "", "", msgInternal)
		return
	}

	dep, err := r.store.GetDeployment(context.Background(), id)
	if err != nil {
		r.log.Error("load deployment", "deployment", id, "err", err)
		r.finish(id, store.DeploymentStatusFailed, "", "", msgInternal)
		return
	}
	app, err := r.store.GetApp(context.Background(), dep.AppName)
	if err != nil {
		r.finish(id, store.DeploymentStatusFailed, "", "", msgInternal)
		return
	}

	jobCtx, jobCancel := context.WithTimeout(r.ctx, r.buildTimeout)
	defer jobCancel()

	dir, err := os.MkdirTemp("", "shipyard-build-")
	if err != nil {
		r.log.Error("temp dir", "deployment", id, "err", err)
		r.finish(id, store.DeploymentStatusFailed, "", "", msgInternal)
		return
	}
	defer func() { _ = os.RemoveAll(dir) }()
	fail := func(sha, msg string) {
		_ = os.RemoveAll(dir)
		r.finish(id, store.DeploymentStatusFailed, "", sha, msg)
	}

	logs := logWriter{store: r.store, id: id}
	refLabel := dep.Ref
	if refLabel == "" {
		refLabel = "default"
	}
	_, _ = fmt.Fprintf(&logs, "cloning %s ref %s\n", app.RepoURL, refLabel)

	cloneCtx, cloneCancel := context.WithTimeout(jobCtx, r.cloneTimeout)
	defer cloneCancel()
	sha, cloneErr := r.cloner.Clone(cloneCtx, app.RepoURL, dep.Ref, dir, &logs)
	if cloneErr != nil {
		_, _ = fmt.Fprintf(&logs, "error: %s\n", cloneErr.Error())
		fail("", cloneFailure(r.ctx, jobCtx, cloneCtx, cloneErr))
		return
	}
	sha = strings.ToLower(strings.TrimSpace(sha))
	if !giturl.ValidSHA(sha) {
		_, _ = fmt.Fprintf(&logs, "error: %s\n", msgInvalidSHA)
		fail("", msgInvalidSHA)
		return
	}
	if _, err := r.store.UpdateDeployment(context.Background(), id, store.DeploymentUpdate{CommitSHA: &sha}); err != nil {
		r.log.Error("record sha", "deployment", id, "err", err)
		fail(sha, msgInternal)
		return
	}
	_, _ = fmt.Fprintf(&logs, "commit %s\n", sha)

	dockerfile := filepath.Join(dir, "Dockerfile")
	info, statErr := os.Lstat(dockerfile)
	if statErr != nil {
		if errors.Is(statErr, os.ErrNotExist) {
			_, _ = fmt.Fprintf(&logs, "error: %s\n", msgDockerfileMissing)
			fail(sha, msgDockerfileMissing)
			return
		}
		r.log.Error("stat dockerfile", "deployment", id, "err", statErr)
		fail(sha, msgInternal)
		return
	}
	if !info.Mode().IsRegular() {
		_, _ = fmt.Fprintf(&logs, "error: %s\n", msgDockerfileNotFile)
		fail(sha, msgDockerfileNotFile)
		return
	}
	if err := checkTree(dir); err != nil {
		msg := msgInternal
		if errors.Is(err, errOutsideSymlink) {
			msg = err.Error()
		} else {
			r.log.Error("check tree", "deployment", id, "err", err)
		}
		_, _ = fmt.Fprintf(&logs, "error: %s\n", msg)
		fail(sha, msg)
		return
	}

	tag, err := imageTag(app.Name, sha)
	if err != nil {
		r.log.Error("image tag", "deployment", id, "err", err)
		fail(sha, msgInternal)
		return
	}
	_, _ = fmt.Fprintf(&logs, "building %s\n", tag)
	buildErr := r.builder.Build(jobCtx, dir, tag, &logs)
	if buildErr != nil {
		_, _ = fmt.Fprintf(&logs, "error: %s\n", buildErr.Error())
		fail(sha, buildFailure(r.ctx, jobCtx, buildErr))
		return
	}
	_, _ = fmt.Fprintf(&logs, "built %s\n", tag)
	_ = os.RemoveAll(dir)
	r.finish(id, store.DeploymentStatusSucceeded, tag, sha, "")
}

func imageTag(app, sha string) (string, error) {
	short := sha
	if len(short) > 12 {
		short = short[:12]
	}
	tag := "shipyard/" + app + ":" + short
	if err := build.ValidateTag(tag); err != nil {
		return "", err
	}
	return tag, nil
}

func cloneFailure(parent, job, clone context.Context, err error) string {
	switch {
	case errors.Is(parent.Err(), context.Canceled):
		return msgCanceled
	case errors.Is(err, source.ErrCloneTooLarge):
		return source.ErrCloneTooLarge.Error()
	case errors.Is(err, source.ErrGitNotFound):
		return source.ErrGitNotFound.Error()
	case errors.Is(err, source.ErrInvalidCommit), errors.Is(err, giturl.ErrRepoInvalid), errors.Is(err, giturl.ErrRefInvalid):
		return err.Error()
	case errors.Is(job.Err(), context.DeadlineExceeded):
		return msgBuildTimedOut
	case errors.Is(clone.Err(), context.DeadlineExceeded), errors.Is(err, context.DeadlineExceeded):
		return msgCloneTimedOut
	case errors.Is(err, context.Canceled):
		return msgCanceled
	default:
		return msgCloneFailed
	}
}

func buildFailure(parent, job context.Context, err error) string {
	switch {
	case errors.Is(parent.Err(), context.Canceled):
		return msgCanceled
	case errors.Is(err, build.ErrDockerNotFound):
		return build.ErrDockerNotFound.Error()
	case errors.Is(job.Err(), context.DeadlineExceeded), errors.Is(err, context.DeadlineExceeded):
		return msgBuildTimedOut
	case errors.Is(err, context.Canceled):
		return msgCanceled
	default:
		return msgBuildFailed
	}
}

func (r *Runner) finish(id, status, image, sha, errMsg string) {
	now := r.now().UTC()
	upd := store.DeploymentUpdate{
		Status:     &status,
		FinishedAt: &now,
	}
	if image != "" {
		upd.ImageTag = &image
	}
	if sha != "" {
		upd.CommitSHA = &sha
	}
	if errMsg != "" {
		upd.Error = &errMsg
	}
	if _, err := r.store.UpdateDeployment(context.Background(), id, upd); err != nil && !errors.Is(err, store.ErrInvalid) && !errors.Is(err, store.ErrNotFound) {
		r.log.Error("finish deployment", "deployment", id, "err", err)
	}
}

type logWriter struct {
	store store.Store
	id    string
}

func (w logWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if err := w.store.AppendDeploymentLog(context.Background(), w.id, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

var _ BuildService = (*Runner)(nil)
