package api

import (
	"errors"
	"regexp"
	"strings"

	"github.com/gabbygtobar-coder/shipyard/internal/giturl"
)

var (
	// ErrNameRequired is returned when an app name is empty.
	ErrNameRequired = errors.New("name is required")
	// ErrNameInvalid is returned when an app name is not a valid slug.
	ErrNameInvalid = errors.New("name must be 1-63 characters, start with a lowercase letter, and contain only lowercase letters, digits, and single hyphens")
	// ErrRepoRequired is returned when repo_url is empty.
	ErrRepoRequired = giturl.ErrRepoRequired
	// ErrRepoInvalid is returned when repo_url is not an https GitHub repository URL.
	ErrRepoInvalid = giturl.ErrRepoInvalid
	// ErrDeploymentID is returned when a deployment id is not a shipyard id.
	ErrDeploymentID = errors.New("deployment id must look like dep_1")
)

// appNamePattern is the character set and length for an app slug.
// Trailing and repeated hyphens are rejected in ValidateName. Go's regexp
// engine does not support lookahead.
var appNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// deploymentIDPattern matches store-generated deployment ids.
var deploymentIDPattern = regexp.MustCompile(`^dep_[1-9][0-9]{0,17}$`)

// ValidateName reports whether name is a valid application slug.
// It does not trim; callers should trim surrounding whitespace first.
func ValidateName(name string) error {
	if name == "" {
		return ErrNameRequired
	}
	if !appNamePattern.MatchString(name) || !singleHyphens(name) {
		return ErrNameInvalid
	}
	return nil
}

// NormalizeRepoURL validates raw and returns a canonical https GitHub URL.
// Surrounding whitespace and a single trailing .git suffix are removed.
// The hostname is lowercased. The owner and repository keep their original case.
func NormalizeRepoURL(raw string) (string, error) {
	return giturl.NormalizeRepoURL(raw)
}

// ValidateDeploymentID reports whether id is a deployment id produced by the store.
func ValidateDeploymentID(id string) error {
	if !deploymentIDPattern.MatchString(id) {
		return ErrDeploymentID
	}
	return nil
}

func singleHyphens(value string) bool {
	return !strings.HasSuffix(value, "-") && !strings.Contains(value, "--")
}
