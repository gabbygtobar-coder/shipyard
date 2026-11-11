package proc

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRunCancelsProcessGroup(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, "sleep", []string{"30"}, Spec{})
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("sleep was not killed after cancel")
	}
}

func TestRunPassesStdin(t *testing.T) {
	t.Parallel()
	var stdout bytes.Buffer
	err := Run(context.Background(), "cat", nil, Spec{
		Stdin:  strings.NewReader("hello"),
		Stdout: &stdout,
	})
	if err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "hello" {
		t.Fatalf("stdout %q", stdout.String())
	}
}
