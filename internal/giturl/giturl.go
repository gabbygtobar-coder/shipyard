// Package giturl validates GitHub repository URLs and git refs.
//
// Shipyard accepts only https://github.com/<owner>/<repo>. Ref names are
// restricted to a small character set so they can be passed as a single git
// argument without shell parsing.
package giturl

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
)

var (
	// ErrRepoRequired is returned when repo_url is empty.
	ErrRepoRequired = errors.New("repo_url is required")
	// ErrRepoInvalid is returned when repo_url is not an https GitHub repository URL.
	ErrRepoInvalid = errors.New("repo_url must be an https://github.com/<owner>/<repo> URL")
	// ErrRefInvalid is returned when a git ref contains characters Shipyard refuses to pass to git.
	ErrRefInvalid = errors.New("ref must be empty or a branch, tag, or commit sha of letters, digits, dots, underscores, hyphens, and slashes")
)

// githubOwnerPattern is the character set and length for a GitHub username.
// Trailing and repeated hyphens are rejected in validGitHubOwner.
var githubOwnerPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`)

// githubRepoPattern matches a GitHub repository name (max 100 characters).
var githubRepoPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,99}$`)

// refPattern is the character set for a branch, tag, or commit sha.
// Leading hyphens are rejected so a ref cannot be parsed as a git option.
var refPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,254}$`)

// shaPattern matches a Git SHA-1 or SHA-256 hex digest.
var shaPattern = regexp.MustCompile(`^[0-9a-f]{40}$|^[0-9a-f]{64}$`)

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

// ValidateRef reports whether ref may be passed to git fetch.
// An empty ref means the remote default branch.
func ValidateRef(ref string) error {
	if ref == "" {
		return nil
	}
	if !refPattern.MatchString(ref) || strings.Contains(ref, "..") || strings.Contains(ref, "//") {
		return ErrRefInvalid
	}
	if strings.HasPrefix(ref, "/") || strings.HasSuffix(ref, "/") || strings.HasSuffix(ref, ".") || strings.HasSuffix(ref, ".lock") {
		return ErrRefInvalid
	}
	return nil
}

// ValidSHA reports whether sha is a lowercase SHA-1 or SHA-256 hex digest.
func ValidSHA(sha string) bool {
	return shaPattern.MatchString(sha)
}

func validGitHubOwner(owner string) bool {
	return githubOwnerPattern.MatchString(owner) && singleHyphens(owner)
}

func singleHyphens(value string) bool {
	return !strings.HasSuffix(value, "-") && !strings.Contains(value, "--")
}
