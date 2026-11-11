// Package config loads process settings for the shipyard binaries.
package config

import (
	"os"
	"strings"
)

const (
	// DefaultAddr is the shipyardd listen address when SHIPYARD_ADDR is unset.
	DefaultAddr = ":8080"
	// DefaultAPIURL is the CLI API base URL when no flag or SHIPYARD_API_URL is set.
	DefaultAPIURL = "http://127.0.0.1:8080"
	// EnvAddr is the environment variable that overrides the server listen address.
	EnvAddr = "SHIPYARD_ADDR"
	// EnvAPIURL is the environment variable that overrides the CLI API base URL.
	EnvAPIURL = "SHIPYARD_API_URL"
)

// Config is the runtime configuration for shipyardd.
type Config struct {
	// Addr is the TCP address the HTTP server listens on, such as ":8080".
	Addr string
}

// Load reads shipyardd configuration from the environment.
func Load() Config {
	addr := strings.TrimSpace(os.Getenv(EnvAddr))
	if addr == "" {
		addr = DefaultAddr
	}
	return Config{Addr: addr}
}
