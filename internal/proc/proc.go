// Package proc runs external commands without a shell.
//
// Arguments are passed as separate exec parameters. On Unix the child is
// placed in its own process group and killed when ctx is canceled, so git
// and docker helpers do not outlive the build.
package proc

import (
	"bytes"
	"context"
	"io"
	"os/exec"
)

// Spec is a subprocess invocation.
// A nil Env uses the current process environment.
// A nil Stdin reads from the null device, so secrets can be passed on stdin
// without becoming command arguments.
type Spec struct {
	Dir    string
	Env    []string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// Run starts name with args and waits until it exits.
// When ctx ends, the process is killed and Run returns ctx.Err().
func Run(ctx context.Context, name string, args []string, spec Spec) error {
	cmd := command(ctx, name, args, spec)
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	return nil
}

// Output runs name and returns stdout. spec.Stderr receives stderr.
func Output(ctx context.Context, name string, args []string, spec Spec) ([]byte, error) {
	var buf bytes.Buffer
	spec.Stdout = &buf
	err := Run(ctx, name, args, spec)
	return buf.Bytes(), err
}

func command(ctx context.Context, name string, args []string, spec Spec) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = spec.Dir
	if spec.Env != nil {
		cmd.Env = spec.Env
	}
	cmd.Stdin = spec.Stdin
	cmd.Stdout = spec.Stdout
	cmd.Stderr = spec.Stderr
	configureCancel(cmd)
	return cmd
}
