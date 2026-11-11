// Package source fetches application source code.
package source

import (
	"context"
	"errors"
	"io"
)

var (
	// ErrGitNotFound is returned when the git binary is not on PATH.
	ErrGitNotFound = errors.New("git not found")
	// ErrCloneTooLarge is returned when a checkout exceeds the size cap.
	ErrCloneTooLarge = errors.New("clone exceeded the size limit")
	// ErrInvalidCommit is returned when git does not report a commit sha.
	ErrInvalidCommit = errors.New("invalid commit sha")
	// ErrInvalidDest is returned when the destination directory is not an absolute path.
	ErrInvalidDest = errors.New("invalid clone directory")
)

// DefaultMaxCloneBytes is the checkout cap used when a limit is not configured.
const DefaultMaxCloneBytes = 100 << 20

// Cloner shallow-clones a repository into dest and returns the commit sha.
// An empty ref selects the remote default branch.
// logs receives human-readable git output. Implementations must not invoke a shell.
type Cloner interface {
	Clone(ctx context.Context, repoURL, ref, dest string, logs io.Writer) (commitSHA string, err error)
}
