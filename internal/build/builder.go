// Package build creates container images from a local build context.
package build

import (
	"context"
	"errors"
	"io"
	"regexp"
	"strings"
)

var (
	// ErrDockerNotFound is returned when the docker binary is not on PATH.
	ErrDockerNotFound = errors.New("docker not found")
	// ErrInvalidTag is returned when an image tag is outside the shipyard/<app>:<sha> form.
	ErrInvalidTag = errors.New("invalid image tag")
	// ErrInvalidContext is returned when the build context is not an absolute directory.
	ErrInvalidContext = errors.New("invalid build context")
)

// tagPattern matches the only image tags Shipyard asks docker to create.
var tagPattern = regexp.MustCompile(`^shipyard/[a-z][a-z0-9-]{0,62}:[0-9a-f]{7,64}$`)

// Builder builds an image from contextDir and tags it.
// logs receives the builder's output. Implementations must not invoke a shell.
type Builder interface {
	Build(ctx context.Context, contextDir, tag string, logs io.Writer) error
}

// ValidateTag reports whether tag is a Shipyard image tag.
func ValidateTag(tag string) error {
	if !tagPattern.MatchString(tag) {
		return ErrInvalidTag
	}
	name := strings.TrimPrefix(tag, "shipyard/")
	name, _, ok := strings.Cut(name, ":")
	if !ok || strings.Contains(name, "--") || strings.HasSuffix(name, "-") {
		return ErrInvalidTag
	}
	return nil
}
