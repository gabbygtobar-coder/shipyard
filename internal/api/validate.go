package api

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
)

var (
	// ErrNameRequired is returned when an app name is empty.
	ErrNameRequired = errors.New("name is required")
	// ErrNameInvalid is returned when an app name is not a valid slug.
	ErrNameInvalid = errors.New("name must be 1-63 characters, start with a lowercase letter, and contain only lowercase letters, digits, and single hyphens")
	// ErrRepoRequired is returned when repo_url is empty.
	ErrRepoRequired = errors.New("repo_url is required")
	// ErrRepoInvalid is returned when repo_url is not an https GitHub repository URL.
	ErrRepoInvalid = errors.New("repo_url must be an https://github.com/<owner>/<repo> URL")
)

// appNamePattern is the character set and length for an app slug.
// Trailing and repeated hyphens are rejected in ValidateName. Go's regexp
// engine does not support lookahead.
var appNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// githubOwnerPattern is the character set and length for a GitHub username.
// Trailing and repeated hyphens are rejected in validGitHubOwner.
var githubOwnerPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`)

// githubRepoPattern matches a GitHub repository name (max 100 characters).
var githubRepoPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,99}$`)

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

func validGitHubOwner(owner string) bool {
	return githubOwnerPattern.MatchString(owner) && singleHyphens(owner)
}

func singleHyphens(value string) bool {
	return !strings.HasSuffix(value, "-") && !strings.Contains(value, "--")
}

// NormalizeRepoURL validates raw and returns a canonical https GitHub URL.
// Surrounding whitespace and a single trailing .git suffix are removed.
// The hostname is lowercased. The owner and repository keep their original case.
func NormalizeRepoURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ErrRepoRequired
	}

	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Scheme == "" {
		return "", ErrRepoInvalid
	}
	if !strings.EqualFold(u.Scheme, "https") || u.User != nil || u.Port() != "" {
		return "", ErrRepoInvalid
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", ErrRepoInvalid
	}
	if !strings.EqualFold(u.Hostname(), "github.com") {
		return "", ErrRepoInvalid
	}
	if strings.ContainsAny(u.EscapedPath(), "%\\") {
		return "", ErrRepoInvalid
	}

	path := strings.Trim(u.EscapedPath(), "/")
	owner, repo, ok := strings.Cut(path, "/")
	if !ok || owner == "" || repo == "" || strings.Contains(repo, "/") {
		return "", ErrRepoInvalid
	}
	repo = strings.TrimSuffix(repo, ".git")
	if !validGitHubOwner(owner) || !githubRepoPattern.MatchString(repo) {
		return "", ErrRepoInvalid
	}
	return "https://github.com/" + owner + "/" + repo, nil
}
