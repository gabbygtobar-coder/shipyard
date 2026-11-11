package build

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/gabbygtobar-coder/shipyard/internal/proc"
)

const dockerBinary = "docker"

type commandRunner interface {
	Run(ctx context.Context, name string, args []string, spec proc.Spec) error
}

type osCommand struct{}

func (osCommand) Run(ctx context.Context, name string, args []string, spec proc.Spec) error {
	return proc.Run(ctx, name, args, spec)
}

// Docker builds images by executing the docker CLI.
//
// The CLI is used instead of the Docker Engine SDK because current Docker
// installs build with BuildKit, and `docker build` is that builder. The Engine
// SDK's ImageBuild API is the legacy builder; driving BuildKit through the SDK
// pulls in a session client and a large module graph. Arguments are exec
// parameters, never a shell string.
type Docker struct {
	run  commandRunner
	look func(string) error
}

// NewDocker returns a builder that runs `docker build`.
func NewDocker() *Docker {
	return &Docker{
		run: osCommand{},
		look: func(name string) error {
			_, err := exec.LookPath(name)
			return err
		},
	}
}

// Build implements Builder.
func (d *Docker) Build(ctx context.Context, contextDir, tag string, logs io.Writer) error {
	if err := ValidateTag(tag); err != nil {
		return err
	}
	if err := validateContext(contextDir); err != nil {
		return err
	}
	if logs == nil {
		logs = io.Discard
	}
	if err := d.look(dockerBinary); err != nil {
		return ErrDockerNotFound
	}
	args := []string{"build", "--progress=plain", "-t", tag, "--", contextDir}
	spec := proc.Spec{
		Env:    setEnv(os.Environ(), "DOCKER_BUILDKIT", "1"),
		Stdout: logs,
		Stderr: logs,
	}
	return d.run.Run(ctx, dockerBinary, args, spec)
}

func validateContext(dir string) error {
	if dir == "" || !filepath.IsAbs(dir) || strings.HasPrefix(dir, "-") || strings.ContainsAny(dir, "\x00\n\r") {
		return ErrInvalidContext
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return ErrInvalidContext
	}
	return nil
}

func setEnv(base []string, key, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(base)+1)
	for _, kv := range base {
		if strings.HasPrefix(kv, prefix) {
			continue
		}
		out = append(out, kv)
	}
	return append(out, prefix+value)
}

var _ Builder = (*Docker)(nil)
