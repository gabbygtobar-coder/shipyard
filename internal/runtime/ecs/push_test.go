package ecs

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/gabbygtobar-coder/shipyard/internal/proc"
	"github.com/gabbygtobar-coder/shipyard/internal/runtime"
)

type recordedCall struct {
	args  []string
	stdin string
}

type recordCmd struct {
	calls []recordedCall
}

func (r *recordCmd) Run(_ context.Context, _ string, args []string, spec proc.Spec) error {
	var buf bytes.Buffer
	if spec.Stdin != nil {
		if _, err := io.Copy(&buf, spec.Stdin); err != nil {
			return err
		}
	}
	r.calls = append(r.calls, recordedCall{args: append([]string(nil), args...), stdin: buf.String()})
	return nil
}

func TestDockerPushKeepsPasswordOffTheCommandLine(t *testing.T) {
	t.Parallel()
	cmd := &recordCmd{}
	pusher := dockerPusher{
		run:  cmd,
		look: func(string) error { return nil },
	}
	const password = "super-secret-token"
	auth := Auth{User: "AWS", Password: password, Host: testHost}
	remote := remoteFor(testImage)
	if err := pusher.Push(context.Background(), testImage, remote, auth); err != nil {
		t.Fatal(err)
	}
	if len(cmd.calls) != 3 {
		t.Fatalf("calls %+v", cmd.calls)
	}
	if strings.Join(cmd.calls[0].args, " ") != "tag "+testImage+" "+remote {
		t.Fatalf("tag %v", cmd.calls[0].args)
	}
	login := strings.Join(cmd.calls[1].args, " ")
	if login != "login --username AWS --password-stdin "+testHost {
		t.Fatalf("login %v", cmd.calls[1].args)
	}
	if cmd.calls[1].stdin != password {
		t.Fatalf("stdin %q", cmd.calls[1].stdin)
	}
	if strings.Join(cmd.calls[2].args, " ") != "push "+remote {
		t.Fatalf("push %v", cmd.calls[2].args)
	}
	for _, call := range cmd.calls {
		for _, arg := range call.args {
			if strings.Contains(arg, password) {
				t.Fatalf("password leaked into args %v", call.args)
			}
		}
	}
}

func TestDockerPushMissingBinary(t *testing.T) {
	t.Parallel()
	pusher := dockerPusher{
		run:  &recordCmd{},
		look: func(string) error { return errors.New("missing") },
	}
	err := pusher.Push(context.Background(), testImage, remoteFor(testImage), Auth{User: "AWS", Password: "pw", Host: testHost})
	if !errors.Is(err, runtime.ErrDockerNotFound) {
		t.Fatalf("error %v", err)
	}
}
