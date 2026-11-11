package build

import (
	"context"
	"io"
	"sync"
)

// Fake is a Builder for tests.
type Fake struct {
	mu    sync.Mutex
	Err   error
	Log   string
	Block <-chan struct{}
	Hook  func(ctx context.Context, contextDir, tag string, logs io.Writer) error
	Calls int
	Tags  []string
	Dirs  []string
}

// Build implements Builder.
func (f *Fake) Build(ctx context.Context, contextDir, tag string, logs io.Writer) error {
	f.mu.Lock()
	f.Calls++
	f.Tags = append(f.Tags, tag)
	f.Dirs = append(f.Dirs, contextDir)
	err := f.Err
	logLine := f.Log
	block := f.Block
	hook := f.Hook
	f.mu.Unlock()

	if hook != nil {
		return hook(ctx, contextDir, tag, logs)
	}
	if logLine != "" && logs != nil {
		if _, writeErr := io.WriteString(logs, logLine); writeErr != nil {
			return writeErr
		}
	}
	if block != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-block:
		}
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return err
}

// Snapshot returns how many builds ran and the tags they used.
func (f *Fake) Snapshot() (calls int, tags []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Calls, append([]string(nil), f.Tags...)
}

var _ Builder = (*Fake)(nil)
