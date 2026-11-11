package runtime

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/gabbygtobar-coder/shipyard/internal/proc"
)

type recorded struct {
	name string
	args []string
}

type script struct {
	mu         sync.Mutex
	cmds       []recorded
	user       string
	port       string
	id         string
	ps         string
	inspectErr error
	err        error
}

func (s *script) Run(_ context.Context, name string, args []string, spec proc.Spec) error {
	s.mu.Lock()
	s.cmds = append(s.cmds, recorded{name: name, args: append([]string(nil), args...)})
	user, port, id, ps := s.user, s.port, s.id, s.ps
	inspectErr := s.inspectErr
	err := s.err
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if len(args) == 0 {
		return nil
	}
	switch args[0] {
	case "network":
		if len(args) > 1 && args[1] == "inspect" && inspectErr != nil {
			return inspectErr
		}
	case "image":
		if spec.Stdout != nil {
			_, _ = io.WriteString(spec.Stdout, user)
		}
	case "run":
		if spec.Stdout != nil {
			_, _ = io.WriteString(spec.Stdout, id+"\n")
		}
	case "port":
		if spec.Stdout != nil {
			_, _ = io.WriteString(spec.Stdout, port+"\n")
		}
	case "inspect":
		if spec.Stdout != nil {
			_, _ = io.WriteString(spec.Stdout, "true|0|\n")
		}
	case "logs":
		if spec.Stdout != nil {
			_, _ = io.WriteString(spec.Stdout, "out\n")
		}
		if spec.Stderr != nil {
			_, _ = io.WriteString(spec.Stderr, "err\n")
		}
	case "ps":
		if spec.Stdout != nil {
			_, _ = io.WriteString(spec.Stdout, ps)
		}
	}
	return nil
}

func (s *script) commands() []recorded {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]recorded, len(s.cmds))
	copy(out, s.cmds)
	return out
}

func testDocker(s *script) *Docker {
	return &Docker{run: s, look: func(string) error { return nil }}
}

func testSpec(network string) Spec {
	name, err := ContainerName("demo", "dep_1")
	if err != nil {
		panic(err)
	}
	return Spec{
		Name:          name,
		Image:         "shipyard/demo:0123456789ab",
		AppName:       "demo",
		DeploymentID:  "dep_1",
		ContainerPort: 8080,
		MemoryBytes:   256 << 20,
		CPUMilli:      500,
		Network:       network,
	}
}

func TestDockerRunPublishesLocallyAsNonRoot(t *testing.T) {
	t.Parallel()
	s := &script{user: "\n", port: "127.0.0.1:32768", id: "0123456789abcdef"}
	inst, err := testDocker(s).Start(context.Background(), testSpec(""))
	if err != nil {
		t.Fatal(err)
	}
	if inst.Endpoint != "http://127.0.0.1:32768" || inst.ID != s.id {
		t.Fatalf("instance %#v", inst)
	}
	cmds := s.commands()
	if len(cmds) != 3 || cmds[0].args[0] != "image" || cmds[1].args[0] != "run" || cmds[2].args[0] != "port" {
		t.Fatalf("commands %#v", cmds)
	}
	want := runArgs(testSpec(""), "")
	if strings.Join(cmds[1].args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("args =\n%s\nwant\n%s", strings.Join(cmds[1].args, " "), strings.Join(want, " "))
	}
	joined := strings.Join(want, " ")
	for _, piece := range []string{"--memory " + "268435456", "--cpus 0.50", "--user " + RunAsNonRootUser, "--env PORT=8080", "--publish 127.0.0.1::8080", "--pull never"} {
		if !strings.Contains(joined, piece) {
			t.Fatalf("args missing %q in %s", piece, joined)
		}
	}
}

func TestDockerRunKeepsImageUserOnNetwork(t *testing.T) {
	t.Parallel()
	s := &script{user: "nginx", id: "0123456789abcdef", inspectErr: errors.New("missing")}
	inst, err := testDocker(s).Start(context.Background(), testSpec("shipyard"))
	if err != nil {
		t.Fatal(err)
	}
	if inst.Endpoint != "http://shipyard-demo-dep_1:8080" {
		t.Fatalf("endpoint %s", inst.Endpoint)
	}
	cmds := s.commands()
	if len(cmds) != 4 || cmds[0].args[1] != "inspect" || cmds[1].args[1] != "create" || cmds[3].args[0] != "run" {
		t.Fatalf("commands %#v", cmds)
	}
	joined := strings.Join(cmds[3].args, " ")
	if strings.Contains(joined, "--user") || !strings.Contains(joined, "--network shipyard") || strings.Contains(joined, "--publish") {
		t.Fatalf("args %s", joined)
	}
}

func TestDockerStartRejectsBadInputWithoutRunning(t *testing.T) {
	t.Parallel()
	s := &script{}
	spec := testSpec("")
	spec.Image = "evil:latest"
	if _, err := testDocker(s).Start(context.Background(), spec); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("error %v", err)
	}
	spec = testSpec("")
	spec.Name = "-rm"
	if _, err := testDocker(s).Start(context.Background(), spec); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("name error %v", err)
	}
	if len(s.commands()) != 0 {
		t.Fatal("docker was invoked")
	}
}

func TestDockerMissingBinary(t *testing.T) {
	t.Parallel()
	d := &Docker{run: &script{}, look: func(string) error { return os.ErrNotExist }}
	_, err := d.Start(context.Background(), testSpec(""))
	if !errors.Is(err, ErrDockerNotFound) {
		t.Fatalf("error %v", err)
	}
	if err := d.Stop(context.Background(), "0123456789abcdef"); !errors.Is(err, ErrDockerNotFound) {
		t.Fatalf("stop error %v", err)
	}
}

func TestDockerStopStatusLogsAndReap(t *testing.T) {
	t.Parallel()
	id := "0123456789abcdef"
	s := &script{id: id, ps: id + "\n"}
	d := testDocker(s)
	if err := d.Stop(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	status, err := d.Status(context.Background(), "shipyard-demo-dep_1")
	if err != nil || !status.Running || status.ExitCode != 0 {
		t.Fatalf("status %#v %v", status, err)
	}
	logs, err := d.Logs(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(logs)
	_ = logs.Close()
	if err != nil || string(body) != "out\nerr\n" {
		t.Fatalf("logs %q %v", body, err)
	}
	if err := d.StopManaged(context.Background()); err != nil {
		t.Fatal(err)
	}
	cmds := s.commands()
	if cmds[0].args[0] != "rm" || cmds[1].args[0] != "inspect" || cmds[2].args[0] != "logs" || cmds[3].args[0] != "ps" || cmds[4].args[0] != "rm" {
		t.Fatalf("commands %#v", cmds)
	}
	if strings.Join(cmds[4].args, " ") != "rm -f "+id {
		t.Fatalf("reap %#v", cmds[4].args)
	}
	if err := d.Stop(context.Background(), "nope"); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("bad id error %v", err)
	}
}

func TestParsePublishedPort(t *testing.T) {
	t.Parallel()
	port, err := parsePublishedPort("0.0.0.0:32768\n127.0.0.1:32768\n")
	if err != nil || port != 32768 {
		t.Fatalf("port %d %v", port, err)
	}
	port, err = parsePublishedPort("[::]:4000\n")
	if err != nil || port != 4000 {
		t.Fatalf("ipv6 port %d %v", port, err)
	}
	if _, err := parsePublishedPort("nope"); err == nil {
		t.Fatal("expected an error")
	}
}

func TestImageUserAndCPU(t *testing.T) {
	t.Parallel()
	if imageRunsAsNonRoot("") || imageRunsAsNonRoot("root") || imageRunsAsNonRoot("0:0") {
		t.Fatal("root was treated as non-root")
	}
	if !imageRunsAsNonRoot("nginx") || !imageRunsAsNonRoot("101:101") {
		t.Fatal("non-root user was overridden")
	}
	if formatCPU(500) != "0.50" || formatCPU(1500) != "1.50" {
		t.Fatalf("cpu %s %s", formatCPU(500), formatCPU(1500))
	}
}

func TestDockerRemoveImage(t *testing.T) {
	t.Parallel()
	s := &script{}
	d := testDocker(s)
	tag := "shipyard/demo:0123456789ab"
	if err := d.RemoveImage(context.Background(), tag); err != nil {
		t.Fatal(err)
	}
	cmd := s.commands()[0]
	want := []string{"rmi", "--", tag}
	if cmd.name != "docker" || strings.Join(cmd.args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("command %#v", cmd)
	}
	if err := d.RemoveImage(context.Background(), "alpine:latest"); err == nil {
		t.Fatal("expected an invalid tag")
	}
}

func TestDockerReadyAndRestart(t *testing.T) {
	t.Parallel()
	s := &script{}
	d := testDocker(s)
	if err := d.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}
	id := "0123456789abcdef"
	if err := d.Restart(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if err := d.Restart(context.Background(), "nope"); err == nil {
		t.Fatal("expected an invalid id")
	}
	cmds := s.commands()
	if len(cmds) != 2 || cmds[0].args[0] != "info" || cmds[1].args[0] != "restart" || cmds[1].args[len(cmds[1].args)-1] != id {
		t.Fatalf("commands %#v", cmds)
	}
}

func TestParseStatus(t *testing.T) {
	t.Parallel()
	status, err := parseStatus("abc", "false|137|died\n")
	if err != nil || status.Running || status.ExitCode != 137 || status.Error != "died" {
		t.Fatalf("status %#v %v", status, err)
	}
	if _, err := parseStatus("abc", "nope"); err == nil {
		t.Fatal("expected an error")
	}
	var buf bytes.Buffer
	buf.WriteString(suffix(" boom "))
	if buf.String() != ": boom" {
		t.Fatalf("suffix %q", buf.String())
	}
}
