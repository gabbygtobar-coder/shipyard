package build

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/gabbygtobar-coder/shipyard/internal/proc"
)

type recorded struct {
	name string
	args []string
	env  []string
}

type script struct {
	mu   sync.Mutex
	cmds []recorded
	err  error
}

func (s *script) Run(_ context.Context, name string, args []string, spec proc.Spec) error {
	s.mu.Lock()
	s.cmds = append(s.cmds, recorded{name: name, args: append([]string(nil), args...), env: append([]string(nil), spec.Env...)})
	err := s.err
	s.mu.Unlock()
	if spec.Stdout != nil {
		_, _ = spec.Stdout.Write([]byte("step ok\n"))
	}
	return err
}

func (s *script) commands() []recorded {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]recorded, len(s.cmds))
	copy(out, s.cmds)
	return out
}

func testDocker(s *script) *Docker {
	return &Docker{
		run:  s,
		look: func(string) error { return nil },
	}
}

func TestDockerBuildArgsAreNotAShell(t *testing.T) {
	t.Parallel()
	s := &script{}
	d := testDocker(s)
	dir := t.TempDir()
	tag := "shipyard/demo:0123456789ab"
	if err := d.Build(context.Background(), dir, tag, nil); err != nil {
		t.Fatal(err)
	}
	cmds := s.commands()
	if len(cmds) != 1 {
		t.Fatalf("commands = %d", len(cmds))
	}
	cmd := cmds[0]
	if cmd.name != "docker" {
		t.Fatalf("name = %s", cmd.name)
	}
	want := []string{"build", "--progress=plain", "-t", tag, "--", dir}
	if strings.Join(cmd.args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("args = %#v, want %#v", cmd.args, want)
	}
	found := false
	for _, kv := range cmd.env {
		if kv == "DOCKER_BUILDKIT=1" {
			found = true
		}
		if strings.Contains(kv, "DOCKER_BUILDKIT=0") {
			t.Fatalf("buildkit disabled: %s", kv)
		}
	}
	if !found {
		t.Fatal("DOCKER_BUILDKIT=1 was not set")
	}
}

func TestDockerBuildRejectsBadTagWithoutRunning(t *testing.T) {
	t.Parallel()
	s := &script{}
	d := testDocker(s)
	err := d.Build(context.Background(), t.TempDir(), "evil:latest", nil)
	if !errors.Is(err, ErrInvalidTag) {
		t.Fatalf("error = %v", err)
	}
	if len(s.commands()) != 0 {
		t.Fatal("docker was invoked")
	}
}

func TestDockerBuildRejectsRelativeContext(t *testing.T) {
	t.Parallel()
	s := &script{}
	d := testDocker(s)
	err := d.Build(context.Background(), "relative", "shipyard/demo:0123456789ab", nil)
	if !errors.Is(err, ErrInvalidContext) {
		t.Fatalf("error = %v", err)
	}
	if len(s.commands()) != 0 {
		t.Fatal("docker was invoked")
	}
}

func TestValidateTag(t *testing.T) {
	t.Parallel()
	if err := ValidateTag("shipyard/demo-api:0123456789ab"); err != nil {
		t.Fatal(err)
	}
	bad := []string{
		"demo:0123456789ab",
		"shipyard/Demo:0123456789ab",
		"shipyard/demo-:0123456789ab",
		"shipyard/demo--api:0123456789ab",
		"shipyard/demo:zzzzzzzzzzzz",
		"shipyard/demo:abc",
		"shipyard/demo:0123456789ab;rm",
	}
	for _, tag := range bad {
		if err := ValidateTag(tag); err == nil {
			t.Fatalf("ValidateTag(%q) accepted", tag)
		}
	}
}

func TestDockerBuildWithArgs(t *testing.T) {
	t.Parallel()
	s := &script{}
	d := testDocker(s)
	dir := t.TempDir()
	tag := "shipyard/demo:0123456789ab"
	if err := d.BuildWithArgs(context.Background(), dir, tag, map[string]string{"VERSION": "v1"}, nil); err != nil {
		t.Fatal(err)
	}
	cmd := s.commands()[0]
	want := []string{"build", "--progress=plain", "--build-arg", "VERSION=v1", "-t", tag, "--", dir}
	if strings.Join(cmd.args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("args = %#v", cmd.args)
	}
	if err := d.BuildWithArgs(context.Background(), dir, tag, map[string]string{"version": "v1"}, nil); err == nil {
		t.Fatal("expected a rejected build arg")
	}
}

func TestDockerBuildMissingBinary(t *testing.T) {
	t.Parallel()
	d := &Docker{
		run:  &script{},
		look: func(string) error { return os.ErrNotExist },
	}
	err := d.Build(context.Background(), t.TempDir(), "shipyard/demo:0123456789ab", nil)
	if !errors.Is(err, ErrDockerNotFound) {
		t.Fatalf("error = %v", err)
	}
}
