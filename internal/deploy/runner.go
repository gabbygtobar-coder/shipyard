// Package deploy builds container images for registered applications.
//
// A bounded worker pool clones the GitHub repository, requires a Dockerfile
// at the repository root, and tags the image shipyard/<app>:<short-sha>.
// When a Releaser is configured, a successful build is started and routed.
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
	"github.com/gabbygtobar-coder/shipyard/internal/metrics"
	"github.com/gabbygtobar-coder/shipyard/internal/runtime"
	"github.com/gabbygtobar-coder/shipyard/internal/source"
	"github.com/gabbygtobar-coder/shipyard/internal/store"
)

const (
	defaultConcurrency    = 2
	defaultQueueSize      = 16
	defaultBuildTimeout   = 10 * time.Minute
	defaultCloneTimeout   = 2 * time.Minute
	defaultReleaseHistory = 5

	msgCloneFailed       = "git clone failed"
	msgCloneTimedOut     = "git clone timed out"
	msgBuildFailed       = "docker build failed"
	msgBuildTimedOut     = "build timed out"
	msgCanceled          = "build canceled"
	msgDockerfileMissing = "no Dockerfile at repository root"
	msgDockerfileNotFile = "dockerfile must be a regular file at the repository root"
	msgInvalidSHA        = "invalid commit sha"
	msgInternal          = "internal error"
	msgContainerStart    = "container start failed"
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

// Releaser starts a built image and routes traffic to it.
// On success the deployment status is running.
// On failure the new container is stopped and the deployment is left starting.
type Releaser interface {
	Release(ctx context.Context, appName, deploymentID, image string, logs io.Writer) error
}

// ImageRemover deletes an image that is no longer retained.
// The local Docker runtime and a future registry runtime can both implement it.
type ImageRemover interface {
	RemoveImage(ctx context.Context, tag string) error
}

// Config configures a Runner.
// Zero concurrency, queue size, and timeouts are replaced with defaults.
// A nil Releaser keeps the M1 behavior: a built image is marked succeeded.
type Config struct {
	Store          store.Store
	Cloner         source.Cloner
	Builder        build.Builder
	Releaser       Releaser
	Concurrency    int
	QueueSize      int
	BuildTimeout   time.Duration
	CloneTimeout   time.Duration
	Logger         *slog.Logger
	Now            func() time.Time
	ReleaseHistory int
	Images         ImageRemover
	// Metrics receives build and deploy observations. Nil skips them.
	Metrics *metrics.Metrics
}

// Runner executes builds on a fixed set of workers.
type Runner struct {
	store        store.Store
	cloner       source.Cloner
	builder      build.Builder
	releaser     Releaser
	concurrency  int
	queueCap     int
	buildTimeout time.Duration
	cloneTimeout time.Duration
	log          *slog.Logger
	now          func() time.Time
	images       ImageRemover
	releaseKeep  int
	metrics      *metrics.Metrics

	mu      sync.Mutex
	jobs    chan string
	pending map[string]string
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
	if cfg.ReleaseHistory <= 0 {
		cfg.ReleaseHistory = defaultReleaseHistory
	}
	return &Runner{
		store:        cfg.Store,
		cloner:       cfg.Cloner,
		builder:      cfg.Builder,
		releaser:     cfg.Releaser,
		images:       cfg.Images,
		concurrency:  cfg.Concurrency,
		queueCap:     cfg.QueueSize,
		buildTimeout: cfg.BuildTimeout,
		cloneTimeout: cfg.CloneTimeout,
		releaseKeep:  cfg.ReleaseHistory,
		metrics:      cfg.Metrics,
		log:          cfg.Logger,
		now:          cfg.Now,
		pending:      make(map[string]string),
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

// StartPush queues a build of an exact commit, or updates the queued build for
// appName when one is already waiting. The second result is true when an
// existing queued build was updated instead of creating another one.
func (r *Runner) StartPush(ctx context.Context, appName, branch, commitSHA string) (store.Deployment, bool, error) {
	if err := ctx.Err(); err != nil {
		return store.Deployment{}, false, err
	}
	appName = strings.TrimSpace(appName)
	branch = strings.TrimSpace(branch)
	commitSHA = strings.ToLower(strings.TrimSpace(commitSHA))
	if appName == "" || branch == "" || giturl.ValidateRef(branch) != nil || !giturl.ValidSHA(commitSHA) {
		return store.Deployment{}, false, store.ErrInvalid
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.started || r.stopped {
		return store.Deployment{}, false, ErrStopped
	}
	if id, ok := r.pending[appName]; ok {
		dep, err := r.store.UpdateDeployment(ctx, id, store.DeploymentUpdate{Ref: &branch, CommitSHA: &commitSHA})
		if err != nil {
			return store.Deployment{}, false, err
		}
		return dep, true, nil
	}
	if len(r.jobs) >= cap(r.jobs) {
		return store.Deployment{}, false, ErrQueueFull
	}
	dep, err := r.store.CreateBuild(ctx, appName, branch)
	if err != nil {
		return store.Deployment{}, false, err
	}
	updated, err := r.store.UpdateDeployment(ctx, dep.ID, store.DeploymentUpdate{CommitSHA: &commitSHA})
	if err != nil {
		failed := store.DeploymentStatusFailed
		msg := msgInternal
		now := r.now().UTC()
		_, _ = r.store.UpdateDeployment(context.Background(), dep.ID, store.DeploymentUpdate{
			Status:     &failed,
			Error:      &msg,
			FinishedAt: &now,
		})
		return store.Deployment{}, false, err
	}
	r.pending[appName] = updated.ID
	r.jobs <- updated.ID
	return updated, false, nil
}

// Rollback queues a redeploy of a retained image. An empty deploymentID selects
// the release before the one that is running, or the newest release when
// nothing is running.
func (r *Runner) Rollback(ctx context.Context, appName, deploymentID string) (store.Deployment, error) {
	if err := ctx.Err(); err != nil {
		return store.Deployment{}, err
	}
	appName = strings.TrimSpace(appName)
	deploymentID = strings.TrimSpace(deploymentID)
	if appName == "" {
		return store.Deployment{}, store.ErrInvalid
	}
	if _, err := r.store.GetApp(ctx, appName); err != nil {
		return store.Deployment{}, err
	}
	releases, err := r.store.ListReleases(ctx, appName)
	if err != nil {
		return store.Deployment{}, err
	}
	deps, err := r.store.ListDeployments(ctx, appName)
	if err != nil {
		return store.Deployment{}, err
	}
	rel, err := SelectRelease(releases, currentRunning(deps), deploymentID)
	if err != nil {
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
	dep, err := r.store.CreateRollback(ctx, appName, rel)
	if err != nil {
		return store.Deployment{}, err
	}
	r.jobs <- dep.ID
	return dep, nil
}

func (r *Runner) worker() {
	defer r.wg.Done()
	for id := range r.jobs {
		r.clearPending(id)
		r.execute(id)
	}
}

func (r *Runner) clearPending(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for app, pending := range r.pending {
		if pending == id {
			delete(r.pending, app)
		}
	}
}

func (r *Runner) execute(id string) {
	var started time.Time
	var buildStatus string
	defer func() {
		if rec := recover(); rec != nil {
			r.log.Error("build panic", "deployment", id, "err", fmt.Sprint(rec))
			r.finish(id, store.DeploymentStatusFailed, "", "", msgInternal)
			if !started.IsZero() && buildStatus == "" {
				buildStatus = store.DeploymentStatusFailed
			}
		}
		if buildStatus != "" {
			r.observeBuild(started, buildStatus)
		}
	}()
	if r.ctx.Err() != nil {
		r.finish(id, store.DeploymentStatusFailed, "", "", msgCanceled)
		return
	}
	peek, peekErr := r.store.GetDeployment(context.Background(), id)
	if peekErr != nil {
		r.log.Error("load deployment", "deployment", id, "err", peekErr)
		r.finish(id, store.DeploymentStatusFailed, "", "", msgInternal)
		return
	}
	if peek.RollbackOf != "" {
		r.executeRollback(peek)
		return
	}

	started = r.now().UTC()
	building := store.DeploymentStatusBuilding
	if _, err := r.store.UpdateDeployment(context.Background(), id, store.DeploymentUpdate{
		Status:    &building,
		StartedAt: &started,
	}); err != nil {
		r.log.Error("mark building", "deployment", id, "err", err)
		buildStatus = store.DeploymentStatusFailed
		r.finish(id, store.DeploymentStatusFailed, "", "", msgInternal)
		return
	}

	dep, err := r.store.GetDeployment(context.Background(), id)
	if err != nil {
		r.log.Error("load deployment", "deployment", id, "err", err)
		buildStatus = store.DeploymentStatusFailed
		r.finish(id, store.DeploymentStatusFailed, "", "", msgInternal)
		return
	}
	app, err := r.store.GetApp(context.Background(), dep.AppName)
	if err != nil {
		buildStatus = store.DeploymentStatusFailed
		r.finish(id, store.DeploymentStatusFailed, "", "", msgInternal)
		return
	}

	jobCtx, jobCancel := context.WithTimeout(r.ctx, r.buildTimeout)
	defer jobCancel()

	dir, err := os.MkdirTemp("", "shipyard-build-")
	if err != nil {
		r.log.Error("temp dir", "deployment", id, "err", err)
		buildStatus = store.DeploymentStatusFailed
		r.finish(id, store.DeploymentStatusFailed, "", "", msgInternal)
		return
	}
	defer func() { _ = os.RemoveAll(dir) }()
	fail := func(sha, msg string) {
		_ = os.RemoveAll(dir)
		buildStatus = store.DeploymentStatusFailed
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
	cloneRef := dep.Ref
	if giturl.ValidSHA(dep.CommitSHA) {
		cloneRef = dep.CommitSHA
	}
	sha, cloneErr := r.cloner.Clone(cloneCtx, app.RepoURL, cloneRef, dir, &logs)
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
	buildStatus = store.DeploymentStatusSucceeded
	_ = os.RemoveAll(dir)
	if r.releaser == nil {
		r.finish(id, store.DeploymentStatusSucceeded, tag, sha, "")
		r.retain(id)
		return
	}
	starting := store.DeploymentStatusStarting
	if _, err := r.store.UpdateDeployment(context.Background(), id, store.DeploymentUpdate{
		Status:    &starting,
		ImageTag:  &tag,
		CommitSHA: &sha,
	}); err != nil {
		r.log.Error("mark starting", "deployment", id, "err", err)
		r.finish(id, store.DeploymentStatusFailed, tag, sha, msgInternal)
		return
	}
	_, _ = fmt.Fprintf(&logs, "starting %s\n", tag)
	if err := r.releaser.Release(r.ctx, app.Name, id, tag, &logs); err != nil {
		_, _ = fmt.Fprintf(&logs, "error: %s\n", err.Error())
		r.finish(id, store.DeploymentStatusFailed, tag, sha, releaseFailure(r.ctx, err))
		return
	}
	got, err := r.store.GetDeployment(context.Background(), id)
	if err != nil || got.Status != store.DeploymentStatusRunning {
		r.log.Error("release did not mark running", "deployment", id, "err", err)
		r.finish(id, store.DeploymentStatusFailed, tag, sha, msgInternal)
		return
	}
	r.retain(id)
}

func (r *Runner) executeRollback(dep store.Deployment) {
	id := dep.ID
	started := r.now().UTC()
	building := store.DeploymentStatusBuilding
	if _, err := r.store.UpdateDeployment(context.Background(), id, store.DeploymentUpdate{
		Status:    &building,
		StartedAt: &started,
	}); err != nil {
		r.log.Error("mark building", "deployment", id, "err", err)
		r.finish(id, store.DeploymentStatusFailed, dep.ImageTag, dep.CommitSHA, msgInternal)
		return
	}
	logs := logWriter{store: r.store, id: id}
	_, _ = fmt.Fprintf(&logs, "rolling back to %s from %s\n", dep.ImageTag, dep.RollbackOf)
	if err := build.ValidateTag(dep.ImageTag); err != nil {
		r.log.Error("rollback image", "deployment", id, "err", err)
		r.finish(id, store.DeploymentStatusFailed, dep.ImageTag, dep.CommitSHA, msgInternal)
		return
	}
	if r.releaser == nil {
		r.finish(id, store.DeploymentStatusSucceeded, dep.ImageTag, dep.CommitSHA, "")
		r.retain(id)
		return
	}
	starting := store.DeploymentStatusStarting
	image := dep.ImageTag
	sha := dep.CommitSHA
	if _, err := r.store.UpdateDeployment(context.Background(), id, store.DeploymentUpdate{
		Status:    &starting,
		ImageTag:  &image,
		CommitSHA: &sha,
	}); err != nil {
		r.log.Error("mark starting", "deployment", id, "err", err)
		r.finish(id, store.DeploymentStatusFailed, dep.ImageTag, dep.CommitSHA, msgInternal)
		return
	}
	_, _ = fmt.Fprintf(&logs, "starting %s\n", dep.ImageTag)
	if err := r.releaser.Release(r.ctx, dep.AppName, id, dep.ImageTag, &logs); err != nil {
		_, _ = fmt.Fprintf(&logs, "error: %s\n", err.Error())
		r.finish(id, store.DeploymentStatusFailed, dep.ImageTag, dep.CommitSHA, releaseFailure(r.ctx, err))
		return
	}
	got, err := r.store.GetDeployment(context.Background(), id)
	if err != nil || got.Status != store.DeploymentStatusRunning {
		r.log.Error("rollback did not mark running", "deployment", id, "err", err)
		r.finish(id, store.DeploymentStatusFailed, dep.ImageTag, dep.CommitSHA, msgInternal)
		return
	}
	r.retain(id)
}

func (r *Runner) retain(id string) {
	r.recordOutcome(id)
	dropped, err := r.store.RecordRelease(context.Background(), id, r.releaseKeep)
	if err != nil {
		r.log.Error("record release", "deployment", id, "err", err)
		return
	}
	if r.images == nil || len(dropped) == 0 {
		return
	}
	dep, err := r.store.GetDeployment(context.Background(), id)
	if err != nil {
		r.log.Error("load release", "deployment", id, "err", err)
		return
	}
	kept, err := r.store.ListReleases(context.Background(), dep.AppName)
	if err != nil {
		r.log.Error("list releases", "app", dep.AppName, "err", err)
		return
	}
	inUse := make(map[string]bool, len(kept))
	for _, rel := range kept {
		inUse[rel.ImageTag] = true
	}
	for _, rel := range dropped {
		if rel.ImageTag == "" || inUse[rel.ImageTag] {
			continue
		}
		if err := r.images.RemoveImage(context.Background(), rel.ImageTag); err != nil {
			r.log.Error("remove image", "image", rel.ImageTag, "err", err)
		}
	}
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

func releaseFailure(parent context.Context, err error) string {
	switch {
	case errors.Is(parent.Err(), context.Canceled), errors.Is(err, context.Canceled):
		return msgCanceled
	case errors.Is(err, runtime.ErrStartTimeout):
		return runtime.ErrStartTimeout.Error()
	case errors.Is(err, runtime.ErrHealthTimeout):
		return runtime.ErrHealthTimeout.Error()
	case errors.Is(err, runtime.ErrDockerNotFound):
		return runtime.ErrDockerNotFound.Error()
	case errors.Is(err, runtime.ErrUnhealthy):
		return runtime.ErrUnhealthy.Error()
	default:
		return msgContainerStart
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
	if _, err := r.store.UpdateDeployment(context.Background(), id, upd); err != nil {
		if !errors.Is(err, store.ErrInvalid) && !errors.Is(err, store.ErrNotFound) {
			r.log.Error("finish deployment", "deployment", id, "err", err)
		}
		return
	}
	r.metrics.ObserveDeploy(status)
}

func (r *Runner) observeBuild(started time.Time, status string) {
	d := time.Duration(0)
	if !started.IsZero() {
		d = r.now().Sub(started)
	}
	r.metrics.ObserveBuild(status, d)
}

func (r *Runner) recordOutcome(id string) {
	dep, err := r.store.GetDeployment(context.Background(), id)
	if err != nil {
		r.log.Error("record event", "deployment", id, "err", err)
		return
	}
	kind := store.EventDeployed
	msg := "deployment " + dep.ID + " is " + dep.Status
	if dep.RollbackOf != "" {
		kind = store.EventRolledBack
		msg = "rolled back " + dep.RollbackOf + " as " + dep.ID
	}
	if dep.Status == store.DeploymentStatusRunning {
		r.metrics.ObserveDeploy(dep.Status)
	}
	if _, err := r.store.AppendEvent(context.Background(), store.Event{
		AppName:      dep.AppName,
		Type:         kind,
		Message:      msg,
		DeploymentID: dep.ID,
	}); err != nil {
		r.log.Error("record event", "deployment", id, "err", err)
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
