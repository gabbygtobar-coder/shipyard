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
		EnvBuildConcurrency,
		EnvBuildQueueSize,
		EnvBuildTimeout,
		EnvCloneTimeout,
		EnvCloneMaxBytes,
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
}

func TestLoadFromEnv(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv(EnvAddr, " 127.0.0.1:9090 ")
	t.Setenv(EnvBuildConcurrency, "4")
	t.Setenv(EnvBuildQueueSize, "8")
	t.Setenv(EnvBuildTimeout, "15m")
	t.Setenv(EnvCloneTimeout, "30s")
	t.Setenv(EnvCloneMaxBytes, "2097152")
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

func TestLoadCloneTimeoutCannotExceedBuild(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv(EnvBuildTimeout, "1m")
	t.Setenv(EnvCloneTimeout, "2m")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), EnvCloneTimeout) {
		t.Fatalf("error = %v", err)
	}
}
