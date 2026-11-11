package ecs

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/gabbygtobar-coder/shipyard/internal/build"
	"github.com/gabbygtobar-coder/shipyard/internal/runtime"
)

var appPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

func validApp(name string) bool {
	return appPattern.MatchString(name) && !strings.Contains(name, "--") && !strings.HasSuffix(name, "-")
}

func serviceName(app string) (string, error) {
	if !validApp(app) {
		return "", runtime.ErrInvalidSpec
	}
	name := namePrefix + app
	if len(name) > 32 {
		return "", fmt.Errorf("app name %s is too long for an AWS target group", app)
	}
	return name, nil
}

func parseRepository(ref string) (host, name string, err error) {
	ref = strings.TrimPrefix(strings.TrimSpace(ref), "https://")
	ref = strings.TrimPrefix(ref, "http://")
	ref = strings.TrimSuffix(ref, "/")
	host, name, ok := strings.Cut(ref, "/")
	if !ok || host == "" || name == "" || strings.Contains(host, ":") || strings.ContainsAny(ref, " \t\r\n") || strings.Contains(name, "..") || strings.HasPrefix(name, "-") {
		return "", "", fmt.Errorf("invalid ecr repository")
	}
	return host, name, nil
}

func ecrTag(local string) (string, error) {
	if err := build.ValidateTag(local); err != nil {
		return "", runtime.ErrInvalidSpec
	}
	rest := strings.TrimPrefix(local, "shipyard/")
	app, sha, ok := strings.Cut(rest, ":")
	if !ok || app == "" || sha == "" {
		return "", runtime.ErrInvalidSpec
	}
	return app + "-" + sha, nil
}

func remoteImage(repository, local string) (string, error) {
	host, name, err := parseRepository(repository)
	if err != nil {
		return "", err
	}
	tag, err := ecrTag(local)
	if err != nil {
		return "", err
	}
	ref := host + "/" + name + ":" + tag
	if !validRemoteRef(ref) {
		return "", fmt.Errorf("invalid remote image")
	}
	return ref, nil
}

func validRemoteRef(ref string) bool {
	if ref == "" || len(ref) > 512 || strings.ContainsAny(ref, " \t\r\n'\"\\") || strings.Contains(ref, "..") {
		return false
	}
	host, rest, ok := strings.Cut(ref, "/")
	if !ok || host == "" || strings.Contains(host, ":") || strings.HasPrefix(host, "-") {
		return false
	}
	name, tag, ok := strings.Cut(rest, ":")
	if !ok || name == "" || tag == "" || strings.Contains(tag, ":") || strings.HasPrefix(tag, "-") {
		return false
	}
	return true
}

func instanceID(app string, revision int) string {
	return fmt.Sprintf("%s%s/%d", idPrefix, app, revision)
}

func activeInstanceID(app string) string {
	return idPrefix + app + "/" + activeRevision
}

func parseInstanceID(id string) (app string, revision int, active bool, err error) {
	rest, ok := strings.CutPrefix(id, idPrefix)
	if !ok {
		return "", 0, false, runtime.ErrInvalidSpec
	}
	app, rev, ok := strings.Cut(rest, "/")
	if !ok || !validApp(app) || strings.Contains(rev, "/") {
		return "", 0, false, runtime.ErrInvalidSpec
	}
	if rev == activeRevision {
		return app, 0, true, nil
	}
	n, convErr := strconv.Atoi(rev)
	if convErr != nil || n < 1 {
		return "", 0, false, runtime.ErrInvalidSpec
	}
	return app, n, false, nil
}

func revisionOfARN(arn string) int {
	i := strings.LastIndex(arn, ":")
	if i < 0 || i == len(arn)-1 {
		return 0
	}
	n, err := strconv.Atoi(arn[i+1:])
	if err != nil || n < 1 {
		return 0
	}
	return n
}

// PublicURL is the address a browser uses for the app.
// An empty base domain uses the ALB path rule. A domain uses the host rule.
func PublicURL(albDNS, baseDomain, app string) (string, error) {
	if !validApp(app) {
		return "", runtime.ErrInvalidSpec
	}
	if strings.TrimSpace(baseDomain) != "" {
		return "http://" + app + "." + baseDomain + "/", nil
	}
	if strings.TrimSpace(albDNS) == "" || strings.ContainsAny(albDNS, " \t/") {
		return "", runtime.ErrInvalidSpec
	}
	return "http://" + albDNS + "/apps/" + app + "/", nil
}
