// Package config loads process settings for the shipyard binaries.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultAddr is the shipyardd listen address when SHIPYARD_ADDR is unset.
	DefaultAddr = ":8080"
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

	// EnvAddr is the environment variable that overrides the server listen address.
	EnvAddr = "SHIPYARD_ADDR"
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

	minBuildConcurrency = 1
	maxBuildConcurrency = 32
	minBuildQueueSize   = 1
	maxBuildQueueSize   = 256
	minTimeout          = time.Second
	maxTimeout          = 30 * time.Minute
	minCloneBytes       = 1 << 20
	maxCloneBytes       = 1 << 30
)

// Config is the runtime configuration for shipyardd.
type Config struct {
	// Addr is the TCP address the HTTP server listens on, such as ":8080".
	Addr string
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
}

// Defaults returns configuration with no environment overrides applied.
func Defaults() Config {
	return Config{
		Addr:             DefaultAddr,
		BuildConcurrency: DefaultBuildConcurrency,
		BuildQueueSize:   DefaultBuildQueueSize,
		BuildTimeout:     DefaultBuildTimeout,
		CloneTimeout:     DefaultCloneTimeout,
		CloneMaxBytes:    DefaultCloneMaxBytes,
	}
}

// Check reports whether c is inside the supported limits.
func (c Config) Check() error {
	if strings.TrimSpace(c.Addr) == "" {
		return fmt.Errorf("addr is required")
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
	return nil
}

// Load reads shipyardd configuration from the environment.
// Empty values use defaults. A set value that is out of range returns an error.
func Load() (Config, error) {
	cfg := Defaults()
	if addr := strings.TrimSpace(os.Getenv(EnvAddr)); addr != "" {
		cfg.Addr = addr
	}
	var err error
	if cfg.BuildConcurrency, err = envInt(EnvBuildConcurrency, DefaultBuildConcurrency, minBuildConcurrency, maxBuildConcurrency); err != nil {
		return Config{}, err
	}
	if cfg.BuildQueueSize, err = envInt(EnvBuildQueueSize, DefaultBuildQueueSize, minBuildQueueSize, maxBuildQueueSize); err != nil {
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
	return cfg, nil
}

func envInt(key string, def, min, max int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < min || n > max {
		return 0, fmt.Errorf("%s must be an integer from %d to %d", key, min, max)
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

func envDuration(key string, def time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < minTimeout || d > maxTimeout {
		return 0, fmt.Errorf("%s must be a duration from %s to %s", key, minTimeout, maxTimeout)
	}
	return d, nil
}
