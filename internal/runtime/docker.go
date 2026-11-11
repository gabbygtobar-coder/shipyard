package runtime

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/gabbygtobar-coder/shipyard/internal/proc"
)

const dockerBinary = "docker"

type commandRunner interface {
	Run(ctx context.Context, name string, args []string, spec proc.Spec) error
}

type osCommand struct{}

func (osCommand) Run(ctx context.Context, name string, args []string, spec proc.Spec) error {
	return proc.Run(ctx, name, args, spec)
}

// Docker runs containers by executing the docker CLI.
//
// Arguments are exec parameters, never a shell string. Images are published on
// 127.0.0.1 with a random host port, or attached to Network with no host port.
type Docker struct {
	run  commandRunner
	look func(string) error
}

// NewDocker returns a runtime that uses the docker CLI on PATH.
func NewDocker() *Docker {
	return &Docker{
		run: osCommand{},
		look: func(name string) error {
			_, err := exec.LookPath(name)
			return err
		},
	}
}

// Start implements Runtime.
func (d *Docker) Start(ctx context.Context, spec Spec) (Instance, error) {
	if err := validateSpec(spec); err != nil {
		return Instance{}, err
	}
	if err := d.look(dockerBinary); err != nil {
		return Instance{}, ErrDockerNotFound
	}
	if spec.Network != "" {
		if err := d.ensureNetwork(ctx, spec.Network); err != nil {
			return Instance{}, err
		}
	}
	user, err := d.imageUser(ctx, spec.Image)
	if err != nil {
		return Instance{}, err
	}
	var stdout, stderr bytes.Buffer
	runErr := d.run.Run(ctx, dockerBinary, runArgs(spec, user), proc.Spec{Stdout: &stdout, Stderr: &stderr})
	if runErr != nil {
		d.removeQuiet(spec.Name)
		return Instance{}, fmt.Errorf("docker run: %w%s", runErr, suffix(stderr.String()))
	}
	id := strings.TrimSpace(stdout.String())
	if !containerIDPattern.MatchString(id) {
		d.removeQuiet(spec.Name)
		return Instance{}, fmt.Errorf("docker run returned an unexpected id")
	}
	endpoint, err := d.endpoint(ctx, spec, id)
	if err != nil {
		d.removeQuiet(id)
		return Instance{}, err
	}
	return Instance{
		ID:           id,
		Name:         spec.Name,
		AppName:      spec.AppName,
		DeploymentID: spec.DeploymentID,
		Endpoint:     endpoint,
	}, nil
}

// Stop implements Runtime.
func (d *Docker) Stop(ctx context.Context, id string) error {
	if err := validateContainerRef(id); err != nil {
		return err
	}
	if err := d.look(dockerBinary); err != nil {
		return ErrDockerNotFound
	}
	return d.remove(ctx, id)
}

// Status implements Runtime.
func (d *Docker) Status(ctx context.Context, id string) (ContainerStatus, error) {
	if err := validateContainerRef(id); err != nil {
		return ContainerStatus{}, err
	}
	if err := d.look(dockerBinary); err != nil {
		return ContainerStatus{}, ErrDockerNotFound
	}
	var stdout, stderr bytes.Buffer
	args := []string{"inspect", "--format", "{{.State.Running}}|{{.State.ExitCode}}|{{.State.Error}}", "--", id}
	if err := d.run.Run(ctx, dockerBinary, args, proc.Spec{Stdout: &stdout, Stderr: &stderr}); err != nil {
		return ContainerStatus{}, fmt.Errorf("docker inspect: %w%s", err, suffix(stderr.String()))
	}
	return parseStatus(id, stdout.String())
}

// Logs implements Runtime.
func (d *Docker) Logs(ctx context.Context, id string) (io.ReadCloser, error) {
	if err := validateContainerRef(id); err != nil {
		return nil, err
	}
	if err := d.look(dockerBinary); err != nil {
		return nil, ErrDockerNotFound
	}
	var stdout, stderr bytes.Buffer
	if err := d.run.Run(ctx, dockerBinary, []string{"logs", "--", id}, proc.Spec{Stdout: &stdout, Stderr: &stderr}); err != nil {
		return nil, fmt.Errorf("docker logs: %w%s", err, suffix(stderr.String()))
	}
	buf := append(stdout.Bytes(), stderr.Bytes()...)
	return io.NopCloser(bytes.NewReader(buf)), nil
}

// StopManaged removes containers this process family started.
// shipyardd calls it on startup so a crash does not leave old containers behind.
func (d *Docker) StopManaged(ctx context.Context) error {
	if err := d.look(dockerBinary); err != nil {
		return ErrDockerNotFound
	}
	var stdout, stderr bytes.Buffer
	args := []string{"ps", "-aq", "--filter", "label=" + managedLabel}
	if err := d.run.Run(ctx, dockerBinary, args, proc.Spec{Stdout: &stdout, Stderr: &stderr}); err != nil {
		return fmt.Errorf("docker ps: %w%s", err, suffix(stderr.String()))
	}
	var ids []string
	for _, line := range strings.Split(stdout.String(), "\n") {
		id := strings.TrimSpace(line)
		if id == "" {
			continue
		}
		if !containerIDPattern.MatchString(id) {
			return fmt.Errorf("docker ps returned an unexpected id")
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil
	}
	rm := append([]string{"rm", "-f"}, ids...)
	stderr.Reset()
	if err := d.run.Run(ctx, dockerBinary, rm, proc.Spec{Stdout: io.Discard, Stderr: &stderr}); err != nil {
		return fmt.Errorf("docker rm: %w%s", err, suffix(stderr.String()))
	}
	return nil
}

func (d *Docker) endpoint(ctx context.Context, spec Spec, id string) (string, error) {
	if spec.Network != "" {
		return fmt.Sprintf("http://%s:%d", spec.Name, spec.ContainerPort), nil
	}
	port, err := d.publishedPort(ctx, id, spec.ContainerPort)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("http://127.0.0.1:%d", port), nil
}

func (d *Docker) publishedPort(ctx context.Context, id string, containerPort int) (int, error) {
	var stdout, stderr bytes.Buffer
	args := []string{"port", id, strconv.Itoa(containerPort) + "/tcp"}
	if err := d.run.Run(ctx, dockerBinary, args, proc.Spec{Stdout: &stdout, Stderr: &stderr}); err != nil {
		return 0, fmt.Errorf("docker port: %w%s", err, suffix(stderr.String()))
	}
	return parsePublishedPort(stdout.String())
}

func (d *Docker) imageUser(ctx context.Context, image string) (string, error) {
	var stdout, stderr bytes.Buffer
	args := []string{"image", "inspect", "--format", "{{.Config.User}}", "--", image}
	if err := d.run.Run(ctx, dockerBinary, args, proc.Spec{Stdout: &stdout, Stderr: &stderr}); err != nil {
		return "", fmt.Errorf("inspect image user: %w%s", err, suffix(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

func (d *Docker) ensureNetwork(ctx context.Context, name string) error {
	inspect := []string{"network", "inspect", "--", name}
	if err := d.run.Run(ctx, dockerBinary, inspect, proc.Spec{Stdout: io.Discard, Stderr: io.Discard}); err == nil {
		return nil
	}
	var stderr bytes.Buffer
	create := []string{"network", "create", "--", name}
	if err := d.run.Run(ctx, dockerBinary, create, proc.Spec{Stdout: io.Discard, Stderr: &stderr}); err != nil {
		return fmt.Errorf("create network: %w%s", err, suffix(stderr.String()))
	}
	return nil
}

func (d *Docker) remove(ctx context.Context, id string) error {
	var stderr bytes.Buffer
	err := d.run.Run(ctx, dockerBinary, []string{"rm", "-f", "--", id}, proc.Spec{Stdout: io.Discard, Stderr: &stderr})
	if err != nil {
		return fmt.Errorf("docker rm: %w%s", err, suffix(stderr.String()))
	}
	return nil
}

func (d *Docker) removeQuiet(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = d.remove(ctx, id)
}

func runArgs(spec Spec, imageUser string) []string {
	args := []string{
		"run", "-d",
		"--name", spec.Name,
		"--label", managedLabel,
		"--label", "shipyard.app=" + spec.AppName,
		"--label", "shipyard.deployment=" + spec.DeploymentID,
		"--memory", strconv.FormatInt(spec.MemoryBytes, 10),
		"--memory-swap", strconv.FormatInt(spec.MemoryBytes, 10),
		"--cpus", formatCPU(spec.CPUMilli),
		"--pids-limit", "256",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--restart", "no",
		"--pull", "never",
	}
	if !imageRunsAsNonRoot(imageUser) {
		args = append(args, "--user", RunAsNonRootUser)
	}
	args = append(args, "--env", "PORT="+strconv.Itoa(spec.ContainerPort))
	if spec.Network != "" {
		args = append(args, "--network", spec.Network)
	} else {
		args = append(args, "--publish", fmt.Sprintf("127.0.0.1::%d", spec.ContainerPort))
	}
	args = append(args, "--", spec.Image)
	return args
}

func imageRunsAsNonRoot(user string) bool {
	user = strings.TrimSpace(user)
	name, _, _ := strings.Cut(user, ":")
	name = strings.TrimSpace(name)
	return name != "" && name != "root" && name != "0"
}

func parsePublishedPort(out string) (int, error) {
	found := 0
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		_, port, err := net.SplitHostPort(line)
		if err != nil {
			continue
		}
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			continue
		}
		if strings.HasPrefix(line, "127.0.0.1:") {
			return n, nil
		}
		if found == 0 {
			found = n
		}
	}
	if found == 0 {
		return 0, fmt.Errorf("docker did not publish a host port")
	}
	return found, nil
}

func parseStatus(id, out string) (ContainerStatus, error) {
	parts := strings.SplitN(strings.TrimSpace(out), "|", 3)
	if len(parts) != 3 {
		return ContainerStatus{}, fmt.Errorf("docker inspect returned an unexpected status")
	}
	running, err := strconv.ParseBool(strings.TrimSpace(parts[0]))
	if err != nil {
		return ContainerStatus{}, fmt.Errorf("docker inspect returned an unexpected status")
	}
	code, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return ContainerStatus{}, fmt.Errorf("docker inspect returned an unexpected status")
	}
	return ContainerStatus{
		ID:       id,
		Running:  running,
		ExitCode: code,
		Error:    strings.TrimSpace(parts[2]),
	}, nil
}

func suffix(stderr string) string {
	msg := strings.TrimSpace(stderr)
	if msg == "" {
		return ""
	}
	return ": " + msg
}

var _ Runtime = (*Docker)(nil)
