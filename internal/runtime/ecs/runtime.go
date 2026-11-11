// Package ecs runs a built image on ECS Fargate behind the Terraform load balancer.
//
// AWS API calls go through the interfaces in this package. Tests use fakes and
// do not open a network connection.
package ecs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/gabbygtobar-coder/shipyard/internal/build"
	"github.com/gabbygtobar-coder/shipyard/internal/runtime"
)

// Runtime implements runtime.Runtime with ECR, ECS, and the ALB target group.
type Runtime struct {
	cfg     Config
	images  ImageStore
	cluster Cluster
	targets TargetGroups
	logs    LogReader
	push    Pusher
	poll    time.Duration
	log     *slog.Logger
}

// Start pushes the local image to ECR, registers a task definition when this
// image does not already have one, and creates or updates the ECS service.
// A deploy of an image that already has a task definition re-points the
// service at that revision. Start returns when the deployment is complete and
// a target is healthy, or rolls the service back and returns an error.
func (r *Runtime) Start(ctx context.Context, spec runtime.Spec) (runtime.Instance, error) {
	if err := ctx.Err(); err != nil {
		return runtime.Instance{}, err
	}
	if err := r.ready(); err != nil {
		return runtime.Instance{}, err
	}
	if err := runtime.CheckSpec(spec); err != nil {
		return runtime.Instance{}, err
	}
	if !strings.HasPrefix(spec.Image, "shipyard/"+spec.AppName+":") {
		return runtime.Instance{}, runtime.ErrInvalidSpec
	}
	name, err := serviceName(spec.AppName)
	if err != nil {
		return runtime.Instance{}, err
	}
	remote, err := remoteImage(r.cfg.Repository, spec.Image)
	if err != nil {
		return runtime.Instance{}, err
	}
	endpoint, err := PublicURL(r.cfg.ALBDNSName, r.cfg.BaseDomain, spec.AppName)
	if err != nil {
		return runtime.Instance{}, err
	}
	cpu, memory, err := fargateResources(spec.CPUMilli, spec.MemoryBytes)
	if err != nil {
		return runtime.Instance{}, err
	}
	groupARN, err := r.targets.ARN(ctx, name)
	if err != nil {
		if errors.Is(err, errTargetGroupNotFound) {
			return runtime.Instance{}, fmt.Errorf("target group %s was not found; add %s to app_names in infra/aws and run make aws-up", name, spec.AppName)
		}
		return runtime.Instance{}, err
	}

	auth, err := r.images.Auth(ctx)
	if err != nil {
		return runtime.Instance{}, fmt.Errorf("ecr auth: %w", err)
	}
	host, _, err := parseRepository(r.cfg.Repository)
	if err != nil {
		return runtime.Instance{}, err
	}
	if auth.Host != host {
		return runtime.Instance{}, fmt.Errorf("ecr authorization host does not match the repository")
	}
	if err := r.push.Push(ctx, spec.Image, remote, auth); err != nil {
		return runtime.Instance{}, fmt.Errorf("push image: %w", err)
	}

	def, created, err := r.taskDefinition(ctx, spec, name, remote, cpu, memory)
	if err != nil {
		return runtime.Instance{}, err
	}
	if created {
		r.logger().Info("registered task definition", "app", spec.AppName, "revision", def.Revision)
	}

	state, err := r.cluster.DescribeService(ctx, r.cfg.Cluster, name)
	if err != nil {
		return runtime.Instance{}, err
	}
	if state.Status == statusInactive {
		return runtime.Instance{}, fmt.Errorf("ecs service %s is being deleted", name)
	}
	input := r.serviceInput(name, def.ARN, groupARN, spec, 1)
	previous := ""
	if state.Status == statusMissing {
		if err := r.cluster.CreateService(ctx, input); err != nil {
			return runtime.Instance{}, fmt.Errorf("create service: %w", err)
		}
	} else {
		previous = state.TaskDefinition
		if err := r.cluster.UpdateService(ctx, input); err != nil {
			return runtime.Instance{}, fmt.Errorf("update service: %w", err)
		}
	}

	if err := r.waitHealthy(ctx, name, groupARN); err != nil {
		r.rollBack(ctx, input, previous, def.ARN)
		return runtime.Instance{}, err
	}
	return runtime.Instance{
		ID:           instanceID(spec.AppName, def.Revision),
		Name:         spec.Name,
		AppName:      spec.AppName,
		DeploymentID: spec.DeploymentID,
		Endpoint:     endpoint,
	}, nil
}

// Stop scales the service to zero when id is the active revision.
// Stopping an older revision does nothing, because ECS has already replaced it.
func (r *Runtime) Stop(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	app, revision, active, err := parseInstanceID(id)
	if err != nil {
		return err
	}
	name, err := serviceName(app)
	if err != nil {
		return err
	}
	state, err := r.cluster.DescribeService(ctx, r.cfg.Cluster, name)
	if err != nil {
		return err
	}
	if state.Status == statusMissing || state.Status == statusInactive || state.Desired == 0 {
		return nil
	}
	current := revisionOfARN(state.TaskDefinition)
	if !active && current != revision {
		return nil
	}
	input := ServiceInput{
		Name:           name,
		Cluster:        r.cfg.Cluster,
		TaskDefinition: state.TaskDefinition,
		DesiredCount:   0,
		Subnets:        r.cfg.Subnets,
		SecurityGroups: r.cfg.SecurityGroups,
		AssignPublicIP: true,
		ContainerName:  containerName,
		AppName:        app,
	}
	if err := r.cluster.UpdateService(ctx, input); err != nil {
		return fmt.Errorf("stop service: %w", err)
	}
	return nil
}

// Status reports whether a task for the service is running.
func (r *Runtime) Status(ctx context.Context, id string) (runtime.ContainerStatus, error) {
	if err := ctx.Err(); err != nil {
		return runtime.ContainerStatus{}, err
	}
	app, _, _, err := parseInstanceID(id)
	if err != nil {
		return runtime.ContainerStatus{}, err
	}
	name, err := serviceName(app)
	if err != nil {
		return runtime.ContainerStatus{}, err
	}
	tasks, err := r.cluster.ListTasks(ctx, r.cfg.Cluster, name)
	if err != nil {
		return runtime.ContainerStatus{}, err
	}
	status := runtime.ContainerStatus{ID: id}
	for _, task := range tasks {
		if task.LastStatus == "RUNNING" {
			status.Running = true
			return status, nil
		}
	}
	if len(tasks) > 0 {
		status.ExitCode = tasks[0].ExitCode
		status.Error = tasks[0].Reason
	}
	return status, nil
}

// Logs returns a snapshot of the newest task's CloudWatch log.
func (r *Runtime) Logs(ctx context.Context, id string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	app, _, _, err := parseInstanceID(id)
	if err != nil {
		return nil, err
	}
	name, err := serviceName(app)
	if err != nil {
		return nil, err
	}
	tasks, err := r.cluster.ListTasks(ctx, r.cfg.Cluster, name)
	if err != nil {
		return nil, err
	}
	if len(tasks) == 0 || tasks[0].ID == "" {
		return io.NopCloser(strings.NewReader("")), nil
	}
	stream := app + "/" + containerName + "/" + tasks[0].ID
	body, err := r.logs.Read(ctx, r.cfg.LogGroup, stream, logLimit)
	if err != nil {
		return nil, err
	}
	if body == nil {
		body = []byte{}
	}
	return io.NopCloser(strings.NewReader(string(body))), nil
}

// RemoveImage deletes the ECR tag for a local shipyard image that is no longer retained.
func (r *Runtime) RemoveImage(ctx context.Context, tag string) error {
	if err := build.ValidateTag(tag); err != nil {
		return runtime.ErrInvalidSpec
	}
	_, name, err := parseRepository(r.cfg.Repository)
	if err != nil {
		return err
	}
	remote, err := ecrTag(tag)
	if err != nil {
		return err
	}
	return r.images.Delete(ctx, name, remote)
}

func (r *Runtime) taskDefinition(ctx context.Context, spec runtime.Spec, family, image, cpu, memory string) (TaskDefinition, bool, error) {
	existing, err := r.cluster.ListTaskDefinitions(ctx, family)
	if err != nil {
		return TaskDefinition{}, false, err
	}
	for _, def := range existing {
		if def.Family == family && def.Image == image && def.ARN != "" {
			return def, false, nil
		}
	}
	def, err := r.cluster.RegisterTaskDefinition(ctx, TaskDefinitionInput{
		Family:           family,
		CPU:              cpu,
		Memory:           memory,
		ExecutionRoleARN: r.cfg.ExecutionRoleARN,
		TaskRoleARN:      r.cfg.TaskRoleARN,
		ContainerName:    containerName,
		Image:            image,
		ContainerPort:    spec.ContainerPort,
		LogGroup:         r.cfg.LogGroup,
		LogRegion:        r.cfg.Region,
		LogStreamPrefix:  spec.AppName,
		AppName:          spec.AppName,
	})
	if err != nil {
		return TaskDefinition{}, false, fmt.Errorf("register task definition: %w", err)
	}
	return def, true, nil
}

func (r *Runtime) serviceInput(name, taskDef, groupARN string, spec runtime.Spec, desired int) ServiceInput {
	return ServiceInput{
		Name:           name,
		Cluster:        r.cfg.Cluster,
		TaskDefinition: taskDef,
		DesiredCount:   desired,
		Subnets:        append([]string(nil), r.cfg.Subnets...),
		SecurityGroups: append([]string(nil), r.cfg.SecurityGroups...),
		AssignPublicIP: true,
		TargetGroupARN: groupARN,
		ContainerName:  containerName,
		ContainerPort:  spec.ContainerPort,
		AppName:        spec.AppName,
	}
}

func (r *Runtime) waitHealthy(ctx context.Context, service, groupARN string) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		state, err := r.cluster.DescribeService(ctx, r.cfg.Cluster, service)
		if err != nil {
			return err
		}
		if state.Rollout == rolloutFailed {
			reason := state.FailedReason
			if reason == "" {
				reason = "deployment failed"
			}
			return fmt.Errorf("%w: %s", runtime.ErrUnhealthy, reason)
		}
		healthy, err := r.targets.Healthy(ctx, groupARN)
		if err != nil {
			return err
		}
		if state.Rollout == rolloutCompleted && healthy {
			return nil
		}
		timer := time.NewTimer(r.interval())
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (r *Runtime) rollBack(ctx context.Context, current ServiceInput, previousARN, failedARN string) {
	rolled := current
	if previousARN != "" && previousARN != failedARN {
		rolled.TaskDefinition = previousARN
		rolled.DesiredCount = 1
	} else {
		rolled.TaskDefinition = failedARN
		rolled.DesiredCount = 0
	}
	rbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := r.cluster.UpdateService(rbCtx, rolled); err != nil {
		r.logger().Error("roll back service", "service", current.Name, "err", err)
	}
}

// Ready reports whether the ECS runtime has its clients and config.
// It does not call AWS. A failed AWS call surfaces on the next deploy.
func (r *Runtime) Ready(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return r.ready()
}

func (r *Runtime) ready() error {
	if r.images == nil || r.cluster == nil || r.targets == nil || r.logs == nil || r.push == nil {
		return errors.New("ecs runtime is not configured")
	}
	return r.cfg.check()
}

func (r *Runtime) interval() time.Duration {
	if r.poll > 0 {
		return r.poll
	}
	return 5 * time.Second
}

func (r *Runtime) logger() *slog.Logger {
	if r.log != nil {
		return r.log
	}
	if r.cfg.Logger != nil {
		return r.cfg.Logger
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

var _ runtime.Runtime = (*Runtime)(nil)
