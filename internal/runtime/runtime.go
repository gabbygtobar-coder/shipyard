// Package runtime starts and stops the containers that serve an application.
//
// The Docker implementation shells out to the docker CLI with argument lists,
// the same way image builds do. Tests use Fake.
package runtime

import (
	"context"
	"errors"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/gabbygtobar-coder/shipyard/internal/build"
)

const (
	// DefaultContainerPort is the port inside the container when none is configured.
	DefaultContainerPort = 8080
	// RunAsNonRootUser is the uid:gid used when an image does not already run as non-root.
	RunAsNonRootUser = "65534:65534"

	managedLabel = "shipyard.managed=true"
)

var (
	// ErrDockerNotFound is returned when the docker binary is not on PATH.
	ErrDockerNotFound = errors.New("docker not found")
	// ErrNotRunning is returned when an app has no container receiving traffic.
	ErrNotRunning = errors.New("app is not running")
	// ErrHealthTimeout is returned when the health check does not succeed in time.
	ErrHealthTimeout = errors.New("health check timed out")
	// ErrStartTimeout is returned when the container does not start in time.
	ErrStartTimeout = errors.New("container start timed out")
	// ErrUnhealthy is returned when a health probe does not get a successful response.
	ErrUnhealthy = errors.New("health check failed")
	// ErrInvalidSpec is returned when a container spec is outside the supported shape.
	ErrInvalidSpec = errors.New("invalid container spec")

	appPattern         = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	deploymentPattern  = regexp.MustCompile(`^dep_[1-9][0-9]{0,17}$`)
	containerIDPattern = regexp.MustCompile(`^[0-9a-f]{12,64}$`)
	networkPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
)

// Spec describes one container to start.
type Spec struct {
	Name          string
	Image         string
	AppName       string
	DeploymentID  string
	ContainerPort int
	MemoryBytes   int64
	CPUMilli      int
	Network       string
}

// Instance is a container the runtime started.
type Instance struct {
	ID           string
	Name         string
	AppName      string
	DeploymentID string
	// Endpoint is the URL the proxy and the health check dial.
	Endpoint string
}

// ContainerStatus is a point-in-time view of a container.
type ContainerStatus struct {
	ID       string
	Running  bool
	ExitCode int
	Error    string
}

// Runtime starts, stops, and inspects containers.
// Implementations must not invoke a shell.
type Runtime interface {
	Start(ctx context.Context, spec Spec) (Instance, error)
	Stop(ctx context.Context, id string) error
	Status(ctx context.Context, id string) (ContainerStatus, error)
	Logs(ctx context.Context, id string) (io.ReadCloser, error)
}

// Checker waits until an endpoint is healthy.
type Checker interface {
	Wait(ctx context.Context, endpoint string) error
}

// ContainerName returns the only container name Shipyard assigns.
func ContainerName(app, deploymentID string) (string, error) {
	if err := validateApp(app); err != nil || !deploymentPattern.MatchString(deploymentID) {
		return "", ErrInvalidSpec
	}
	return "shipyard-" + app + "-" + deploymentID, nil
}

func validateSpec(spec Spec) error {
	name, err := ContainerName(spec.AppName, spec.DeploymentID)
	if err != nil || name != spec.Name {
		return ErrInvalidSpec
	}
	if err := build.ValidateTag(spec.Image); err != nil {
		return ErrInvalidSpec
	}
	if spec.ContainerPort < 1 || spec.ContainerPort > 65535 {
		return ErrInvalidSpec
	}
	if spec.MemoryBytes <= 0 || spec.CPUMilli <= 0 {
		return ErrInvalidSpec
	}
	if spec.Network != "" && !networkPattern.MatchString(spec.Network) {
		return ErrInvalidSpec
	}
	return nil
}

func validateApp(name string) error {
	if !appPattern.MatchString(name) || strings.Contains(name, "--") || strings.HasSuffix(name, "-") {
		return ErrInvalidSpec
	}
	return nil
}

func validateContainerRef(id string) error {
	if containerIDPattern.MatchString(id) {
		return nil
	}
	if strings.HasPrefix(id, "shipyard-") && !strings.Contains(id, "..") && !strings.HasPrefix(id, "-") {
		_, _, err := parseContainerName(id)
		return err
	}
	return ErrInvalidSpec
}

func parseContainerName(name string) (app, deploymentID string, err error) {
	rest, ok := strings.CutPrefix(name, "shipyard-")
	if !ok {
		return "", "", ErrInvalidSpec
	}
	app, deploymentID, ok = strings.Cut(rest, "-dep_")
	if !ok {
		return "", "", ErrInvalidSpec
	}
	deploymentID = "dep_" + deploymentID
	if err := validateApp(app); err != nil || !deploymentPattern.MatchString(deploymentID) {
		return "", "", ErrInvalidSpec
	}
	if name != "shipyard-"+app+"-"+deploymentID {
		return "", "", ErrInvalidSpec
	}
	return app, deploymentID, nil
}

func formatCPU(milli int) string {
	return strconv.FormatFloat(float64(milli)/1000, 'f', 2, 64)
}
