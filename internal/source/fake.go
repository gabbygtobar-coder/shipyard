package source

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Fake is a Cloner for tests.
type Fake struct {
	mu    sync.Mutex
	SHA   string
	Err   error
	Files map[string]string
	Block <-chan struct{}
	Dests []string
	Calls int
	Refs  []string
}

// Clone implements Cloner.
func (f *Fake) Clone(ctx context.Context, repoURL, ref, dest string, logs io.Writer) (string, error) {
	f.mu.Lock()
	f.Calls++
	f.Dests = append(f.Dests, dest)
	f.Refs = append(f.Refs, ref)
	sha := f.SHA
	err := f.Err
	block := f.Block
	files := make(map[string]string, len(f.Files))
	for name, content := range f.Files {
		files[name] = content
	}
	f.mu.Unlock()

	if logs != nil {
		_, _ = fmt.Fprintf(logs, "clone %s ref %s\n", repoURL, ref)
	}
	if block != nil {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-block:
		}
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", ctxErr
	}
	if err != nil {
		return "", err
	}
	for name, content := range files {
		if writeErr := writeRepoFile(dest, name, content); writeErr != nil {
			return "", writeErr
		}
	}
	if sha == "" {
		sha = "0123456789abcdef0123456789abcdef01234567"
	}
	return sha, nil
}

func writeRepoFile(dest, name, content string) error {
	if name == "" || filepath.IsAbs(name) || strings.Contains(name, "..") {
		return ErrInvalidDest
	}
	path := filepath.Join(dest, filepath.FromSlash(name))
	rel, err := filepath.Rel(dest, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ErrInvalidDest
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o600)
}

// Snapshot returns how many clones ran and the destinations and refs they used.
func (f *Fake) Snapshot() (calls int, dests, refs []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Calls, append([]string(nil), f.Dests...), append([]string(nil), f.Refs...)
}

var _ Cloner = (*Fake)(nil)
