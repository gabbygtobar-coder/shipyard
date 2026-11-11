package ecs

import (
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gabbygtobar-coder/shipyard/internal/runtime"
)

const (
	testHost  = "123456789012.dkr.ecr.us-east-1.amazonaws.com"
	testRepo  = testHost + "/shipyard"
	testImage = "shipyard/demo:0123456789ab"
	testALB   = "shipyard-alb-123.us-east-1.elb.amazonaws.com"
)

func testECSConfig() Config {
	return Config{
		Region:           "us-east-1",
		Cluster:          "shipyard",
		Repository:       testRepo,
		ExecutionRoleARN: "arn:aws:iam::123456789012:role/shipyard-ecs-execution",
		TaskRoleARN:      "arn:aws:iam::123456789012:role/shipyard-ecs-task",
		Subnets:          []string{"subnet-0123456789abcdef0", "subnet-abcdef0123456789a"},
		SecurityGroups:   []string{"sg-0123456789abcdef0"},
		LogGroup:         "/shipyard/ecs",
		ALBDNSName:       testALB,
	}
}

func demoSpec() runtime.Spec {
	return runtime.Spec{
		Name:          "shipyard-demo-dep_1",
		Image:         testImage,
		AppName:       "demo",
		DeploymentID:  "dep_1",
		ContainerPort: 8080,
		MemoryBytes:   256 << 20,
		CPUMilli:      250,
	}
}

func remoteFor(local string) string {
	tag, err := ecrTag(local)
	if err != nil {
		panic(err)
	}
	return testRepo + ":" + tag
}

func taskARN(rev int) string {
	return "arn:aws:ecs:us-east-1:123456789012:task-definition/sy-demo:" + strconv.Itoa(rev)
}

type fakeCluster struct {
	defs      []TaskDefinition
	state     DeploymentState
	present   bool
	tasks     []Task
	registers int
	creates   int
	updates   []ServiceInput
	lastDef   TaskDefinitionInput
	rollout   string
	reason    string
}

func (f *fakeCluster) RegisterTaskDefinition(_ context.Context, in TaskDefinitionInput) (TaskDefinition, error) {
	f.registers++
	f.lastDef = in
	rev := 1
	for _, def := range f.defs {
		if def.Revision >= rev {
			rev = def.Revision + 1
		}
	}
	def := TaskDefinition{
		ARN:      "arn:aws:ecs:us-east-1:123456789012:task-definition/" + in.Family + ":" + strconv.Itoa(rev),
		Family:   in.Family,
		Revision: rev,
		Image:    in.Image,
	}
	f.defs = append([]TaskDefinition{def}, f.defs...)
	return def, nil
}

func (f *fakeCluster) ListTaskDefinitions(_ context.Context, family string) ([]TaskDefinition, error) {
	var out []TaskDefinition
	for _, def := range f.defs {
		if def.Family == family {
			out = append(out, def)
		}
	}
	return out, nil
}

func (f *fakeCluster) DescribeService(context.Context, string, string) (DeploymentState, error) {
	if !f.present {
		return DeploymentState{Status: statusMissing}, nil
	}
	return f.state, nil
}

func (f *fakeCluster) CreateService(_ context.Context, in ServiceInput) error {
	f.creates++
	f.present = true
	f.apply(in)
	return nil
}

func (f *fakeCluster) UpdateService(_ context.Context, in ServiceInput) error {
	f.updates = append(f.updates, in)
	f.present = true
	f.apply(in)
	return nil
}

func (f *fakeCluster) apply(in ServiceInput) {
	rollout := f.rollout
	if rollout == "" {
		rollout = rolloutCompleted
	}
	f.state = DeploymentState{
		Status:         statusActive,
		TaskDefinition: in.TaskDefinition,
		Desired:        in.DesiredCount,
		Running:        in.DesiredCount,
		Rollout:        rollout,
		FailedReason:   f.reason,
	}
}

func (f *fakeCluster) ListTasks(context.Context, string, string) ([]Task, error) {
	return f.tasks, nil
}

type fakeImages struct {
	auth    Auth
	deleted []string
}

func (f *fakeImages) Auth(context.Context) (Auth, error) {
	if f.auth.Host == "" {
		f.auth = Auth{User: "AWS", Password: "secret-token", Host: testHost}
	}
	return f.auth, nil
}

func (f *fakeImages) Delete(_ context.Context, repository, tag string) error {
	f.deleted = append(f.deleted, repository+":"+tag)
	return nil
}

type fakePush struct {
	calls  int
	local  string
	remote string
	auth   Auth
	err    error
}

func (f *fakePush) Push(_ context.Context, localRef, remoteRef string, auth Auth) error {
	f.calls++
	f.local = localRef
	f.remote = remoteRef
	f.auth = auth
	return f.err
}

type fakeTargets struct {
	missing bool
	healthy bool
	arn     string
}

func (f *fakeTargets) ARN(_ context.Context, name string) (string, error) {
	if f.missing {
		return "", errTargetGroupNotFound
	}
	if f.arn == "" {
		f.arn = "arn:aws:elasticloadbalancing:us-east-1:123:targetgroup/" + name + "/aabbcc"
	}
	return f.arn, nil
}

func (f *fakeTargets) Healthy(context.Context, string) (bool, error) {
	return f.healthy, nil
}

type fakeLogs struct {
	group  string
	stream string
	body   []byte
}

func (f *fakeLogs) Read(_ context.Context, group, stream string, _ int) ([]byte, error) {
	f.group = group
	f.stream = stream
	return f.body, nil
}

type runtimeFakes struct {
	rt      *Runtime
	cluster *fakeCluster
	images  *fakeImages
	push    *fakePush
	targets *fakeTargets
	logs    *fakeLogs
}

func newRuntimeFakes() runtimeFakes {
	f := runtimeFakes{
		cluster: &fakeCluster{},
		images:  &fakeImages{},
		push:    &fakePush{},
		targets: &fakeTargets{healthy: true},
		logs:    &fakeLogs{body: []byte("hello\n")},
	}
	f.rt = &Runtime{
		cfg:     testECSConfig(),
		images:  f.images,
		cluster: f.cluster,
		targets: f.targets,
		logs:    f.logs,
		push:    f.push,
		poll:    time.Millisecond,
	}
	return f
}

func TestStartCreatesService(t *testing.T) {
	t.Parallel()
	f := newRuntimeFakes()
	inst, err := f.rt.Start(context.Background(), demoSpec())
	if err != nil {
		t.Fatal(err)
	}
	if f.push.calls != 1 || f.push.local != testImage || f.push.remote != remoteFor(testImage) {
		t.Fatalf("push %+v", f.push)
	}
	if f.push.auth.Password != "secret-token" {
		t.Fatal("push did not receive the registry password")
	}
	if f.cluster.registers != 1 || f.cluster.creates != 1 || len(f.cluster.updates) != 0 {
		t.Fatalf("register %d create %d updates %d", f.cluster.registers, f.cluster.creates, len(f.cluster.updates))
	}
	if f.cluster.lastDef.CPU != "256" || f.cluster.lastDef.Memory != "512" {
		t.Fatalf("fargate size %s %s", f.cluster.lastDef.CPU, f.cluster.lastDef.Memory)
	}
	if f.cluster.lastDef.Image != remoteFor(testImage) || f.cluster.lastDef.ContainerPort != 8080 {
		t.Fatalf("task def %+v", f.cluster.lastDef)
	}
	if f.cluster.lastDef.ExecutionRoleARN != testECSConfig().ExecutionRoleARN || f.cluster.lastDef.LogGroup != "/shipyard/ecs" {
		t.Fatalf("roles %+v", f.cluster.lastDef)
	}
	if inst.ID != "ecs/demo/1" || inst.Endpoint != "http://"+testALB+"/apps/demo/" {
		t.Fatalf("instance %+v", inst)
	}
	if f.cluster.state.Desired != 1 || !strings.HasSuffix(f.cluster.state.TaskDefinition, ":1") {
		t.Fatalf("service state %+v", f.cluster.state)
	}
}

func TestStartUpdatesExistingService(t *testing.T) {
	t.Parallel()
	f := newRuntimeFakes()
	f.cluster.present = true
	f.cluster.state = DeploymentState{
		Status:         statusActive,
		TaskDefinition: taskARN(1),
		Desired:        1,
		Rollout:        rolloutCompleted,
	}
	f.cluster.defs = []TaskDefinition{{
		ARN: taskARN(1), Family: "sy-demo", Revision: 1, Image: remoteFor(testImage),
	}}
	spec := demoSpec()
	spec.Image = "shipyard/demo:abcdefabcdef"
	spec.DeploymentID = "dep_2"
	spec.Name = "shipyard-demo-dep_2"
	inst, err := f.rt.Start(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if f.cluster.creates != 0 || f.cluster.registers != 1 || len(f.cluster.updates) != 1 {
		t.Fatalf("create %d register %d updates %d", f.cluster.creates, f.cluster.registers, len(f.cluster.updates))
	}
	got := f.cluster.updates[0]
	if !got.AssignPublicIP || got.DesiredCount != 1 || got.TargetGroupARN == "" {
		t.Fatalf("update %+v", got)
	}
	if len(got.Subnets) != 2 || got.SecurityGroups[0] != "sg-0123456789abcdef0" {
		t.Fatalf("network %+v", got)
	}
	if inst.ID != "ecs/demo/2" {
		t.Fatalf("id %s", inst.ID)
	}
}

func TestRollbackRepointsPreviousTaskDefinition(t *testing.T) {
	t.Parallel()
	f := newRuntimeFakes()
	oldImage := "shipyard/demo:aaaaaaaaaaaa"
	newImage := "shipyard/demo:bbbbbbbbbbbb"
	f.cluster.defs = []TaskDefinition{
		{ARN: taskARN(2), Family: "sy-demo", Revision: 2, Image: remoteFor(newImage)},
		{ARN: taskARN(1), Family: "sy-demo", Revision: 1, Image: remoteFor(oldImage)},
	}
	f.cluster.present = true
	f.cluster.state = DeploymentState{
		Status:         statusActive,
		TaskDefinition: taskARN(2),
		Desired:        1,
		Rollout:        rolloutCompleted,
	}
	spec := demoSpec()
	spec.Image = oldImage
	inst, err := f.rt.Start(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if f.cluster.registers != 0 {
		t.Fatalf("registered %d task definitions, want a re-point", f.cluster.registers)
	}
	if len(f.cluster.updates) != 1 || f.cluster.updates[0].TaskDefinition != taskARN(1) || f.cluster.updates[0].DesiredCount != 1 {
		t.Fatalf("updates %+v", f.cluster.updates)
	}
	if inst.ID != "ecs/demo/1" {
		t.Fatalf("id %s", inst.ID)
	}
}

func TestStartRollsBackWhenDeploymentFails(t *testing.T) {
	t.Parallel()
	f := newRuntimeFakes()
	f.cluster.present = true
	f.cluster.state = DeploymentState{
		Status:         statusActive,
		TaskDefinition: taskARN(1),
		Desired:        1,
		Rollout:        rolloutCompleted,
	}
	f.cluster.defs = []TaskDefinition{{
		ARN: taskARN(1), Family: "sy-demo", Revision: 1, Image: remoteFor(testImage),
	}}
	f.cluster.rollout = rolloutFailed
	f.cluster.reason = "tasks failed health checks"
	spec := demoSpec()
	spec.Image = "shipyard/demo:bbbbbbbbbbbb"
	_, err := f.rt.Start(context.Background(), spec)
	if !errors.Is(err, runtime.ErrUnhealthy) {
		t.Fatalf("error %v", err)
	}
	if len(f.cluster.updates) != 2 {
		t.Fatalf("updates %+v", f.cluster.updates)
	}
	if f.cluster.updates[1].TaskDefinition != taskARN(1) || f.cluster.updates[1].DesiredCount != 1 {
		t.Fatalf("rollback %+v", f.cluster.updates[1])
	}
}

func TestStartScalesDownWhenFirstDeployFails(t *testing.T) {
	t.Parallel()
	f := newRuntimeFakes()
	f.cluster.rollout = rolloutFailed
	_, err := f.rt.Start(context.Background(), demoSpec())
	if !errors.Is(err, runtime.ErrUnhealthy) {
		t.Fatalf("error %v", err)
	}
	if f.cluster.creates != 1 || len(f.cluster.updates) != 1 || f.cluster.updates[0].DesiredCount != 0 {
		t.Fatalf("create %d updates %+v", f.cluster.creates, f.cluster.updates)
	}
}

func TestStartTimesOutAndRollsBack(t *testing.T) {
	t.Parallel()
	f := newRuntimeFakes()
	f.cluster.rollout = rolloutInProgress
	f.targets.healthy = false
	f.rt.poll = time.Hour
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := f.rt.Start(ctx, demoSpec())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error %v", err)
	}
	if len(f.cluster.updates) != 1 || f.cluster.updates[0].DesiredCount != 0 {
		t.Fatalf("updates %+v", f.cluster.updates)
	}
}

func TestStartRejectsBadSpecBeforeAWS(t *testing.T) {
	t.Parallel()
	f := newRuntimeFakes()
	spec := demoSpec()
	spec.Image = "library/nginx:latest"
	if _, err := f.rt.Start(context.Background(), spec); !errors.Is(err, runtime.ErrInvalidSpec) {
		t.Fatalf("error %v", err)
	}
	if f.push.calls != 0 || f.cluster.registers != 0 {
		t.Fatal("invalid spec reached AWS")
	}
}

func TestStartRequiresMatchingRegistryHost(t *testing.T) {
	t.Parallel()
	f := newRuntimeFakes()
	f.images.auth = Auth{User: "AWS", Password: "secret-token", Host: "other.example.com"}
	_, err := f.rt.Start(context.Background(), demoSpec())
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("error %v", err)
	}
	if f.push.calls != 0 {
		t.Fatal("pushed with the wrong registry")
	}
}

func TestStartMissingTargetGroup(t *testing.T) {
	t.Parallel()
	f := newRuntimeFakes()
	f.targets.missing = true
	_, err := f.rt.Start(context.Background(), demoSpec())
	if err == nil || !strings.Contains(err.Error(), "app_names") {
		t.Fatalf("error %v", err)
	}
	if f.push.calls != 0 {
		t.Fatal("pushed without a target group")
	}
}

func TestStartInactiveService(t *testing.T) {
	t.Parallel()
	f := newRuntimeFakes()
	f.cluster.present = true
	f.cluster.state = DeploymentState{Status: statusInactive, TaskDefinition: taskARN(1)}
	_, err := f.rt.Start(context.Background(), demoSpec())
	if err == nil || !strings.Contains(err.Error(), "being deleted") {
		t.Fatalf("error %v", err)
	}
	if f.cluster.creates != 0 {
		t.Fatal("created a service that is still deleting")
	}
}

func TestStopScalesActiveServiceToZero(t *testing.T) {
	t.Parallel()
	f := newRuntimeFakes()
	f.cluster.present = true
	f.cluster.state = DeploymentState{Status: statusActive, TaskDefinition: taskARN(3), Desired: 1}
	if err := f.rt.Stop(context.Background(), "ecs/demo/3"); err != nil {
		t.Fatal(err)
	}
	if len(f.cluster.updates) != 1 || f.cluster.updates[0].DesiredCount != 0 {
		t.Fatalf("updates %+v", f.cluster.updates)
	}
	f.cluster.updates = nil
	f.cluster.state.Desired = 0
	if err := f.rt.Stop(context.Background(), activeInstanceID("demo")); err != nil {
		t.Fatal(err)
	}
	if len(f.cluster.updates) != 0 {
		t.Fatalf("second stop updated %+v", f.cluster.updates)
	}
}

func TestStopInactiveRevisionIsNoop(t *testing.T) {
	t.Parallel()
	f := newRuntimeFakes()
	f.cluster.present = true
	f.cluster.state = DeploymentState{Status: statusActive, TaskDefinition: taskARN(2), Desired: 1}
	if err := f.rt.Stop(context.Background(), "ecs/demo/1"); err != nil {
		t.Fatal(err)
	}
	if len(f.cluster.updates) != 0 {
		t.Fatalf("updates %+v", f.cluster.updates)
	}
}

func TestStopMissingService(t *testing.T) {
	t.Parallel()
	f := newRuntimeFakes()
	if err := f.rt.Stop(context.Background(), "ecs/demo/active"); err != nil {
		t.Fatal(err)
	}
}

func TestStopRejectsDockerID(t *testing.T) {
	t.Parallel()
	f := newRuntimeFakes()
	if err := f.rt.Stop(context.Background(), "abcdef123456"); !errors.Is(err, runtime.ErrInvalidSpec) {
		t.Fatalf("error %v", err)
	}
}

func TestStatusAndLogs(t *testing.T) {
	t.Parallel()
	f := newRuntimeFakes()
	f.cluster.tasks = []Task{{ID: "task123", LastStatus: "RUNNING"}}
	status, err := f.rt.Status(context.Background(), "ecs/demo/1")
	if err != nil || !status.Running || status.ID != "ecs/demo/1" {
		t.Fatalf("status %+v %v", status, err)
	}
	body, err := f.rt.Logs(context.Background(), "ecs/demo/1")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = body.Close() }()
	got, err := io.ReadAll(body)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello\n" || f.logs.stream != "demo/app/task123" || f.logs.group != "/shipyard/ecs" {
		t.Fatalf("logs %q stream %s", got, f.logs.stream)
	}

	f.cluster.tasks = []Task{{ID: "task123", LastStatus: "STOPPED", ExitCode: 1, Reason: "exited"}}
	status, err = f.rt.Status(context.Background(), "ecs/demo/1")
	if err != nil || status.Running || status.ExitCode != 1 || status.Error != "exited" {
		t.Fatalf("stopped %+v %v", status, err)
	}
}

func TestRemoveImage(t *testing.T) {
	t.Parallel()
	f := newRuntimeFakes()
	if err := f.rt.RemoveImage(context.Background(), testImage); err != nil {
		t.Fatal(err)
	}
	if len(f.images.deleted) != 1 || f.images.deleted[0] != "shipyard:demo-0123456789ab" {
		t.Fatalf("deleted %+v", f.images.deleted)
	}
	if err := f.rt.RemoveImage(context.Background(), "nginx:latest"); !errors.Is(err, runtime.ErrInvalidSpec) {
		t.Fatalf("error %v", err)
	}
}

func TestPublicURL(t *testing.T) {
	t.Parallel()
	got, err := PublicURL(testALB, "", "demo")
	if err != nil || got != "http://"+testALB+"/apps/demo/" {
		t.Fatalf("path %s %v", got, err)
	}
	got, err = PublicURL(testALB, "example.com", "demo")
	if err != nil || got != "http://demo.example.com/" {
		t.Fatalf("host %s %v", got, err)
	}
	if _, err := PublicURL("", "", "nope!"); !errors.Is(err, runtime.ErrInvalidSpec) {
		t.Fatalf("error %v", err)
	}
}

func TestAppNameTooLong(t *testing.T) {
	t.Parallel()
	f := newRuntimeFakes()
	spec := demoSpec()
	spec.AppName = "abcdefghijklmnopqrstuvwxyzabcd"
	spec.Name = "shipyard-" + spec.AppName + "-dep_1"
	spec.Image = "shipyard/" + spec.AppName + ":0123456789ab"
	_, err := f.rt.Start(context.Background(), spec)
	if err == nil || !strings.Contains(err.Error(), "too long") {
		t.Fatalf("error %v", err)
	}
}

func TestParseInstanceID(t *testing.T) {
	t.Parallel()
	app, rev, active, err := parseInstanceID("ecs/demo/4")
	if err != nil || app != "demo" || rev != 4 || active {
		t.Fatalf("%s %d %v %v", app, rev, active, err)
	}
	_, _, active, err = parseInstanceID("ecs/demo/active")
	if err != nil || !active {
		t.Fatalf("active %v %v", active, err)
	}
	if _, _, _, err := parseInstanceID("ecs/Demo/1"); !errors.Is(err, runtime.ErrInvalidSpec) {
		t.Fatalf("error %v", err)
	}
}
