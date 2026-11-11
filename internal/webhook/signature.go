// Package webhook verifies GitHub webhook deliveries and reads push events.
package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
)

const signaturePrefix = "sha256="

// Verify reports whether header is the HMAC-SHA256 of body keyed by secret.
// The comparison is constant time. An empty secret never verifies.
// secret is not logged.
func Verify(secret string, body []byte, header string) bool {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	expected := mac.Sum(nil)
	got := make([]byte, len(expected))
	decoded := decodeSignature(header, got)
	match := hmac.Equal(got, expected)
	return secret != "" && decoded && match
}

func decodeSignature(header string, dst []byte) bool {
	header = strings.TrimSpace(header)
	encoded, ok := strings.CutPrefix(header, signaturePrefix)
	if !ok || len(encoded) != hex.EncodedLen(len(dst)) {
		return false
	}
	raw, err := hex.DecodeString(encoded)
	if err != nil || len(raw) != len(dst) {
		return false
	}
	copy(dst, raw)
	return true
}

// Push is the part of a GitHub push delivery Shipyard acts on.
type Push struct {
	RepoURL string
	Branch  string
	SHA     string
	Deleted bool
}

// ErrIgnored is returned when the body is not a branch push Shipyard can deploy.
var ErrIgnored = errors.New("ignored webhook event")

// ParsePush reads a GitHub push payload.
// A push that is not a branch update returns ErrIgnored.
func ParsePush(body []byte) (Push, error) {
	var payload struct {
		Ref     string `json:"ref"`
		After   string `json:"after"`
		Deleted bool   `json:"deleted"`
		Repo    struct {
			CloneURL string `json:"clone_url"`
			HTMLURL  string `json:"html_url"`
			FullName string `json:"full_name"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return Push{}, err
	}
	push := Push{
		RepoURL: repoURL(payload.Repo.CloneURL, payload.Repo.HTMLURL, payload.Repo.FullName),
		Deleted: payload.Deleted || zeroSHA(payload.After),
		SHA:     strings.ToLower(strings.TrimSpace(payload.After)),
	}
	branch, ok := strings.CutPrefix(payload.Ref, "refs/heads/")
	if !ok || branch == "" || strings.Contains(branch, "\n") {
		return push, ErrIgnored
	}
	push.Branch = branch
	return push, nil
}

// RepoURL returns a repository URL from a webhook body when one is present.
func RepoURL(body []byte) string {
	var payload struct {
		Repo struct {
			CloneURL string `json:"clone_url"`
			HTMLURL  string `json:"html_url"`
			FullName string `json:"full_name"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	return repoURL(payload.Repo.CloneURL, payload.Repo.HTMLURL, payload.Repo.FullName)
}

func repoURL(cloneURL, htmlURL, fullName string) string {
	for _, candidate := range []string{cloneURL, htmlURL} {
		if strings.TrimSpace(candidate) != "" {
			return candidate
		}
	}
	fullName = strings.Trim(strings.TrimSpace(fullName), "/")
	if fullName == "" || strings.Contains(fullName, " ") {
		return ""
	}
	return "https://github.com/" + fullName
}

func zeroSHA(sha string) bool {
	sha = strings.TrimSpace(sha)
	if sha == "" {
		return false
	}
	for _, r := range sha {
		if r != '0' {
			return false
		}
	}
	return true
}
