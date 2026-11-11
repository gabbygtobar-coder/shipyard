package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gabbygtobar-coder/shipyard/internal/giturl"
	_ "modernc.org/sqlite" // pure-Go SQLite driver.
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// SQLite is a durable Store. Open it with Open.
type SQLite struct {
	db  *sql.DB
	now func() time.Time
	mu  sync.Mutex
}

// Open opens a SQLite database at path, creating it and applying migrations.
func Open(path string) (*SQLite, error) {
	return OpenWithClock(path, time.Now)
}

// OpenWithClock is Open with an injected clock. A nil clock uses time.Now.
func OpenWithClock(path string, now func() time.Time) (*SQLite, error) {
	if now == nil {
		now = time.Now
	}
	path = strings.TrimSpace(path)
	if path == "" || strings.ContainsAny(path, "\x00?#") {
		return nil, fmt.Errorf("database path is invalid")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err := mkdirParent(abs); err != nil {
		return nil, err
	}
	dsn := "file:" + filepath.ToSlash(abs) + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec(`PRAGMA busy_timeout = 5000`); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := enableWAL(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := applyMigrations(db, now); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &SQLite{db: db, now: now}, nil
}

// Close closes the database.
func (s *SQLite) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil
	}
	_, _ = s.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	err := s.db.Close()
	s.db = nil
	return err
}

// CreateApp implements Store.
func (s *SQLite) CreateApp(ctx context.Context, name, repoURL string) (App, error) {
	if err := ctx.Err(); err != nil {
		return App{}, err
	}
	name = strings.TrimSpace(name)
	repoURL = strings.TrimSpace(repoURL)
	if name == "" || repoURL == "" {
		return App{}, ErrInvalid
	}
	app := App{
		Name:      name,
		RepoURL:   repoURL,
		Branch:    DefaultBranch,
		Status:    AppStatusIdle,
		CreatedAt: s.now().UTC(),
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := s.appByName(ctx, name); err == nil {
		return App{}, ErrAlreadyExists
	} else if !errors.Is(err, ErrNotFound) {
		return App{}, err
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO apps(name, repo_url, branch, auto_deploy, webhook_secret, status, url, created_at)
		VALUES (?, ?, ?, 0, '', ?, '', ?)`,
		app.Name, app.RepoURL, app.Branch, app.Status, formatTime(app.CreatedAt))
	if err != nil {
		return App{}, err
	}
	return app, nil
}

// GetApp implements Store.
func (s *SQLite) GetApp(ctx context.Context, name string) (App, error) {
	if err := ctx.Err(); err != nil {
		return App{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return App{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.appByName(ctx, name)
}

// ListApps implements Store.
func (s *SQLite) ListApps(ctx context.Context) ([]App, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.QueryContext(ctx, `SELECT `+appColumns+` FROM apps ORDER BY rowid`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanApps(rows)
}

// SetAppStatus implements Store.
func (s *SQLite) SetAppStatus(ctx context.Context, name, status, appURL string) (App, error) {
	if err := ctx.Err(); err != nil {
		return App{}, err
	}
	name = strings.TrimSpace(name)
	status = strings.TrimSpace(status)
	appURL = strings.TrimSpace(appURL)
	if name == "" || !validAppStatus(status, appURL) {
		return App{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.appByName(ctx, name); err != nil {
		return App{}, err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE apps SET status = ?, url = ? WHERE name = ?`, status, appURL, name); err != nil {
		return App{}, err
	}
	return s.appByName(ctx, name)
}

// ConfigureApp implements Store.
func (s *SQLite) ConfigureApp(ctx context.Context, name string, settings AppSettings) (App, error) {
	if err := ctx.Err(); err != nil {
		return App{}, err
	}
	name = strings.TrimSpace(name)
	settings, err := normalizeSettings(settings)
	if err != nil || name == "" {
		return App{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.appByName(ctx, name); err != nil {
		return App{}, err
	}
	auto := 0
	if settings.AutoDeploy {
		auto = 1
	}
	if _, err := s.db.ExecContext(ctx, `
		UPDATE apps SET branch = ?, auto_deploy = ?, webhook_secret = ? WHERE name = ?`,
		settings.Branch, auto, settings.WebhookSecret, name); err != nil {
		return App{}, err
	}
	return s.appByName(ctx, name)
}

// ListAppsByRepo implements Store.
func (s *SQLite) ListAppsByRepo(ctx context.Context, repoURL string) ([]App, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	canonical, err := giturl.NormalizeRepoURL(repoURL)
	if err != nil {
		return nil, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.QueryContext(ctx, `SELECT `+appColumns+` FROM apps WHERE repo_url = ? ORDER BY rowid`, canonical)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanApps(rows)
}

// CreateDeployment implements Store.
func (s *SQLite) CreateDeployment(ctx context.Context, appName, status string) (Deployment, error) {
	if err := ctx.Err(); err != nil {
		return Deployment{}, err
	}
	appName = strings.TrimSpace(appName)
	status = strings.TrimSpace(status)
	if appName == "" || !validDeploymentStatus(status) {
		return Deployment{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.insertDeployment(ctx, appName, status, "", "", "", "")
}

// CreateBuild implements Store.
func (s *SQLite) CreateBuild(ctx context.Context, appName, ref string) (Deployment, error) {
	if err := ctx.Err(); err != nil {
		return Deployment{}, err
	}
	appName = strings.TrimSpace(appName)
	ref = strings.TrimSpace(ref)
	if appName == "" || giturl.ValidateRef(ref) != nil {
		return Deployment{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.insertDeployment(ctx, appName, DeploymentStatusQueued, ref, "", "", "")
}

// UpdateDeployment implements Store.
func (s *SQLite) UpdateDeployment(ctx context.Context, id string, upd DeploymentUpdate) (Deployment, error) {
	if err := ctx.Err(); err != nil {
		return Deployment{}, err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return Deployment{}, ErrInvalid
	}
	upd, err := prepareDeploymentUpdate(upd)
	if err != nil {
		return Deployment{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dep, err := s.deploymentByID(ctx, id)
	if err != nil {
		return Deployment{}, err
	}
	next, err := applyDeploymentUpdate(dep, upd)
	if err != nil {
		return Deployment{}, err
	}
	if err := s.saveDeployment(ctx, next); err != nil {
		return Deployment{}, err
	}
	return cloneDeployment(next), nil
}

// GetDeployment implements Store.
func (s *SQLite) GetDeployment(ctx context.Context, id string) (Deployment, error) {
	if err := ctx.Err(); err != nil {
		return Deployment{}, err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return Deployment{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dep, err := s.deploymentByID(ctx, id)
	if err != nil {
		return Deployment{}, err
	}
	return cloneDeployment(dep), nil
}

// ListDeployments implements Store.
func (s *SQLite) ListDeployments(ctx context.Context, appName string) ([]Deployment, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	appName = strings.TrimSpace(appName)
	if appName == "" {
		return nil, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.appByName(ctx, appName); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+deploymentColumns+` FROM deployments WHERE app_name = ? ORDER BY rowid`, appName)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanDeployments(rows)
}

// AppendDeploymentLog implements Store.
func (s *SQLite) AppendDeploymentLog(ctx context.Context, id string, p []byte) error {
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
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.deploymentByID(ctx, id); err != nil {
		return err
	}
	data, truncated, err := s.readLog(ctx, id)
	if err != nil {
		return err
	}
	data, truncated = appendCappedLog(data, truncated, p)
	trunc := 0
	if truncated {
		trunc = 1
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO deployment_logs(deployment_id, data, truncated) VALUES (?, ?, ?)
		ON CONFLICT(deployment_id) DO UPDATE SET data = excluded.data, truncated = excluded.truncated`,
		id, data, trunc)
	return err
}

// ReadDeploymentLog implements Store.
func (s *SQLite) ReadDeploymentLog(ctx context.Context, id string, offset int) (LogRead, error) {
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
	s.mu.Lock()
	defer s.mu.Unlock()
	dep, err := s.deploymentByID(ctx, id)
	if err != nil {
		return LogRead{}, err
	}
	raw, _, err := s.readLog(ctx, id)
	if err != nil {
		return LogRead{}, err
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
func (s *SQLite) WaitDeploymentLog(ctx context.Context, id string, offset int) error {
	ticker := time.NewTicker(logPollInterval)
	defer ticker.Stop()
	for {
		snap, err := s.ReadDeploymentLog(ctx, id, offset)
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

// ClaimDelivery implements Store.
func (s *SQLite) ClaimDelivery(ctx context.Context, id string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	id = strings.TrimSpace(id)
	if !validDeliveryID(id) {
		return false, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM deliveries WHERE id = ?`, id).Scan(&n); err != nil {
		return false, err
	}
	if n > 0 {
		return false, nil
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO deliveries(id, received_at) VALUES (?, ?)`, id, formatTime(s.now().UTC()))
	if err != nil {
		return false, err
	}
	return true, nil
}

// ForgetDelivery implements Store.
func (s *SQLite) ForgetDelivery(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	id = strings.TrimSpace(id)
	if !validDeliveryID(id) {
		return ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `DELETE FROM deliveries WHERE id = ?`, id)
	return err
}

// CreateRollback implements Store.
func (s *SQLite) CreateRollback(ctx context.Context, appName string, source Release) (Deployment, error) {
	if err := ctx.Err(); err != nil {
		return Deployment{}, err
	}
	appName = strings.TrimSpace(appName)
	source, err := normalizeRelease(appName, source)
	if err != nil {
		return Deployment{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.insertDeployment(ctx, appName, DeploymentStatusQueued, source.Ref, source.CommitSHA, source.ImageTag, source.DeploymentID)
}

// RecordRelease implements Store.
func (s *SQLite) RecordRelease(ctx context.Context, deploymentID string, keep int) ([]Release, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	deploymentID = strings.TrimSpace(deploymentID)
	if deploymentID == "" || keep < 1 {
		return nil, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dep, err := s.deploymentByID(ctx, deploymentID)
	if err != nil {
		return nil, err
	}
	if !releasable(dep) {
		return nil, ErrInvalid
	}
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM releases WHERE app_name = ? AND deployment_id = ?`, dep.AppName, dep.ID).Scan(&n); err != nil {
		return nil, err
	}
	if n > 0 {
		return nil, nil
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO releases(app_name, deployment_id, ref, commit_sha, image_tag, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		dep.AppName, dep.ID, dep.Ref, dep.CommitSHA, dep.ImageTag, formatTime(s.now().UTC()))
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT rowid, `+releaseColumns+` FROM releases WHERE app_name = ? ORDER BY rowid`, dep.AppName)
	if err != nil {
		return nil, err
	}
	type numbered struct {
		rowid int64
		rel   Release
	}
	var all []numbered
	scanErr := func() error {
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var item numbered
			item.rel, item.rowid, err = scanReleaseRow(rows)
			if err != nil {
				return err
			}
			all = append(all, item)
		}
		return rows.Err()
	}()
	if scanErr != nil {
		return nil, scanErr
	}
	if len(all) <= keep {
		return nil, nil
	}
	drop := all[:len(all)-keep]
	dropped := make([]Release, 0, len(drop))
	for _, item := range drop {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM releases WHERE rowid = ?`, item.rowid); err != nil {
			return nil, err
		}
		dropped = append(dropped, item.rel)
	}
	return dropped, nil
}

// ListReleases implements Store.
func (s *SQLite) ListReleases(ctx context.Context, appName string) ([]Release, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	appName = strings.TrimSpace(appName)
	if appName == "" {
		return nil, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.appByName(ctx, appName); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+releaseColumns+` FROM releases WHERE app_name = ? ORDER BY rowid DESC`, appName)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]Release, 0)
	for rows.Next() {
		rel, err := scanRelease(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rel)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// Ping implements Store.
func (s *SQLite) Ping(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return errors.New("database is closed")
	}
	return s.db.PingContext(ctx)
}

// AppendEvent implements Store.
func (s *SQLite) AppendEvent(ctx context.Context, ev Event) (Event, error) {
	if err := ctx.Err(); err != nil {
		return Event{}, err
	}
	ev, err := normalizeEvent(ev)
	if err != nil {
		return Event{}, err
	}
	ev.CreatedAt = s.now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.appByName(ctx, ev.AppName); err != nil {
		return Event{}, err
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO events(app_name, type, message, deployment_id, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		ev.AppName, ev.Type, ev.Message, ev.DeploymentID, formatTime(ev.CreatedAt))
	if err != nil {
		return Event{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Event{}, err
	}
	ev.ID = id
	return ev, nil
}

// ListEvents implements Store.
func (s *SQLite) ListEvents(ctx context.Context, appName string, limit int) ([]Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	appName = strings.TrimSpace(appName)
	if appName == "" {
		return nil, ErrInvalid
	}
	limit = clampEventLimit(limit)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.appByName(ctx, appName); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, app_name, type, message, deployment_id, created_at
		FROM events WHERE app_name = ? ORDER BY id DESC LIMIT ?`, appName, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]Event, 0)
	for rows.Next() {
		ev, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// CountActiveDeployments implements Store.
func (s *SQLite) CountActiveDeployments(ctx context.Context) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM deployments
		WHERE status IN (?, ?, ?, ?)`,
		DeploymentStatusQueued, DeploymentStatusBuilding, DeploymentStatusStarting, DeploymentStatusRunning).Scan(&n)
	return n, err
}

func scanEvent(row scanner) (Event, error) {
	var ev Event
	var created string
	if err := row.Scan(&ev.ID, &ev.AppName, &ev.Type, &ev.Message, &ev.DeploymentID, &created); err != nil {
		return Event{}, err
	}
	t, err := parseTime(created)
	if err != nil {
		return Event{}, err
	}
	ev.CreatedAt = t
	return ev, nil
}

func (s *SQLite) insertDeployment(ctx context.Context, appName, status, ref, sha, image, rollbackOf string) (Deployment, error) {
	if _, err := s.appByName(ctx, appName); err != nil {
		return Deployment{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Deployment{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var n int
	err = tx.QueryRowContext(ctx, `SELECT value FROM counters WHERE name = 'deployment'`).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		n = 0
	} else if err != nil {
		return Deployment{}, err
	}
	n++
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO counters(name, value) VALUES ('deployment', ?)
		ON CONFLICT(name) DO UPDATE SET value = excluded.value`, n); err != nil {
		return Deployment{}, err
	}
	dep := Deployment{
		ID:         "dep_" + strconv.Itoa(n),
		AppName:    appName,
		Ref:        ref,
		CommitSHA:  sha,
		Status:     status,
		ImageTag:   image,
		RollbackOf: rollbackOf,
		CreatedAt:  s.now().UTC(),
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO deployments(
			id, app_name, ref, commit_sha, status, image_tag, url, error, rollback_of, created_at
		) VALUES (?, ?, ?, ?, ?, ?, '', '', ?, ?)`,
		dep.ID, dep.AppName, dep.Ref, dep.CommitSHA, dep.Status, dep.ImageTag, dep.RollbackOf, formatTime(dep.CreatedAt)); err != nil {
		return Deployment{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO deployment_logs(deployment_id, data, truncated) VALUES (?, ?, 0)`, dep.ID, []byte{}); err != nil {
		return Deployment{}, err
	}
	if err := tx.Commit(); err != nil {
		return Deployment{}, err
	}
	return cloneDeployment(dep), nil
}

func (s *SQLite) saveDeployment(ctx context.Context, dep Deployment) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE deployments
		SET ref = ?, commit_sha = ?, status = ?, image_tag = ?, url = ?, error = ?, started_at = ?, finished_at = ?
		WHERE id = ?`,
		dep.Ref, dep.CommitSHA, dep.Status, dep.ImageTag, dep.URL, dep.Error, nullTime(dep.StartedAt), nullTime(dep.FinishedAt), dep.ID)
	return err
}

func (s *SQLite) appByName(ctx context.Context, name string) (App, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+appColumns+` FROM apps WHERE name = ?`, name)
	app, err := scanApp(row)
	if errors.Is(err, sql.ErrNoRows) {
		return App{}, ErrNotFound
	}
	return app, err
}

func (s *SQLite) deploymentByID(ctx context.Context, id string) (Deployment, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+deploymentColumns+` FROM deployments WHERE id = ?`, id)
	dep, err := scanDeployment(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Deployment{}, ErrNotFound
	}
	return dep, err
}

func (s *SQLite) readLog(ctx context.Context, id string) ([]byte, bool, error) {
	var data []byte
	var truncated int
	err := s.db.QueryRowContext(ctx, `SELECT data, truncated FROM deployment_logs WHERE deployment_id = ?`, id).Scan(&data, &truncated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return data, truncated != 0, nil
}

const appColumns = `name, repo_url, branch, auto_deploy, webhook_secret, status, url, created_at`

const deploymentColumns = `id, app_name, ref, commit_sha, status, image_tag, url, error, rollback_of, created_at, started_at, finished_at`

const releaseColumns = `app_name, deployment_id, ref, commit_sha, image_tag, created_at`

type scanner interface {
	Scan(dest ...any) error
}

func scanApps(rows *sql.Rows) ([]App, error) {
	apps := make([]App, 0)
	for rows.Next() {
		app, err := scanApp(rows)
		if err != nil {
			return nil, err
		}
		apps = append(apps, app)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return apps, nil
}

func scanApp(row scanner) (App, error) {
	var app App
	var auto int
	var created string
	if err := row.Scan(&app.Name, &app.RepoURL, &app.Branch, &auto, &app.WebhookSecret, &app.Status, &app.URL, &created); err != nil {
		return App{}, err
	}
	app.AutoDeploy = auto != 0
	stamp, err := parseTime(created)
	if err != nil {
		return App{}, err
	}
	app.CreatedAt = stamp
	return app, nil
}

func scanDeployments(rows *sql.Rows) ([]Deployment, error) {
	out := make([]Deployment, 0)
	for rows.Next() {
		dep, err := scanDeployment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, dep)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func scanDeployment(row scanner) (Deployment, error) {
	var dep Deployment
	var created string
	var started, finished sql.NullString
	if err := row.Scan(
		&dep.ID, &dep.AppName, &dep.Ref, &dep.CommitSHA, &dep.Status, &dep.ImageTag, &dep.URL, &dep.Error, &dep.RollbackOf,
		&created, &started, &finished,
	); err != nil {
		return Deployment{}, err
	}
	stamp, err := parseTime(created)
	if err != nil {
		return Deployment{}, err
	}
	dep.CreatedAt = stamp
	if started.Valid {
		t, err := parseTime(started.String)
		if err != nil {
			return Deployment{}, err
		}
		dep.StartedAt = &t
	}
	if finished.Valid {
		t, err := parseTime(finished.String)
		if err != nil {
			return Deployment{}, err
		}
		dep.FinishedAt = &t
	}
	return cloneDeployment(dep), nil
}

func scanRelease(row scanner) (Release, error) {
	rel, _, err := scanReleaseMaybeRow(row, false)
	return rel, err
}

func scanReleaseRow(row scanner) (Release, int64, error) {
	return scanReleaseMaybeRow(row, true)
}

func scanReleaseMaybeRow(row scanner, withRowID bool) (Release, int64, error) {
	var rel Release
	var created string
	var rowid int64
	var err error
	if withRowID {
		err = row.Scan(&rowid, &rel.AppName, &rel.DeploymentID, &rel.Ref, &rel.CommitSHA, &rel.ImageTag, &created)
	} else {
		err = row.Scan(&rel.AppName, &rel.DeploymentID, &rel.Ref, &rel.CommitSHA, &rel.ImageTag, &created)
	}
	if err != nil {
		return Release{}, 0, err
	}
	rel.CreatedAt, err = parseTime(created)
	if err != nil {
		return Release{}, 0, err
	}
	return rel, rowid, nil
}

func appendCappedLog(buf []byte, truncated bool, p []byte) ([]byte, bool) {
	if len(p) == 0 || len(buf) >= MaxDeploymentLogBytes {
		return buf, truncated
	}
	remain := MaxDeploymentLogBytes - len(buf)
	if len(p) <= remain {
		return append(buf, p...), truncated
	}
	buf = append(buf, p[:remain]...)
	if !truncated {
		buf = append(buf, []byte("\n[log truncated]\n")...)
		truncated = true
	}
	return buf, truncated
}

func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

func parseTime(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, fmt.Errorf("empty timestamp")
	}
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, err
	}
	return t.UTC(), nil
}

func nullTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return formatTime(t.UTC())
}

func mkdirParent(path string) error {
	parent := filepath.Dir(path)
	if parent == "" || parent == "." {
		return nil
	}
	return os.MkdirAll(parent, 0o755)
}

func enableWAL(db *sql.DB) error {
	var mode string
	if err := db.QueryRow(`PRAGMA journal_mode = WAL`).Scan(&mode); err != nil {
		return err
	}
	if !strings.EqualFold(mode, "wal") {
		return fmt.Errorf("journal mode %q", mode)
	}
	return nil
}

func applyMigrations(db *sql.DB, now func() time.Time) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return err
	}
	var current int
	if err := db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
		return err
	}
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		version, ok := migrationVersion(name)
		if !ok {
			return fmt.Errorf("migration %s has no version", name)
		}
		if version <= current {
			continue
		}
		script, err := migrationFiles.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		for _, stmt := range splitSQL(string(script)) {
			if _, err := tx.Exec(stmt); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("migration %s: %w", name, err)
			}
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)`, version, formatTime(now().UTC())); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		current = version
	}
	return nil
}

func migrationVersion(name string) (int, bool) {
	i := 0
	for i < len(name) && name[i] >= '0' && name[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0, false
	}
	n, err := strconv.Atoi(name[:i])
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}

func splitSQL(script string) []string {
	var stmts []string
	var b strings.Builder
	for _, line := range strings.Split(script, "\n") {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "--") {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
		if strings.HasSuffix(trim, ";") {
			stmt := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(b.String()), ";"))
			if stmt != "" {
				stmts = append(stmts, stmt)
			}
			b.Reset()
		}
	}
	rest := strings.TrimSpace(b.String())
	if rest != "" {
		stmts = append(stmts, strings.TrimSuffix(rest, ";"))
	}
	return stmts
}

var _ Store = (*SQLite)(nil)
