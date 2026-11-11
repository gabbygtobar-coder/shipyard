package config

import (
	"strings"
	"testing"
	"time"
)

func clearConfigEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		EnvAddr,
		EnvProxyAddr,
		EnvBuildConcurrency,
		EnvBuildQueueSize,
		EnvBuildTimeout,
		EnvCloneTimeout,
		EnvCloneMaxBytes,
		EnvContainerPort,
		EnvMemoryBytes,
		EnvCPUs,
		EnvHealthPath,
		EnvHealthTimeout,
		EnvHealthInterval,
		EnvRuntimeNetwork,
		EnvDB,
		EnvReleaseHistory,
		EnvWebhookSecret,
		EnvRuntime,
		EnvAWSRegion,
		EnvECSCluster,
		EnvECRRepository,
		EnvECSExecutionRoleARN,
		EnvECSTaskRoleARN,
		EnvECSSubnets,
		EnvECSSecurityGroups,
		EnvECSLogGroup,
		EnvALBDNSName,
		EnvBaseDomain,
		EnvECSDeployTimeout,
	} {
		t.Setenv(key, "")
	}
}

func TestLoadDefaults(t *testing.T) {
	clearConfigEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != DefaultAddr {
		t.Fatalf("Addr = %q, want %q", cfg.Addr, DefaultAddr)
	}
	if cfg.BuildConcurrency != DefaultBuildConcurrency || cfg.BuildQueueSize != DefaultBuildQueueSize {
		t.Fatalf("pool = %d queue %d", cfg.BuildConcurrency, cfg.BuildQueueSize)
	}
	if cfg.BuildTimeout != DefaultBuildTimeout || cfg.CloneTimeout != DefaultCloneTimeout {
		t.Fatalf("timeouts = %s %s", cfg.BuildTimeout, cfg.CloneTimeout)
	}
	if cfg.CloneMaxBytes != DefaultCloneMaxBytes {
		t.Fatalf("clone max = %d", cfg.CloneMaxBytes)
	}
	if cfg.ProxyAddr != DefaultProxyAddr || cfg.ContainerPort != DefaultContainerPort {
		t.Fatalf("proxy %s port %d", cfg.ProxyAddr, cfg.ContainerPort)
	}
	if cfg.MemoryBytes != DefaultMemoryBytes || cfg.CPUMilli != DefaultCPUMilli {
		t.Fatalf("limits memory %d cpu %d", cfg.MemoryBytes, cfg.CPUMilli)
	}
	if cfg.HealthPath != DefaultHealthPath || cfg.HealthTimeout != DefaultHealthTimeout || cfg.HealthInterval != DefaultHealthInterval {
		t.Fatalf("health %#v", cfg)
	}
	if cfg.RuntimeNetwork != "" {
		t.Fatalf("network %q", cfg.RuntimeNetwork)
	}
	if cfg.DBPath != DefaultDBPath || cfg.ReleaseHistory != DefaultReleaseHistory || cfg.WebhookSecret != "" {
		t.Fatalf("store %#v", cfg)
	}
	if cfg.Runtime != RuntimeDocker || cfg.ECSDeployTimeout != DefaultECSDeployTimeout {
		t.Fatalf("runtime %s timeout %s", cfg.Runtime, cfg.ECSDeployTimeout)
	}
	if cfg.ECSCluster != "" || cfg.ALBDNSName != "" || len(cfg.ECSSubnets) != 0 {
		t.Fatalf("ecs config %#v", cfg)
	}
}

func TestLoadFromEnv(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv(EnvAddr, " 127.0.0.1:9090 ")
	t.Setenv(EnvBuildConcurrency, "4")
	t.Setenv(EnvBuildQueueSize, "8")
	t.Setenv(EnvBuildTimeout, "15m")
	t.Setenv(EnvCloneTimeout, "30s")
	t.Setenv(EnvCloneMaxBytes, "2097152")
	t.Setenv(EnvProxyAddr, "127.0.0.1:9091")
	t.Setenv(EnvContainerPort, "3000")
	t.Setenv(EnvMemoryBytes, "67108864")
	t.Setenv(EnvCPUs, "1.5")
	t.Setenv(EnvHealthPath, "/healthz")
	t.Setenv(EnvHealthTimeout, "20s")
	t.Setenv(EnvHealthInterval, "250ms")
	t.Setenv(EnvRuntimeNetwork, "shipyard")
	t.Setenv(EnvDB, "data/shipyard.db")
	t.Setenv(EnvReleaseHistory, "8")
	t.Setenv(EnvWebhookSecret, "global-secret")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != "127.0.0.1:9090" {
		t.Fatalf("Addr = %q, want trimmed env value", cfg.Addr)
	}
	if cfg.BuildConcurrency != 4 || cfg.BuildQueueSize != 8 {
		t.Fatalf("pool = %d queue %d", cfg.BuildConcurrency, cfg.BuildQueueSize)
	}
	if cfg.BuildTimeout != 15*time.Minute || cfg.CloneTimeout != 30*time.Second {
		t.Fatalf("timeouts = %s %s", cfg.BuildTimeout, cfg.CloneTimeout)
	}
	if cfg.CloneMaxBytes != 2<<20 {
		t.Fatalf("clone max = %d", cfg.CloneMaxBytes)
	}
	if cfg.ProxyAddr != "127.0.0.1:9091" || cfg.ContainerPort != 3000 {
		t.Fatalf("proxy %s port %d", cfg.ProxyAddr, cfg.ContainerPort)
	}
	if cfg.MemoryBytes != 64<<20 || cfg.CPUMilli != 1500 {
		t.Fatalf("limits memory %d cpu %d", cfg.MemoryBytes, cfg.CPUMilli)
	}
	if cfg.HealthPath != "/healthz" || cfg.HealthTimeout != 20*time.Second || cfg.HealthInterval != 250*time.Millisecond {
		t.Fatalf("health %#v", cfg)
	}
	if cfg.RuntimeNetwork != "shipyard" {
		t.Fatalf("network %q", cfg.RuntimeNetwork)
	}
	if cfg.DBPath != "data/shipyard.db" || cfg.ReleaseHistory != 8 || cfg.WebhookSecret != "global-secret" {
		t.Fatalf("store path %q history %d", cfg.DBPath, cfg.ReleaseHistory)
	}
}

func TestLoadECS(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv(EnvRuntime, "ecs")
	t.Setenv(EnvAWSRegion, "us-east-1")
	t.Setenv(EnvECSCluster, "shipyard")
	t.Setenv(EnvECRRepository, "123456789012.dkr.ecr.us-east-1.amazonaws.com/shipyard")
	t.Setenv(EnvECSExecutionRoleARN, "arn:aws:iam::123456789012:role/shipyard-ecs-execution")
	t.Setenv(EnvECSTaskRoleARN, "arn:aws:iam::123456789012:role/shipyard-ecs-task")
	t.Setenv(EnvECSSubnets, "subnet-0123456789abcdef0, subnet-abcdef0123456789a")
	t.Setenv(EnvECSSecurityGroups, "sg-0123456789abcdef0")
	t.Setenv(EnvECSLogGroup, "/shipyard/ecs")
	t.Setenv(EnvALBDNSName, "shipyard-alb-123.us-east-1.elb.amazonaws.com")
	t.Setenv(EnvBaseDomain, "example.com")
	t.Setenv(EnvECSDeployTimeout, "8m")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Runtime != RuntimeECS || cfg.AWSRegion != "us-east-1" || cfg.ECSCluster != "shipyard" {
		t.Fatalf("runtime %#v", cfg)
	}
	if len(cfg.ECSSubnets) != 2 || cfg.ECSSubnets[1] != "subnet-abcdef0123456789a" {
		t.Fatalf("subnets %#v", cfg.ECSSubnets)
	}
	if cfg.BaseDomain != "example.com" || cfg.ECSDeployTimeout != 8*time.Minute {
		t.Fatalf("domain %s timeout %s", cfg.BaseDomain, cfg.ECSDeployTimeout)
	}
	if cfg.ECRRepository == "" || cfg.ALBDNSName == "" || len(cfg.ECSSecurityGroups) != 1 {
		t.Fatalf("ecs %#v", cfg)
	}
}

func TestLoadECSRequiresSettings(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv(EnvRuntime, "ecs")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), EnvAWSRegion) {
		t.Fatalf("error %v", err)
	}
}

func TestLoadWhitespaceOnlyUsesDefault(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv(EnvAddr, "   ")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != DefaultAddr {
		t.Fatalf("Addr = %q, want %q", cfg.Addr, DefaultAddr)
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	cases := []struct {
		key   string
		value string
	}{
		{key: EnvBuildConcurrency, value: "0"},
		{key: EnvBuildConcurrency, value: "99"},
		{key: EnvBuildQueueSize, value: "nope"},
		{key: EnvBuildTimeout, value: "10"},
		{key: EnvBuildTimeout, value: "1ms"},
		{key: EnvCloneMaxBytes, value: "10"},
		{key: EnvContainerPort, value: "0"},
		{key: EnvMemoryBytes, value: "10"},
		{key: EnvCPUs, value: "nope"},
		{key: EnvHealthPath, value: "health"},
		{key: EnvHealthTimeout, value: "1ms"},
		{key: EnvProxyAddr, value: "8081"},
		{key: EnvRuntimeNetwork, value: "-bad"},
		{key: EnvReleaseHistory, value: "0"},
		{key: EnvDB, value: "-bad"},
		{key: EnvRuntime, value: "k8s"},
		{key: EnvECSDeployTimeout, value: "1s"},
	}
	for _, tc := range cases {
		t.Run(tc.key+"="+tc.value, func(t *testing.T) {
			clearConfigEnv(t)
			t.Setenv(tc.key, tc.value)
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), tc.key) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestLoadHealthIntervalCannotExceedTimeout(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv(EnvHealthTimeout, "1s")
	t.Setenv(EnvHealthInterval, "1s")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), EnvHealthInterval) {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadCloneTimeoutCannotExceedBuild(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv(EnvBuildTimeout, "1m")
	t.Setenv(EnvCloneTimeout, "2m")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), EnvCloneTimeout) {
		t.Fatalf("error = %v", err)
	}
}
