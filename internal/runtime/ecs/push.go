package ecs

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/gabbygtobar-coder/shipyard/internal/build"
	"github.com/gabbygtobar-coder/shipyard/internal/proc"
	"github.com/gabbygtobar-coder/shipyard/internal/runtime"
)

const dockerBinary = "docker"

type commandRunner interface {
	Run(ctx context.Context, name string, args []string, spec proc.Spec) error
}

type osCommand struct{}

func (osCommand) Run(ctx context.Context, name string, args []string, spec proc.Spec) error {
	return proc.Run(ctx, name, args, spec)
}

// dockerPusher runs docker tag, docker login --password-stdin, and docker push.
// The registry password is written to stdin and is never an argument.
type dockerPusher struct {
	run  commandRunner
	look func(string) error
}

func (p dockerPusher) Push(ctx context.Context, localRef, remoteRef string, auth Auth) error {
	if err := build.ValidateTag(localRef); err != nil || !validRemoteRef(remoteRef) {
		return runtime.ErrInvalidSpec
	}
	if !validAuth(auth) {
		return fmt.Errorf("invalid registry auth")
	}
	if p.look == nil || p.run == nil {
		return fmt.Errorf("docker pusher is not configured")
	}
	if err := p.look(dockerBinary); err != nil {
		return runtime.ErrDockerNotFound
	}
	if err := p.exec(ctx, []string{"tag", localRef, remoteRef}, nil, ""); err != nil {
		return fmt.Errorf("docker tag: %w", err)
	}
	if err := p.exec(ctx, []string{"login", "--username", auth.User, "--password-stdin", auth.Host}, strings.NewReader(auth.Password), auth.Password); err != nil {
		return fmt.Errorf("docker login: %w", err)
	}
	if err := p.exec(ctx, []string{"push", remoteRef}, nil, auth.Password); err != nil {
		return fmt.Errorf("docker push: %w", err)
	}
	return nil
}

func (p dockerPusher) exec(ctx context.Context, args []string, stdin io.Reader, secret string) error {
	var stderr bytes.Buffer
	err := p.run.Run(ctx, dockerBinary, args, proc.Spec{
		Stdin:  stdin,
		Stdout: io.Discard,
		Stderr: &stderr,
	})
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if secret != "" {
			msg = strings.ReplaceAll(msg, secret, "[redacted]")
		}
		if msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
		return err
	}
	return nil
}

func validAuth(auth Auth) bool {
	if auth.User == "" || auth.Password == "" || auth.Host == "" {
		return false
	}
	if strings.ContainsAny(auth.User+auth.Host, " \t\r\n") || strings.HasPrefix(auth.Host, "-") || strings.HasPrefix(auth.User, "-") {
		return false
	}
	return true
}
