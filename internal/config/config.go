// Package config loads process settings for the shipyard binaries.
package config

import (
	"fmt"
	"math"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultAddr is the shipyardd listen address when SHIPYARD_ADDR is unset.
	DefaultAddr = ":8080"
	// DefaultProxyAddr is the reverse proxy listen address.
	DefaultProxyAddr = ":8081"
	// DefaultAPIURL is the CLI API base URL when no flag or SHIPYARD_API_URL is set.
	DefaultAPIURL = "http://127.0.0.1:8080"
	// DefaultBuildConcurrency is the number of image builds that run at once.
	DefaultBuildConcurrency = 2
	// DefaultBuildQueueSize is how many builds may wait for a worker.
	DefaultBuildQueueSize = 16
	// DefaultBuildTimeout bounds clone plus docker build.
	DefaultBuildTimeout = 10 * time.Minute
	// DefaultCloneTimeout bounds the git fetch.
	DefaultCloneTimeout = 2 * time.Minute
	// DefaultCloneMaxBytes is the maximum checkout size, including .git.
	DefaultCloneMaxBytes int64 = 100 << 20
	// DefaultContainerPort is the port inside a running app container.
	DefaultContainerPort = 8080
	// DefaultMemoryBytes is the memory limit applied to an app container.
	DefaultMemoryBytes int64 = 256 << 20
	// DefaultCPUMilli is the CPU limit in milli-CPU. 500 is half a core.
	DefaultCPUMilli = 500
	// DefaultHealthPath is the HTTP path polled before traffic is routed.
	DefaultHealthPath = "/"
	// DefaultHealthTimeout bounds the health gate.
	DefaultHealthTimeout = 30 * time.Second
	// DefaultHealthInterval is the delay between health probes.
	DefaultHealthInterval = 500 * time.Millisecond
	// DefaultReleaseHistory is how many successful images are kept per app.
	DefaultReleaseHistory = 5
	// DefaultRuntime is the local Docker runtime.
	DefaultRuntime = RuntimeDocker
	// DefaultECSDeployTimeout bounds an ECS rollout, including image pull.
	DefaultECSDeployTimeout = 10 * time.Minute
	// DefaultDBPath is the SQLite file used when SHIPYARD_DB is unset.
	DefaultDBPath = "shipyard.db"
	// MemoryDBPath selects the in-memory store instead of SQLite.
	MemoryDBPath = "memory"

	// EnvAddr is the environment variable that overrides the server listen address.
	EnvAddr = "SHIPYARD_ADDR"
	// EnvProxyAddr overrides the reverse proxy listen address.
	EnvProxyAddr = "SHIPYARD_PROXY_ADDR"
	// EnvAPIURL is the environment variable that overrides the CLI API base URL.
	EnvAPIURL = "SHIPYARD_API_URL"
	// EnvBuildConcurrency overrides the worker count.
	EnvBuildConcurrency = "SHIPYARD_BUILD_CONCURRENCY"
	// EnvBuildQueueSize overrides the build queue length.
	EnvBuildQueueSize = "SHIPYARD_BUILD_QUEUE_SIZE"
	// EnvBuildTimeout overrides the build time limit. Use Go duration syntax, such as 10m.
	EnvBuildTimeout = "SHIPYARD_BUILD_TIMEOUT"
	// EnvCloneTimeout overrides the clone time limit.
	EnvCloneTimeout = "SHIPYARD_CLONE_TIMEOUT"
	// EnvCloneMaxBytes overrides the checkout size cap, in bytes.
	EnvCloneMaxBytes = "SHIPYARD_CLONE_MAX_BYTES"
	// EnvContainerPort overrides the port inside the app container. The container also receives PORT.
	EnvContainerPort = "SHIPYARD_CONTAINER_PORT"
	// EnvMemoryBytes overrides the container memory limit, in bytes.
	EnvMemoryBytes = "SHIPYARD_MEMORY_BYTES"
	// EnvCPUs overrides the container CPU limit. 0.5 is half a core.
	EnvCPUs = "SHIPYARD_CPUS"
	// EnvHealthPath overrides the HTTP health path.
	EnvHealthPath = "SHIPYARD_HEALTH_PATH"
	// EnvHealthTimeout overrides how long to wait for a healthy response.
	EnvHealthTimeout = "SHIPYARD_HEALTH_TIMEOUT"
	// EnvHealthInterval overrides the delay between health probes.
	EnvHealthInterval = "SHIPYARD_HEALTH_INTERVAL"
	// EnvRuntimeNetwork attaches app containers to a Docker network.
	// Empty publishes a random port on 127.0.0.1 instead.
	EnvRuntimeNetwork = "SHIPYARD_RUNTIME_NETWORK"
	// EnvDB is the SQLite file. "memory" keeps the in-memory store.
	EnvDB = "SHIPYARD_DB"
	// EnvReleaseHistory is how many successful images to keep per app.
	EnvReleaseHistory = "SHIPYARD_RELEASE_HISTORY"
	// EnvWebhookSecret is the global GitHub webhook HMAC secret.
	// It is read from the environment and is never logged.
	EnvWebhookSecret = "SHIPYARD_GITHUB_WEBHOOK_SECRET"
	// EnvRuntime selects docker or ecs.
	EnvRuntime = "SHIPYARD_RUNTIME"
	// EnvAWSRegion is the AWS region for the ECS runtime.
	EnvAWSRegion = "SHIPYARD_AWS_REGION"
	// EnvECSCluster is the ECS cluster name.
	EnvECSCluster = "SHIPYARD_ECS_CLUSTER"
	// EnvECRRepository is the ECR repository URL without a scheme.
	EnvECRRepository = "SHIPYARD_ECR_REPOSITORY"
	// EnvECSExecutionRoleARN is the Fargate task execution role.
	EnvECSExecutionRoleARN = "SHIPYARD_ECS_EXECUTION_ROLE_ARN"
	// EnvECSTaskRoleARN is the role assumed by the app container.
	EnvECSTaskRoleARN = "SHIPYARD_ECS_TASK_ROLE_ARN"
	// EnvECSSubnets is a comma-separated list of public subnet ids.
	EnvECSSubnets = "SHIPYARD_ECS_SUBNETS"
	// EnvECSSecurityGroups is a comma-separated list of task security group ids.
	EnvECSSecurityGroups = "SHIPYARD_ECS_SECURITY_GROUPS"
	// EnvECSLogGroup is the CloudWatch log group for task output.
	EnvECSLogGroup = "SHIPYARD_ECS_LOG_GROUP"
	// EnvALBDNSName is the load balancer DNS name, without a scheme.
	EnvALBDNSName = "SHIPYARD_ALB_DNS_NAME"
	// EnvBaseDomain is an optional parent domain for host-based ALB rules.
	// Empty uses the path rule http://<alb>/apps/<app>/.
	EnvBaseDomain = "SHIPYARD_BASE_DOMAIN"
	// EnvECSDeployTimeout bounds the ECS rollout. Use Go duration syntax, such as 10m.
	EnvECSDeployTimeout = "SHIPYARD_ECS_DEPLOY_TIMEOUT"

	// RuntimeDocker runs containers on the local Docker daemon.
	RuntimeDocker = "docker"
	// RuntimeECS pushes the image to ECR and runs it on Fargate.
	RuntimeECS = "ecs"

	minBuildConcurrency = 1
	maxBuildConcurrency = 32
	minBuildQueueSize   = 1
	maxBuildQueueSize   = 256
	minTimeout          = time.Second
	maxTimeout          = 30 * time.Minute
	minCloneBytes       = 1 << 20
	maxCloneBytes       = 1 << 30
	minContainerPort    = 1
	maxContainerPort    = 65535
	minMemoryBytes      = 16 << 20
	maxMemoryBytes      = 8 << 30
	minCPUMilli         = 100
	maxCPUMilli         = 8000
	minHealthTimeout    = time.Second
	maxHealthTimeout    = 5 * time.Minute
	minHealthInterval   = 50 * time.Millisecond
	maxHealthInterval   = 10 * time.Second
	minReleaseHistory   = 1
	maxReleaseHistory   = 50
	maxWebhookSecret    = 256
	minECSDeployTimeout = time.Minute
	maxECSDeployTimeout = 15 * time.Minute
)

var (
	awsRegionPattern = regexp.MustCompile(`^[a-z]{2}-[a-z0-9-]+-[0-9]+$`)
	ecsNamePattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,254}$`)
	subnetPattern    = regexp.MustCompile(`^subnet-[0-9a-f]{8,32}$`)
	sgPattern        = regexp.MustCompile(`^sg-[0-9a-f]{8,32}$`)
	logGroupPattern  = regexp.MustCompile(`^/[A-Za-z0-9_./-]{1,500}$`)
	domainPattern    = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)
)

var networkNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

// Config is the runtime configuration for shipyardd.
type Config struct {
	// Addr is the TCP address the HTTP API listens on, such as ":8080".
	Addr string
	// ProxyAddr is the TCP address the reverse proxy listens on, such as ":8081".
	ProxyAddr string
	// BuildConcurrency is the number of builds that run at once.
	BuildConcurrency int
	// BuildQueueSize is how many accepted builds may wait for a worker.
	BuildQueueSize int
	// BuildTimeout bounds one build, including the clone.
	BuildTimeout time.Duration
	// CloneTimeout bounds the git fetch inside a build.
	CloneTimeout time.Duration
	// CloneMaxBytes is the maximum size of a checkout.
	CloneMaxBytes int64
	// ContainerPort is the port the app process listens on inside the container.
	ContainerPort int
	// MemoryBytes is the memory limit for an app container.
	MemoryBytes int64
	// CPUMilli is the CPU limit in milli-CPU.
	CPUMilli int
	// HealthPath is the HTTP path polled on the container.
	HealthPath string
	// HealthTimeout bounds the health gate.
	HealthTimeout time.Duration
	// HealthInterval is the delay between health probes.
	HealthInterval time.Duration
	// RuntimeNetwork is a Docker network for app containers.
	// Empty publishes a random host port on 127.0.0.1.
	RuntimeNetwork string
	// DBPath is the SQLite database file, or MemoryDBPath.
	DBPath string
	// ReleaseHistory is how many successful images are kept for rollback.
	ReleaseHistory int
	// WebhookSecret is the global GitHub HMAC secret. Empty means only a
	// per-app secret can verify deliveries. It must never be logged.
	WebhookSecret string
	// Runtime is RuntimeDocker or RuntimeECS.
	Runtime string
	// AWSRegion is the region used when Runtime is RuntimeECS.
	AWSRegion string
	// ECSCluster is the ECS cluster name.
	ECSCluster string
	// ECRRepository is the repository URL without a scheme.
	ECRRepository string
	// ECSExecutionRoleARN pulls images and writes logs.
	ECSExecutionRoleARN string
	// ECSTaskRoleARN is assumed by the app container.
	ECSTaskRoleARN string
	// ECSSubnets are the public subnets for the task network.
	ECSSubnets []string
	// ECSSecurityGroups are attached to the task.
	ECSSecurityGroups []string
	// ECSLogGroup is the CloudWatch log group name.
	ECSLogGroup string
	// ALBDNSName is the load balancer DNS name without a scheme.
	ALBDNSName string
	// BaseDomain selects host rules when set. Empty selects path rules.
	BaseDomain string
	// ECSDeployTimeout bounds an ECS rollout.
	ECSDeployTimeout time.Duration
}

// Defaults returns configuration with no environment overrides applied.
func Defaults() Config {
	return Config{
		Addr:             DefaultAddr,
		ProxyAddr:        DefaultProxyAddr,
		BuildConcurrency: DefaultBuildConcurrency,
		BuildQueueSize:   DefaultBuildQueueSize,
		BuildTimeout:     DefaultBuildTimeout,
		CloneTimeout:     DefaultCloneTimeout,
		CloneMaxBytes:    DefaultCloneMaxBytes,
		ContainerPort:    DefaultContainerPort,
		MemoryBytes:      DefaultMemoryBytes,
		CPUMilli:         DefaultCPUMilli,
		HealthPath:       DefaultHealthPath,
		HealthTimeout:    DefaultHealthTimeout,
		HealthInterval:   DefaultHealthInterval,
		DBPath:           DefaultDBPath,
		ReleaseHistory:   DefaultReleaseHistory,
		Runtime:          DefaultRuntime,
		ECSDeployTimeout: DefaultECSDeployTimeout,
	}
}

// Check reports whether c is inside the supported limits.
func (c Config) Check() error {
	if strings.TrimSpace(c.Addr) == "" {
		return fmt.Errorf("addr is required")
	}
	if err := checkListenAddr(c.ProxyAddr); err != nil {
		return fmt.Errorf("proxy addr: %w", err)
	}
	if c.BuildConcurrency < minBuildConcurrency || c.BuildConcurrency > maxBuildConcurrency {
		return fmt.Errorf("build concurrency must be from %d to %d", minBuildConcurrency, maxBuildConcurrency)
	}
	if c.BuildQueueSize < minBuildQueueSize || c.BuildQueueSize > maxBuildQueueSize {
		return fmt.Errorf("build queue size must be from %d to %d", minBuildQueueSize, maxBuildQueueSize)
	}
	if c.BuildTimeout < minTimeout || c.BuildTimeout > maxTimeout {
		return fmt.Errorf("build timeout must be from %s to %s", minTimeout, maxTimeout)
	}
	if c.CloneTimeout < minTimeout || c.CloneTimeout > maxTimeout {
		return fmt.Errorf("clone timeout must be from %s to %s", minTimeout, maxTimeout)
	}
	if c.CloneTimeout > c.BuildTimeout {
		return fmt.Errorf("clone timeout must be less than or equal to build timeout")
	}
	if c.CloneMaxBytes < minCloneBytes || c.CloneMaxBytes > maxCloneBytes {
		return fmt.Errorf("clone max bytes must be from %d to %d", minCloneBytes, maxCloneBytes)
	}
	if c.ContainerPort < minContainerPort || c.ContainerPort > maxContainerPort {
		return fmt.Errorf("container port must be from %d to %d", minContainerPort, maxContainerPort)
	}
	if c.MemoryBytes < minMemoryBytes || c.MemoryBytes > maxMemoryBytes {
		return fmt.Errorf("memory bytes must be from %d to %d", minMemoryBytes, maxMemoryBytes)
	}
	if c.CPUMilli < minCPUMilli || c.CPUMilli > maxCPUMilli {
		return fmt.Errorf("cpu must be from %.1f to %.1f", float64(minCPUMilli)/1000, float64(maxCPUMilli)/1000)
	}
	if !validHealthPath(c.HealthPath) {
		return fmt.Errorf("health path must be an absolute path")
	}
	if c.HealthTimeout < minHealthTimeout || c.HealthTimeout > maxHealthTimeout {
		return fmt.Errorf("health timeout must be from %s to %s", minHealthTimeout, maxHealthTimeout)
	}
	if c.HealthInterval < minHealthInterval || c.HealthInterval > maxHealthInterval {
		return fmt.Errorf("health interval must be from %s to %s", minHealthInterval, maxHealthInterval)
	}
	if c.HealthInterval >= c.HealthTimeout {
		return fmt.Errorf("health interval must be less than health timeout")
	}
	if c.RuntimeNetwork != "" && !networkNamePattern.MatchString(c.RuntimeNetwork) {
		return fmt.Errorf("runtime network must be a docker network name")
	}
	if err := checkDBPath(c.DBPath); err != nil {
		return err
	}
	if c.ReleaseHistory < minReleaseHistory || c.ReleaseHistory > maxReleaseHistory {
		return fmt.Errorf("release history must be from %d to %d", minReleaseHistory, maxReleaseHistory)
	}
	if err := checkWebhookSecret(c.WebhookSecret); err != nil {
		return err
	}
	if err := c.checkRuntime(); err != nil {
		return err
	}
	return nil
}

// Load reads shipyardd configuration from the environment.
// Empty values use defaults. A set value that is out of range returns an error.
func Load() (Config, error) {
	cfg := Defaults()
	if addr := strings.TrimSpace(os.Getenv(EnvAddr)); addr != "" {
		cfg.Addr = addr
	}
	if addr := strings.TrimSpace(os.Getenv(EnvProxyAddr)); addr != "" {
		cfg.ProxyAddr = addr
	}
	var err error
	if cfg.BuildConcurrency, err = envInt(EnvBuildConcurrency, DefaultBuildConcurrency, maxBuildConcurrency); err != nil {
		return Config{}, err
	}
	if cfg.BuildQueueSize, err = envInt(EnvBuildQueueSize, DefaultBuildQueueSize, maxBuildQueueSize); err != nil {
		return Config{}, err
	}
	if cfg.BuildTimeout, err = envDuration(EnvBuildTimeout, DefaultBuildTimeout); err != nil {
		return Config{}, err
	}
	if cfg.CloneTimeout, err = envDuration(EnvCloneTimeout, DefaultCloneTimeout); err != nil {
		return Config{}, err
	}
	if cfg.CloneTimeout > cfg.BuildTimeout {
		return Config{}, fmt.Errorf("%s must be less than or equal to %s", EnvCloneTimeout, EnvBuildTimeout)
	}
	if cfg.CloneMaxBytes, err = envInt64(EnvCloneMaxBytes, DefaultCloneMaxBytes, minCloneBytes, maxCloneBytes); err != nil {
		return Config{}, err
	}
	if cfg.ContainerPort, err = envInt(EnvContainerPort, DefaultContainerPort, maxContainerPort); err != nil {
		return Config{}, err
	}
	if cfg.MemoryBytes, err = envInt64(EnvMemoryBytes, DefaultMemoryBytes, minMemoryBytes, maxMemoryBytes); err != nil {
		return Config{}, err
	}
	if cfg.CPUMilli, err = envCPU(EnvCPUs, DefaultCPUMilli); err != nil {
		return Config{}, err
	}
	if path := strings.TrimSpace(os.Getenv(EnvHealthPath)); path != "" {
		cfg.HealthPath = path
	}
	if !validHealthPath(cfg.HealthPath) {
		return Config{}, fmt.Errorf("%s must be an absolute path", EnvHealthPath)
	}
	if cfg.HealthTimeout, err = envDurationRange(EnvHealthTimeout, DefaultHealthTimeout, minHealthTimeout, maxHealthTimeout); err != nil {
		return Config{}, err
	}
	if cfg.HealthInterval, err = envDurationRange(EnvHealthInterval, DefaultHealthInterval, minHealthInterval, maxHealthInterval); err != nil {
		return Config{}, err
	}
	if cfg.HealthInterval >= cfg.HealthTimeout {
		return Config{}, fmt.Errorf("%s must be less than %s", EnvHealthInterval, EnvHealthTimeout)
	}
	cfg.RuntimeNetwork = strings.TrimSpace(os.Getenv(EnvRuntimeNetwork))
	if cfg.RuntimeNetwork != "" && !networkNamePattern.MatchString(cfg.RuntimeNetwork) {
		return Config{}, fmt.Errorf("%s must be a docker network name", EnvRuntimeNetwork)
	}
	if path := strings.TrimSpace(os.Getenv(EnvDB)); path != "" {
		cfg.DBPath = path
	}
	if err := checkDBPath(cfg.DBPath); err != nil {
		return Config{}, fmt.Errorf("%s: %w", EnvDB, err)
	}
	if cfg.ReleaseHistory, err = envInt(EnvReleaseHistory, DefaultReleaseHistory, maxReleaseHistory); err != nil {
		return Config{}, err
	}
	cfg.WebhookSecret = os.Getenv(EnvWebhookSecret)
	if err := checkWebhookSecret(cfg.WebhookSecret); err != nil {
		return Config{}, fmt.Errorf("%s is invalid", EnvWebhookSecret)
	}
	if runtimeName := strings.TrimSpace(os.Getenv(EnvRuntime)); runtimeName != "" {
		cfg.Runtime = runtimeName
	}
	cfg.AWSRegion = strings.TrimSpace(os.Getenv(EnvAWSRegion))
	cfg.ECSCluster = strings.TrimSpace(os.Getenv(EnvECSCluster))
	cfg.ECRRepository = strings.TrimSpace(os.Getenv(EnvECRRepository))
	cfg.ECSExecutionRoleARN = strings.TrimSpace(os.Getenv(EnvECSExecutionRoleARN))
	cfg.ECSTaskRoleARN = strings.TrimSpace(os.Getenv(EnvECSTaskRoleARN))
	cfg.ECSSubnets = splitList(os.Getenv(EnvECSSubnets))
	cfg.ECSSecurityGroups = splitList(os.Getenv(EnvECSSecurityGroups))
	cfg.ECSLogGroup = strings.TrimSpace(os.Getenv(EnvECSLogGroup))
	cfg.ALBDNSName = strings.TrimSpace(os.Getenv(EnvALBDNSName))
	cfg.BaseDomain = strings.TrimSpace(os.Getenv(EnvBaseDomain))
	if cfg.ECSDeployTimeout, err = envDurationRange(EnvECSDeployTimeout, DefaultECSDeployTimeout, minECSDeployTimeout, maxECSDeployTimeout); err != nil {
		return Config{}, err
	}
	if err := cfg.checkRuntime(); err != nil {
		return Config{}, err
	}
	if err := checkListenAddr(cfg.ProxyAddr); err != nil {
		return Config{}, fmt.Errorf("%s: %w", EnvProxyAddr, err)
	}
	return cfg, nil
}

func checkDBPath(path string) error {
	path = strings.TrimSpace(path)
	if path == "" || strings.ContainsAny(path, "\x00\r\n") {
		return fmt.Errorf("database path is invalid")
	}
	if path == MemoryDBPath {
		return nil
	}
	if strings.HasPrefix(path, "-") {
		return fmt.Errorf("database path is invalid")
	}
	return nil
}

func checkWebhookSecret(secret string) error {
	if len(secret) > maxWebhookSecret || strings.ContainsRune(secret, 0) {
		return fmt.Errorf("webhook secret is invalid")
	}
	return nil
}

func checkListenAddr(addr string) error {
	_, port, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil {
		return fmt.Errorf("must be host:port")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("must include a port from 1 to 65535")
	}
	return nil
}

func validHealthPath(path string) bool {
	if path == "" || len(path) > 128 || !strings.HasPrefix(path, "/") {
		return false
	}
	if strings.Contains(path, "..") || strings.ContainsAny(path, " \t\r\n?#") {
		return false
	}
	return true
}

func envInt(key string, def, max int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > max {
		return 0, fmt.Errorf("%s must be an integer from 1 to %d", key, max)
	}
	return n, nil
}

func envInt64(key string, def, min, max int64) (int64, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < min || n > max {
		return 0, fmt.Errorf("%s must be an integer from %d to %d", key, min, max)
	}
	return n, nil
}

func envCPU(key string, defMilli int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return defMilli, nil
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be a number from %.1f to %.1f", key, float64(minCPUMilli)/1000, float64(maxCPUMilli)/1000)
	}
	milli := int(math.Round(f * 1000))
	if milli < minCPUMilli || milli > maxCPUMilli {
		return 0, fmt.Errorf("%s must be a number from %.1f to %.1f", key, float64(minCPUMilli)/1000, float64(maxCPUMilli)/1000)
	}
	return milli, nil
}

func envDuration(key string, def time.Duration) (time.Duration, error) {
	return envDurationRange(key, def, minTimeout, maxTimeout)
}

func envDurationRange(key string, def, min, max time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < min || d > max {
		return 0, fmt.Errorf("%s must be a duration from %s to %s", key, min, max)
	}
	return d, nil
}

func (c Config) checkRuntime() error {
	if c.ECSDeployTimeout < minECSDeployTimeout || c.ECSDeployTimeout > maxECSDeployTimeout {
		return fmt.Errorf("%s must be a duration from %s to %s", EnvECSDeployTimeout, minECSDeployTimeout, maxECSDeployTimeout)
	}
	switch c.Runtime {
	case RuntimeDocker:
		return nil
	case RuntimeECS:
		return checkECS(c)
	default:
		return fmt.Errorf("%s must be %s or %s", EnvRuntime, RuntimeDocker, RuntimeECS)
	}
}

func checkECS(c Config) error {
	if !awsRegionPattern.MatchString(c.AWSRegion) {
		return fmt.Errorf("%s must be an AWS region such as us-east-1", EnvAWSRegion)
	}
	if !ecsNamePattern.MatchString(c.ECSCluster) {
		return fmt.Errorf("%s must be an ECS cluster name", EnvECSCluster)
	}
	if !repositoryMatches(c.ECRRepository, c.AWSRegion) {
		return fmt.Errorf("%s must be an ECR repository URL in %s", EnvECRRepository, c.AWSRegion)
	}
	if !validRoleARN(c.ECSExecutionRoleARN) {
		return fmt.Errorf("%s must be an IAM role ARN", EnvECSExecutionRoleARN)
	}
	if !validRoleARN(c.ECSTaskRoleARN) {
		return fmt.Errorf("%s must be an IAM role ARN", EnvECSTaskRoleARN)
	}
	if len(c.ECSSubnets) < 2 {
		return fmt.Errorf("%s must list two subnet ids", EnvECSSubnets)
	}
	for _, id := range c.ECSSubnets {
		if !subnetPattern.MatchString(id) {
			return fmt.Errorf("%s must list subnet ids", EnvECSSubnets)
		}
	}
	if len(c.ECSSecurityGroups) == 0 {
		return fmt.Errorf("%s must list a security group id", EnvECSSecurityGroups)
	}
	for _, id := range c.ECSSecurityGroups {
		if !sgPattern.MatchString(id) {
			return fmt.Errorf("%s must list security group ids", EnvECSSecurityGroups)
		}
	}
	if !logGroupPattern.MatchString(c.ECSLogGroup) {
		return fmt.Errorf("%s must be a CloudWatch log group name", EnvECSLogGroup)
	}
	if !domainPattern.MatchString(c.ALBDNSName) {
		return fmt.Errorf("%s must be a load balancer DNS name", EnvALBDNSName)
	}
	if c.BaseDomain != "" && !domainPattern.MatchString(c.BaseDomain) {
		return fmt.Errorf("%s must be a bare domain name", EnvBaseDomain)
	}
	return nil
}

func repositoryMatches(ref, region string) bool {
	ref = strings.TrimPrefix(strings.TrimSpace(ref), "https://")
	ref = strings.TrimSuffix(ref, "/")
	host, name, ok := strings.Cut(ref, "/")
	if !ok || name == "" || strings.ContainsAny(ref, " \t") || strings.Contains(name, "..") {
		return false
	}
	return strings.HasSuffix(host, ".dkr.ecr."+region+".amazonaws.com")
}

func validRoleARN(arn string) bool {
	if len(arn) < 20 || len(arn) > 2048 || strings.ContainsAny(arn, " \t\r\n") {
		return false
	}
	return strings.HasPrefix(arn, "arn:aws") && strings.Contains(arn, ":iam::") && strings.Contains(arn, ":role/")
}

func splitList(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
