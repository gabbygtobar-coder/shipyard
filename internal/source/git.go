package source

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gabbygtobar-coder/shipyard/internal/giturl"
	"github.com/gabbygtobar-coder/shipyard/internal/proc"
)

const (
	gitBinary       = "git"
	defaultFetchRef = "HEAD"
)

// commandRunner executes a binary. Tests substitute a script.
type commandRunner interface {
	Run(ctx context.Context, name string, args []string, spec proc.Spec) error
	Output(ctx context.Context, name string, args []string, spec proc.Spec) ([]byte, error)
}

type osCommand struct{}

func (osCommand) Run(ctx context.Context, name string, args []string, spec proc.Spec) error {
	return proc.Run(ctx, name, args, spec)
}

func (osCommand) Output(ctx context.Context, name string, args []string, spec proc.Spec) ([]byte, error) {
	return proc.Output(ctx, name, args, spec)
}

// Git shallow-clones https://github.com repositories with the git binary.
// Each user-controlled value is one exec argument. The environment passed to
// git drops GIT_* variables from the server process.
type Git struct {
	maxBytes int64
	run      commandRunner
	look     func(string) error
}

// NewGit returns a cloner that refuses checkouts larger than maxBytes.
// A non-positive maxBytes uses DefaultMaxCloneBytes.
func NewGit(maxBytes int64) *Git {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxCloneBytes
	}
	return &Git{
		maxBytes: maxBytes,
		run:      osCommand{},
		look: func(name string) error {
			_, err := exec.LookPath(name)
			return err
		},
	}
}

// Clone implements Cloner.
func (g *Git) Clone(ctx context.Context, repoURL, ref, dest string, logs io.Writer) (string, error) {
	normalized, err := giturl.NormalizeRepoURL(repoURL)
	if err != nil {
		return "", err
	}
	if err := giturl.ValidateRef(ref); err != nil {
		return "", err
	}
	if err := validateDest(dest); err != nil {
		return "", err
	}
	if logs == nil {
		logs = io.Discard
	}
	if err := g.look(gitBinary); err != nil {
		return "", ErrGitNotFound
	}

	cloneCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var tooBig atomic.Bool
	go watchSize(cloneCtx, dest, g.maxBytes, func() {
		tooBig.Store(true)
		cancel()
	})

	spec := proc.Spec{Env: gitEnviron(), Stdout: logs, Stderr: logs}
	fetchRef := defaultFetchRef
	if ref != "" {
		fetchRef = ref
	}
	steps := [][]string{
		gitGlobal("init", "--quiet", dest),
		gitDashC(dest, "remote", "add", "origin", normalized),
		gitDashC(dest, "fetch", "--depth", "1", "--no-tags", "origin", fetchRef),
		gitDashC(dest, "checkout", "--detach", "--quiet", "FETCH_HEAD"),
	}
	for _, args := range steps {
		if err := g.run.Run(cloneCtx, gitBinary, args, spec); err != nil {
			return "", classifyGitError(ctx, tooBig.Load(), err)
		}
	}
	out, err := g.run.Output(cloneCtx, gitBinary, gitDashC(dest, "rev-parse", defaultFetchRef), spec)
	if err != nil {
		return "", classifyGitError(ctx, tooBig.Load(), err)
	}
	over, sizeErr := dirBytes(dest)
	if sizeErr != nil {
		return "", sizeErr
	}
	if tooBig.Load() || over > g.maxBytes {
		return "", ErrCloneTooLarge
	}
	sha := strings.ToLower(strings.TrimSpace(string(out)))
	if !giturl.ValidSHA(sha) {
		return "", ErrInvalidCommit
	}
	return sha, nil
}

func classifyGitError(parent context.Context, tooBig bool, err error) error {
	if tooBig {
		return ErrCloneTooLarge
	}
	if parent.Err() != nil {
		return parent.Err()
	}
	if err == nil {
		return nil
	}
	return err
}

func validateDest(dest string) error {
	if dest == "" || !filepath.IsAbs(dest) || strings.ContainsAny(dest, "\x00\n\r") {
		return ErrInvalidDest
	}
	return nil
}

func gitConfigArgs() []string {
	return []string{
		"-c", "credential.helper=",
		"-c", "protocol.file.allow=never",
		"-c", "protocol.ext.allow=never",
	}
}

func gitGlobal(args ...string) []string {
	out := gitConfigArgs()
	return append(out, args...)
}

func gitDashC(dest string, args ...string) []string {
	out := append([]string{"-C", dest}, gitConfigArgs()...)
	return append(out, args...)
}

func gitEnviron() []string {
	env := make([]string, 0, len(os.Environ())+6)
	for _, kv := range os.Environ() {
		key, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(key, "GIT_") {
			continue
		}
		env = append(env, kv)
	}
	return append(env,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_CONFIG_COUNT=0",
		"GCM_INTERACTIVE=never",
	)
}

func watchSize(ctx context.Context, dir string, max int64, onLimit func()) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		over, err := dirBytes(dir)
		if err == nil && over > max {
			onLimit()
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func dirBytes(root string) (int64, error) {
	info, err := os.Lstat(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	if !info.IsDir() {
		if info.Mode().IsRegular() {
			return info.Size(), nil
		}
		return 0, nil
	}
	var total int64
	err = filepath.WalkDir(root, func(_ string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, os.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		if !d.Type().IsRegular() {
			return nil
		}
		fi, infoErr := d.Info()
		if infoErr != nil {
			if errors.Is(infoErr, os.ErrNotExist) {
				return nil
			}
			return infoErr
		}
		total += fi.Size()
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return total, nil
	}
	return total, err
}

var _ Cloner = (*Git)(nil)
