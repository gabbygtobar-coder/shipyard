package source

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gabbygtobar-coder/shipyard/internal/proc"
)

const testSHA = "0123456789abcdef0123456789abcdef01234567"

type recorded struct {
	name string
	args []string
	env  []string
}

type script struct {
	mu     sync.Mutex
	cmds   []recorded
	handle func(ctx context.Context, name string, args []string, spec proc.Spec) ([]byte, error)
}

func (s *script) Run(ctx context.Context, name string, args []string, spec proc.Spec) error {
	_, err := s.exec(ctx, name, args, spec)
	return err
}

func (s *script) Output(ctx context.Context, name string, args []string, spec proc.Spec) ([]byte, error) {
	return s.exec(ctx, name, args, spec)
}

func (s *script) exec(ctx context.Context, name string, args []string, spec proc.Spec) ([]byte, error) {
	s.mu.Lock()
	s.cmds = append(s.cmds, recorded{name: name, args: append([]string(nil), args...), env: append([]string(nil), spec.Env...)})
	handle := s.handle
	s.mu.Unlock()
	if handle != nil {
		return handle(ctx, name, args, spec)
	}
	if len(args) > 0 && args[len(args)-1] == defaultFetchRef && containsArg(args, "rev-parse") {
		return []byte(testSHA + "\n"), nil
	}
	return nil, nil
}

func (s *script) commands() []recorded {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]recorded, len(s.cmds))
	copy(out, s.cmds)
	return out
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func testGit(s *script) *Git {
	g := NewGit(1 << 20)
	g.run = s
	g.look = func(string) error { return nil }
	return g
}

func TestGitCloneRejectsNonGitHubBeforeExec(t *testing.T) {
	t.Parallel()
	s := &script{}
	g := testGit(s)
	_, err := g.Clone(context.Background(), "https://gitlab.com/acme/demo", "main", t.TempDir(), ioDiscard())
	if !errors.Is(err, errRepoInvalid()) {
		t.Fatalf("error = %v", err)
	}
	if len(s.commands()) != 0 {
		t.Fatalf("git was invoked: %#v", s.commands())
	}
}

func TestGitCloneRejectsOptionRef(t *testing.T) {
	t.Parallel()
	s := &script{}
	g := testGit(s)
	_, err := g.Clone(context.Background(), "https://github.com/acme/demo", "--upload-pack=evil", t.TempDir(), ioDiscard())
	if err == nil {
		t.Fatal("expected ref to be rejected")
	}
	if len(s.commands()) != 0 {
		t.Fatalf("git was invoked: %#v", s.commands())
	}
}

func TestGitCloneArgsAreNotAShell(t *testing.T) {
	t.Parallel()
	s := &script{}
	g := testGit(s)
	dest := t.TempDir()
	sha, err := g.Clone(context.Background(), "https://github.com/acme/demo.git", "feature/add-logs", dest, ioDiscard())
	if err != nil {
		t.Fatal(err)
	}
	if sha != testSHA {
		t.Fatalf("sha = %s", sha)
	}
	cmds := s.commands()
	if len(cmds) != 5 {
		t.Fatalf("commands = %d, want 5", len(cmds))
	}
	for _, cmd := range cmds {
		if cmd.name != "git" {
			t.Fatalf("command %q, want git", cmd.name)
		}
		joined := strings.Join(cmd.args, " ")
		if strings.Contains(joined, "sh -c") || strings.Contains(cmd.name, "sh") {
			t.Fatalf("shell invocation: %#v", cmd)
		}
	}
	fetch := cmds[2].args
	if fetch[len(fetch)-2] != "origin" || fetch[len(fetch)-1] != "feature/add-logs" {
		t.Fatalf("fetch args = %#v", fetch)
	}
	if !containsArg(fetch, "--depth") || !containsArg(fetch, "1") {
		t.Fatalf("fetch is not shallow: %#v", fetch)
	}
	remote := cmds[1].args
	if remote[len(remote)-1] != "https://github.com/acme/demo" {
		t.Fatalf("remote url = %#v", remote)
	}
	for _, kv := range cmds[0].env {
		if strings.HasPrefix(kv, "GIT_SSH_COMMAND=") || strings.HasPrefix(kv, "GIT_EXEC_PATH=") {
			t.Fatalf("dangerous git env kept: %s", kv)
		}
	}
	foundPrompt := false
	for _, kv := range cmds[0].env {
		if kv == "GIT_TERMINAL_PROMPT=0" {
			foundPrompt = true
		}
	}
	if !foundPrompt {
		t.Fatal("GIT_TERMINAL_PROMPT was not set")
	}
}

func TestGitCloneDefaultBranchFetchesHEAD(t *testing.T) {
	t.Parallel()
	s := &script{}
	g := testGit(s)
	if _, err := g.Clone(context.Background(), "https://github.com/acme/demo", "", t.TempDir(), ioDiscard()); err != nil {
		t.Fatal(err)
	}
	fetch := s.commands()[2].args
	if fetch[len(fetch)-1] != defaultFetchRef {
		t.Fatalf("fetch ref = %#v", fetch)
	}
}

func TestGitCloneContextCancel(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	s := &script{handle: func(ctx context.Context, _ string, args []string, _ proc.Spec) ([]byte, error) {
		if containsArg(args, "init") {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return nil, nil
	}}
	g := testGit(s)
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, err := g.Clone(ctx, "https://github.com/acme/demo", "main", t.TempDir(), ioDiscard())
		errCh <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("clone did not start")
	}
	cancel()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("clone did not return")
	}
}

func TestGitCloneSizeCap(t *testing.T) {
	t.Parallel()
	dest := t.TempDir()
	s := &script{handle: func(ctx context.Context, _ string, args []string, _ proc.Spec) ([]byte, error) {
		if containsArg(args, "fetch") {
			if err := os.WriteFile(filepath.Join(dest, "blob"), bytes.Repeat([]byte("x"), 64), 0o600); err != nil {
				return nil, err
			}
			<-ctx.Done()
			return nil, ctx.Err()
		}
		if containsArg(args, "rev-parse") {
			return []byte(testSHA + "\n"), nil
		}
		return nil, nil
	}}
	g := testGit(s)
	g.maxBytes = 16
	_, err := g.Clone(context.Background(), "https://github.com/acme/demo", "main", dest, ioDiscard())
	if !errors.Is(err, ErrCloneTooLarge) {
		t.Fatalf("error = %v, want ErrCloneTooLarge", err)
	}
}

func TestGitCloneRejectsBadSHA(t *testing.T) {
	t.Parallel()
	s := &script{handle: func(_ context.Context, _ string, args []string, _ proc.Spec) ([]byte, error) {
		if containsArg(args, "rev-parse") {
			return []byte("not-a-sha\n"), nil
		}
		return nil, nil
	}}
	g := testGit(s)
	_, err := g.Clone(context.Background(), "https://github.com/acme/demo", "main", t.TempDir(), ioDiscard())
	if !errors.Is(err, ErrInvalidCommit) {
		t.Fatalf("error = %v", err)
	}
}

func TestGitCloneRelativeDest(t *testing.T) {
	t.Parallel()
	g := testGit(&script{})
	_, err := g.Clone(context.Background(), "https://github.com/acme/demo", "main", "relative", ioDiscard())
	if !errors.Is(err, ErrInvalidDest) {
		t.Fatalf("error = %v", err)
	}
}

func ioDiscard() *bytes.Buffer { return &bytes.Buffer{} }

func errRepoInvalid() error {
	_, err := NewGit(0).Clone(context.Background(), "https://example.com/acme/demo", "", "/tmp", ioDiscard())
	return err
}
