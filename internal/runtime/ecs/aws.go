package ecs

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	awscwl "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	awsecr "github.com/aws/aws-sdk-go-v2/service/ecr"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"
	awsecs "github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	awselb "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	smithy "github.com/aws/smithy-go"
)

// New returns a runtime that uses the default AWS credential chain.
// It does not call AWS until a deploy, stop, or log read.
func New(cfg Config) (*Runtime, error) {
	if err := cfg.check(); err != nil {
		return nil, err
	}
	awsCfg, err := awscfg.LoadDefaultConfig(context.Background(), awscfg.WithRegion(cfg.Region))
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}
	return &Runtime{
		cfg:     cfg,
		images:  &ecrClient{api: awsecr.NewFromConfig(awsCfg)},
		cluster: &ecsClient{api: awsecs.NewFromConfig(awsCfg)},
		targets: &elbClient{api: awselb.NewFromConfig(awsCfg)},
		logs:    &logClient{api: awscwl.NewFromConfig(awsCfg)},
		push: dockerPusher{
			run: osCommand{},
			look: func(name string) error {
				_, lookErr := exec.LookPath(name)
				return lookErr
			},
		},
		poll: 5 * time.Second,
		log:  cfg.Logger,
	}, nil
}

type ecrAPI interface {
	GetAuthorizationToken(context.Context, *awsecr.GetAuthorizationTokenInput, ...func(*awsecr.Options)) (*awsecr.GetAuthorizationTokenOutput, error)
	BatchDeleteImage(context.Context, *awsecr.BatchDeleteImageInput, ...func(*awsecr.Options)) (*awsecr.BatchDeleteImageOutput, error)
}

type ecsAPI interface {
	RegisterTaskDefinition(context.Context, *awsecs.RegisterTaskDefinitionInput, ...func(*awsecs.Options)) (*awsecs.RegisterTaskDefinitionOutput, error)
	ListTaskDefinitions(context.Context, *awsecs.ListTaskDefinitionsInput, ...func(*awsecs.Options)) (*awsecs.ListTaskDefinitionsOutput, error)
	DescribeTaskDefinition(context.Context, *awsecs.DescribeTaskDefinitionInput, ...func(*awsecs.Options)) (*awsecs.DescribeTaskDefinitionOutput, error)
	DescribeServices(context.Context, *awsecs.DescribeServicesInput, ...func(*awsecs.Options)) (*awsecs.DescribeServicesOutput, error)
	CreateService(context.Context, *awsecs.CreateServiceInput, ...func(*awsecs.Options)) (*awsecs.CreateServiceOutput, error)
	UpdateService(context.Context, *awsecs.UpdateServiceInput, ...func(*awsecs.Options)) (*awsecs.UpdateServiceOutput, error)
	ListTasks(context.Context, *awsecs.ListTasksInput, ...func(*awsecs.Options)) (*awsecs.ListTasksOutput, error)
	DescribeTasks(context.Context, *awsecs.DescribeTasksInput, ...func(*awsecs.Options)) (*awsecs.DescribeTasksOutput, error)
}

type elbAPI interface {
	DescribeTargetGroups(context.Context, *awselb.DescribeTargetGroupsInput, ...func(*awselb.Options)) (*awselb.DescribeTargetGroupsOutput, error)
	DescribeTargetHealth(context.Context, *awselb.DescribeTargetHealthInput, ...func(*awselb.Options)) (*awselb.DescribeTargetHealthOutput, error)
}

type logAPI interface {
	GetLogEvents(context.Context, *awscwl.GetLogEventsInput, ...func(*awscwl.Options)) (*awscwl.GetLogEventsOutput, error)
}

type ecrClient struct{ api ecrAPI }
type ecsClient struct{ api ecsAPI }
type elbClient struct{ api elbAPI }
type logClient struct{ api logAPI }

func (c *ecrClient) Auth(ctx context.Context) (Auth, error) {
	out, err := c.api.GetAuthorizationToken(ctx, &awsecr.GetAuthorizationTokenInput{})
	if err != nil {
		return Auth{}, err
	}
	if out == nil || len(out.AuthorizationData) == 0 || out.AuthorizationData[0].AuthorizationToken == nil {
		return Auth{}, fmt.Errorf("ecr returned no authorization token")
	}
	raw, err := base64.StdEncoding.DecodeString(aws.ToString(out.AuthorizationData[0].AuthorizationToken))
	if err != nil {
		return Auth{}, fmt.Errorf("decode ecr authorization token: %w", err)
	}
	user, password, ok := strings.Cut(string(raw), ":")
	if !ok || user == "" || password == "" {
		return Auth{}, fmt.Errorf("ecr authorization token was not a user and password")
	}
	host, err := registryHost(aws.ToString(out.AuthorizationData[0].ProxyEndpoint))
	if err != nil {
		return Auth{}, err
	}
	return Auth{User: user, Password: password, Host: host}, nil
}

func (c *ecrClient) Delete(ctx context.Context, repository, tag string) error {
	out, err := c.api.BatchDeleteImage(ctx, &awsecr.BatchDeleteImageInput{
		RepositoryName: aws.String(repository),
		ImageIds:       []ecrtypes.ImageIdentifier{{ImageTag: aws.String(tag)}},
	})
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	for _, failure := range out.Failures {
		code := string(failure.FailureCode)
		if code == "" || code == "ImageNotFound" {
			continue
		}
		return fmt.Errorf("delete image: %s", aws.ToString(failure.FailureReason))
	}
	return nil
}

func (c *ecsClient) RegisterTaskDefinition(ctx context.Context, in TaskDefinitionInput) (TaskDefinition, error) {
	port, err := port32(in.ContainerPort)
	if err != nil {
		return TaskDefinition{}, err
	}
	out, err := c.api.RegisterTaskDefinition(ctx, &awsecs.RegisterTaskDefinitionInput{
		Family:                  aws.String(in.Family),
		Cpu:                     aws.String(in.CPU),
		Memory:                  aws.String(in.Memory),
		NetworkMode:             ecstypes.NetworkModeAwsvpc,
		RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
		ExecutionRoleArn:        aws.String(in.ExecutionRoleARN),
		TaskRoleArn:             aws.String(in.TaskRoleARN),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			Name:      aws.String(in.ContainerName),
			Image:     aws.String(in.Image),
			Essential: aws.Bool(true),
			PortMappings: []ecstypes.PortMapping{{
				ContainerPort: aws.Int32(port),
				Protocol:      ecstypes.TransportProtocolTcp,
			}},
			Environment: []ecstypes.KeyValuePair{{
				Name:  aws.String("PORT"),
				Value: aws.String(fmt.Sprintf("%d", in.ContainerPort)),
			}},
			LogConfiguration: &ecstypes.LogConfiguration{
				LogDriver: ecstypes.LogDriverAwslogs,
				Options: map[string]string{
					"awslogs-group":         in.LogGroup,
					"awslogs-region":        in.LogRegion,
					"awslogs-stream-prefix": in.LogStreamPrefix,
				},
			},
		}},
		Tags: serviceTags(in.AppName),
	})
	if err != nil {
		return TaskDefinition{}, err
	}
	if out == nil || out.TaskDefinition == nil {
		return TaskDefinition{}, fmt.Errorf("ecs returned no task definition")
	}
	return taskDefinitionFrom(out.TaskDefinition, in.ContainerName), nil
}

func (c *ecsClient) ListTaskDefinitions(ctx context.Context, family string) ([]TaskDefinition, error) {
	out, err := c.api.ListTaskDefinitions(ctx, &awsecs.ListTaskDefinitionsInput{
		FamilyPrefix: aws.String(family),
		Sort:         ecstypes.SortOrderDesc,
		MaxResults:   aws.Int32(20),
	})
	if err != nil {
		return nil, err
	}
	if out == nil {
		return nil, nil
	}
	defs := make([]TaskDefinition, 0, len(out.TaskDefinitionArns))
	for _, arn := range out.TaskDefinitionArns {
		described, err := c.api.DescribeTaskDefinition(ctx, &awsecs.DescribeTaskDefinitionInput{
			TaskDefinition: aws.String(arn),
		})
		if err != nil {
			return nil, err
		}
		if described == nil || described.TaskDefinition == nil {
			continue
		}
		def := taskDefinitionFrom(described.TaskDefinition, containerName)
		if def.Family != family {
			continue
		}
		defs = append(defs, def)
	}
	return defs, nil
}

func (c *ecsClient) DescribeService(ctx context.Context, cluster, name string) (DeploymentState, error) {
	out, err := c.api.DescribeServices(ctx, &awsecs.DescribeServicesInput{
		Cluster:  aws.String(cluster),
		Services: []string{name},
	})
	if err != nil {
		return DeploymentState{}, err
	}
	if out == nil {
		return DeploymentState{Status: statusMissing}, nil
	}
	for _, failure := range out.Failures {
		if aws.ToString(failure.Reason) == statusMissing {
			return DeploymentState{Status: statusMissing}, nil
		}
	}
	if len(out.Services) == 0 {
		return DeploymentState{Status: statusMissing}, nil
	}
	svc := out.Services[0]
	state := DeploymentState{
		Status:         aws.ToString(svc.Status),
		TaskDefinition: aws.ToString(svc.TaskDefinition),
		Desired:        int(svc.DesiredCount),
		Running:        int(svc.RunningCount),
		Pending:        int(svc.PendingCount),
	}
	for _, deployment := range svc.Deployments {
		if aws.ToString(deployment.Status) != "PRIMARY" {
			continue
		}
		state.Rollout = string(deployment.RolloutState)
		state.FailedReason = aws.ToString(deployment.RolloutStateReason)
		if state.TaskDefinition == "" {
			state.TaskDefinition = aws.ToString(deployment.TaskDefinition)
		}
	}
	return state, nil
}

func (c *ecsClient) CreateService(ctx context.Context, in ServiceInput) error {
	input, err := serviceRequest(in)
	if err != nil {
		return err
	}
	_, err = c.api.CreateService(ctx, input)
	return err
}

func (c *ecsClient) UpdateService(ctx context.Context, in ServiceInput) error {
	port, err := port32(in.ContainerPort)
	if err != nil && in.ContainerPort != 0 {
		return err
	}
	input := &awsecs.UpdateServiceInput{
		Cluster:        aws.String(in.Cluster),
		Service:        aws.String(in.Name),
		TaskDefinition: aws.String(in.TaskDefinition),
		DesiredCount:   aws.Int32(int32(in.DesiredCount)),
		NetworkConfiguration: &ecstypes.NetworkConfiguration{
			AwsvpcConfiguration: vpcConfig(in),
		},
	}
	if in.TargetGroupARN != "" && in.ContainerPort != 0 {
		input.LoadBalancers = []ecstypes.LoadBalancer{{
			TargetGroupArn: aws.String(in.TargetGroupARN),
			ContainerName:  aws.String(in.ContainerName),
			ContainerPort:  aws.Int32(port),
		}}
	}
	_, err = c.api.UpdateService(ctx, input)
	return err
}

func (c *ecsClient) ListTasks(ctx context.Context, cluster, service string) ([]Task, error) {
	arns, err := c.taskARNs(ctx, cluster, service, ecstypes.DesiredStatusRunning)
	if err != nil {
		return nil, err
	}
	if len(arns) == 0 {
		arns, err = c.taskARNs(ctx, cluster, service, ecstypes.DesiredStatusStopped)
		if err != nil {
			return nil, err
		}
	}
	if len(arns) == 0 {
		return nil, nil
	}
	out, err := c.api.DescribeTasks(ctx, &awsecs.DescribeTasksInput{
		Cluster: aws.String(cluster),
		Tasks:   arns,
	})
	if err != nil {
		return nil, err
	}
	if out == nil {
		return nil, nil
	}
	tasks := make([]Task, 0, len(out.Tasks))
	for _, task := range out.Tasks {
		item := Task{
			ARN:        aws.ToString(task.TaskArn),
			ID:         taskID(aws.ToString(task.TaskArn)),
			LastStatus: aws.ToString(task.LastStatus),
			Reason:     aws.ToString(task.StoppedReason),
		}
		for _, container := range task.Containers {
			if aws.ToString(container.Name) != containerName && len(task.Containers) != 1 {
				continue
			}
			if container.ExitCode != nil {
				item.ExitCode = int(*container.ExitCode)
			}
			if item.Reason == "" {
				item.Reason = aws.ToString(container.Reason)
			}
			break
		}
		tasks = append(tasks, item)
	}
	return tasks, nil
}

func (c *ecsClient) taskARNs(ctx context.Context, cluster, service string, status ecstypes.DesiredStatus) ([]string, error) {
	out, err := c.api.ListTasks(ctx, &awsecs.ListTasksInput{
		Cluster:       aws.String(cluster),
		ServiceName:   aws.String(service),
		DesiredStatus: status,
	})
	if err != nil {
		return nil, err
	}
	if out == nil {
		return nil, nil
	}
	return out.TaskArns, nil
}

func (c *elbClient) ARN(ctx context.Context, name string) (string, error) {
	out, err := c.api.DescribeTargetGroups(ctx, &awselb.DescribeTargetGroupsInput{
		Names: []string{name},
	})
	if err != nil {
		if errorCode(err) == "TargetGroupNotFound" || errorCode(err) == "TargetGroupNotFoundException" {
			return "", errTargetGroupNotFound
		}
		return "", err
	}
	if out == nil || len(out.TargetGroups) == 0 || out.TargetGroups[0].TargetGroupArn == nil {
		return "", errTargetGroupNotFound
	}
	return aws.ToString(out.TargetGroups[0].TargetGroupArn), nil
}

func (c *elbClient) Healthy(ctx context.Context, arn string) (bool, error) {
	out, err := c.api.DescribeTargetHealth(ctx, &awselb.DescribeTargetHealthInput{
		TargetGroupArn: aws.String(arn),
	})
	if err != nil {
		return false, err
	}
	if out == nil {
		return false, nil
	}
	for _, desc := range out.TargetHealthDescriptions {
		if desc.TargetHealth != nil && desc.TargetHealth.State == elbtypes.TargetHealthStateEnumHealthy {
			return true, nil
		}
	}
	return false, nil
}

func (c *logClient) Read(ctx context.Context, group, stream string, limit int) ([]byte, error) {
	if limit < 1 {
		limit = logLimit
	}
	if limit > 10000 {
		limit = 10000
	}
	out, err := c.api.GetLogEvents(ctx, &awscwl.GetLogEventsInput{
		LogGroupName:  aws.String(group),
		LogStreamName: aws.String(stream),
		Limit:         aws.Int32(int32(limit)),
		StartFromHead: aws.Bool(true),
	})
	if err != nil {
		if errorCode(err) == "ResourceNotFoundException" {
			return nil, nil
		}
		return nil, err
	}
	if out == nil || len(out.Events) == 0 {
		return nil, nil
	}
	var buf strings.Builder
	for _, event := range out.Events {
		msg := aws.ToString(event.Message)
		if msg == "" {
			continue
		}
		buf.WriteString(msg)
		if !strings.HasSuffix(msg, "\n") {
			buf.WriteByte('\n')
		}
	}
	return []byte(buf.String()), nil
}

func serviceRequest(in ServiceInput) (*awsecs.CreateServiceInput, error) {
	port, err := port32(in.ContainerPort)
	if err != nil {
		return nil, err
	}
	return &awsecs.CreateServiceInput{
		Cluster:         aws.String(in.Cluster),
		ServiceName:     aws.String(in.Name),
		TaskDefinition:  aws.String(in.TaskDefinition),
		DesiredCount:    aws.Int32(int32(in.DesiredCount)),
		LaunchType:      ecstypes.LaunchTypeFargate,
		PlatformVersion: aws.String("LATEST"),
		NetworkConfiguration: &ecstypes.NetworkConfiguration{
			AwsvpcConfiguration: vpcConfig(in),
		},
		LoadBalancers: []ecstypes.LoadBalancer{{
			TargetGroupArn: aws.String(in.TargetGroupARN),
			ContainerName:  aws.String(in.ContainerName),
			ContainerPort:  aws.Int32(port),
		}},
		HealthCheckGracePeriodSeconds: aws.Int32(60),
		DeploymentConfiguration: &ecstypes.DeploymentConfiguration{
			MinimumHealthyPercent: aws.Int32(100),
			MaximumPercent:        aws.Int32(200),
			DeploymentCircuitBreaker: &ecstypes.DeploymentCircuitBreaker{
				Enable:   true,
				Rollback: true,
			},
		},
		PropagateTags: ecstypes.PropagateTagsService,
		Tags:          serviceTags(in.AppName),
	}, nil
}

func vpcConfig(in ServiceInput) *ecstypes.AwsVpcConfiguration {
	assign := ecstypes.AssignPublicIpDisabled
	if in.AssignPublicIP {
		assign = ecstypes.AssignPublicIpEnabled
	}
	return &ecstypes.AwsVpcConfiguration{
		Subnets:        in.Subnets,
		SecurityGroups: in.SecurityGroups,
		AssignPublicIp: assign,
	}
}

func serviceTags(app string) []ecstypes.Tag {
	return []ecstypes.Tag{
		{Key: aws.String("app"), Value: aws.String(app)},
		{Key: aws.String("managed-by"), Value: aws.String("shipyard")},
	}
}

func taskDefinitionFrom(def *ecstypes.TaskDefinition, container string) TaskDefinition {
	if def == nil {
		return TaskDefinition{}
	}
	image := ""
	for _, item := range def.ContainerDefinitions {
		if aws.ToString(item.Name) == container || container == "" {
			image = aws.ToString(item.Image)
			break
		}
	}
	return TaskDefinition{
		ARN:      aws.ToString(def.TaskDefinitionArn),
		Family:   aws.ToString(def.Family),
		Revision: int(def.Revision),
		Image:    image,
	}
}

func taskID(arn string) string {
	if arn == "" {
		return ""
	}
	i := strings.LastIndex(arn, "/")
	if i < 0 || i == len(arn)-1 {
		return arn
	}
	return arn[i+1:]
}

func registryHost(endpoint string) (string, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return "", fmt.Errorf("ecr returned no registry endpoint")
	}
	if !strings.Contains(endpoint, "://") {
		endpoint = "https://" + endpoint
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" {
		return "", fmt.Errorf("invalid ecr registry endpoint")
	}
	return parsed.Host, nil
}

func port32(port int) (int32, error) {
	if port < 1 || port > 65535 {
		return 0, fmt.Errorf("invalid container port")
	}
	return int32(port), nil
}

func errorCode(err error) string {
	var api smithy.APIError
	if errors.As(err, &api) {
		return api.ErrorCode()
	}
	return ""
}

// Compile-time checks that the SDK clients match the small interfaces.
var (
	_ ecrAPI = (*awsecr.Client)(nil)
	_ ecsAPI = (*awsecs.Client)(nil)
	_ elbAPI = (*awselb.Client)(nil)
	_ logAPI = (*awscwl.Client)(nil)
)
