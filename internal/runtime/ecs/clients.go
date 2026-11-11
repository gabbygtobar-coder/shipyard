package ecs

import (
	"context"
	"errors"
	"log/slog"
)

const (
	statusMissing     = "MISSING"
	statusActive      = "ACTIVE"
	statusInactive    = "INACTIVE"
	rolloutCompleted  = "COMPLETED"
	rolloutFailed     = "FAILED"
	rolloutInProgress = "IN_PROGRESS"

	containerName  = "app"
	namePrefix     = "sy-"
	idPrefix       = "ecs/"
	activeRevision = "active"
	logLimit       = 1000
)

// errTargetGroupNotFound is returned when the ALB target group for an app
// was not created by Terraform.
var errTargetGroupNotFound = errors.New("target group not found")

// Auth is a short-lived ECR login. Password is a secret and must not be logged.
type Auth struct {
	// User is the registry username. ECR uses AWS.
	User string
	// Password is the registry password. It must not be logged or passed as an argument.
	Password string
	// Host is the registry host without a scheme.
	Host string
}

// TaskDefinition is one registered Fargate revision.
type TaskDefinition struct {
	// ARN is the task definition ARN, including the revision.
	ARN string
	// Family is the task definition family.
	Family string
	// Revision is the task definition revision.
	Revision int
	// Image is the container image URI.
	Image string
}

// TaskDefinitionInput is the subset of a Fargate task definition Shipyard sets.
type TaskDefinitionInput struct {
	// Family is the task definition family, one per app.
	Family string
	// CPU is the Fargate CPU value, such as 256.
	CPU string
	// Memory is the Fargate memory value in MiB, such as 512.
	Memory string
	// ExecutionRoleARN pulls the image and writes logs.
	ExecutionRoleARN string
	// TaskRoleARN is assumed by the container. It can be a role with no policies.
	TaskRoleARN string
	// ContainerName is the name used by the load balancer.
	ContainerName string
	// Image is the ECR image URI.
	Image string
	// ContainerPort is the port inside the task.
	ContainerPort int
	// LogGroup is the CloudWatch log group.
	LogGroup string
	// LogRegion is the region of the log group.
	LogRegion string
	// LogStreamPrefix is the awslogs stream prefix.
	LogStreamPrefix string
	// AppName is stored as a tag.
	AppName string
}

// ServiceInput is a create or update of one ECS service.
type ServiceInput struct {
	// Name is the ECS service name.
	Name string
	// Cluster is the ECS cluster name.
	Cluster string
	// TaskDefinition is the task definition ARN to run.
	TaskDefinition string
	// DesiredCount is the number of tasks. Zero stops the service.
	DesiredCount int
	// Subnets are the public subnets for the task network.
	Subnets []string
	// SecurityGroups are attached to the task.
	SecurityGroups []string
	// AssignPublicIP gives the task a public address so it can reach ECR without a NAT gateway.
	AssignPublicIP bool
	// TargetGroupARN receives the task.
	TargetGroupARN string
	// ContainerName must match the task definition.
	ContainerName string
	// ContainerPort is registered in the target group.
	ContainerPort int
	// AppName is stored as a tag.
	AppName string
}

// DeploymentState is the primary deployment of a service.
type DeploymentState struct {
	// Status is ACTIVE, INACTIVE, or MISSING.
	Status string
	// TaskDefinition is the task definition ARN the service is using.
	TaskDefinition string
	// Desired is the desired task count.
	Desired int
	// Running is the running task count.
	Running int
	// Pending is the pending task count.
	Pending int
	// Rollout is COMPLETED, IN_PROGRESS, or FAILED.
	Rollout string
	// FailedReason is set when Rollout is FAILED.
	FailedReason string
}

// Task is one ECS task, used for status and logs.
type Task struct {
	// ARN is the task ARN.
	ARN string
	// ID is the task id, the last path segment of ARN.
	ID string
	// LastStatus is the ECS last status, such as RUNNING.
	LastStatus string
	// ExitCode is the essential container exit code when the task has stopped.
	ExitCode int
	// Reason is why the task stopped, when ECS provided one.
	Reason string
}

// ImageStore is the ECR calls the runtime needs.
type ImageStore interface {
	Auth(ctx context.Context) (Auth, error)
	Delete(ctx context.Context, repository, tag string) error
}

// Cluster is the ECS calls the runtime needs.
type Cluster interface {
	RegisterTaskDefinition(ctx context.Context, in TaskDefinitionInput) (TaskDefinition, error)
	ListTaskDefinitions(ctx context.Context, family string) ([]TaskDefinition, error)
	DescribeService(ctx context.Context, cluster, name string) (DeploymentState, error)
	CreateService(ctx context.Context, in ServiceInput) error
	UpdateService(ctx context.Context, in ServiceInput) error
	ListTasks(ctx context.Context, cluster, service string) ([]Task, error)
}

// TargetGroups is the load balancer calls the runtime needs.
type TargetGroups interface {
	ARN(ctx context.Context, name string) (string, error)
	Healthy(ctx context.Context, arn string) (bool, error)
}

// LogReader reads a CloudWatch log stream.
type LogReader interface {
	Read(ctx context.Context, group, stream string, limit int) ([]byte, error)
}

// Pusher tags a local image and pushes it to a registry.
// Implementations must not put the registry password on the command line.
type Pusher interface {
	Push(ctx context.Context, localRef, remoteRef string, auth Auth) error
}

// Config is the AWS topology Terraform created.
type Config struct {
	// Region is the AWS region.
	Region string
	// Cluster is the ECS cluster name.
	Cluster string
	// Repository is the ECR repository URL without a scheme.
	Repository string
	// ExecutionRoleARN pulls images and writes logs.
	ExecutionRoleARN string
	// TaskRoleARN is assumed by the app container.
	TaskRoleARN string
	// Subnets are the public subnets for tasks.
	Subnets []string
	// SecurityGroups are attached to tasks.
	SecurityGroups []string
	// LogGroup is the CloudWatch log group name.
	LogGroup string
	// ALBDNSName is the load balancer DNS name without a scheme.
	ALBDNSName string
	// BaseDomain selects host rules when set. Empty selects path rules.
	BaseDomain string
	// Logger receives runtime errors. Nil discards them.
	Logger *slog.Logger
}

func (c Config) check() error {
	if c.Region == "" || c.Cluster == "" || c.Repository == "" {
		return errors.New("ecs region, cluster, and repository are required")
	}
	if _, _, err := parseRepository(c.Repository); err != nil {
		return err
	}
	if c.ExecutionRoleARN == "" || c.TaskRoleARN == "" {
		return errors.New("ecs execution role and task role are required")
	}
	if len(c.Subnets) < 2 {
		return errors.New("ecs requires two subnets")
	}
	if len(c.SecurityGroups) == 0 {
		return errors.New("ecs security group is required")
	}
	if c.LogGroup == "" || c.ALBDNSName == "" {
		return errors.New("ecs log group and load balancer DNS name are required")
	}
	return nil
}
