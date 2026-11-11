package config

import "testing"

func TestLoadDefaults(t *testing.T) {
	t.Setenv(EnvAddr, "")
	cfg := Load()
	if cfg.Addr != DefaultAddr {
		t.Fatalf("Addr = %q, want %q", cfg.Addr, DefaultAddr)
	}
}

func TestLoadFromEnv(t *testing.T) {
	t.Setenv(EnvAddr, " 127.0.0.1:9090 ")
	cfg := Load()
	if cfg.Addr != "127.0.0.1:9090" {
		t.Fatalf("Addr = %q, want trimmed env value", cfg.Addr)
	}
}

func TestLoadWhitespaceOnlyUsesDefault(t *testing.T) {
	t.Setenv(EnvAddr, "   ")
	cfg := Load()
	if cfg.Addr != DefaultAddr {
		t.Fatalf("Addr = %q, want %q", cfg.Addr, DefaultAddr)
	}
}
