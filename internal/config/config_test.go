package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestConfigDefaults(t *testing.T) {
	// Isolate from host environment variables
	cfg, err := Load(WithEnvLookup(func(string) (string, bool) { return "", false }))
	if err != nil {
		t.Fatalf("expected valid default config, got: %v", err)
	}
	if cfg.Server.HTTPAddr != "127.0.0.1:8080" {
		t.Fatalf("unexpected default http_addr: %s", cfg.Server.HTTPAddr)
	}
	if cfg.DataDir != ".data" {
		t.Fatalf("unexpected default data_dir: %s", cfg.DataDir)
	}
	if cfg.LogLevel != "info" {
		t.Fatalf("unexpected default log_level: %s", cfg.LogLevel)
	}
}

func TestConfigPrecedence(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "pulsemedia-config-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() {
		_ = os.RemoveAll(tempDir)
	}()

	yamlContent := `
server:
  http_addr: "127.0.0.1:9090"
database:
  url: "postgres://fileuser:filepass@127.0.0.1:5432/fromfile?sslmode=disable"
data_dir: "/tmp/file-data"
log_level: "warn"
`
	configPath := filepath.Join(tempDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(yamlContent), 0600); err != nil {
		t.Fatalf("failed to write config.yaml: %v", err)
	}

	// 1. File overrides default
	cfg, err := Load(
		WithConfigFile(configPath),
		WithEnvLookup(func(string) (string, bool) { return "", false }),
	)
	if err != nil {
		t.Fatalf("failed to load from file: %v", err)
	}
	if cfg.Server.HTTPAddr != "127.0.0.1:9090" {
		t.Fatalf("expected file http_addr 127.0.0.1:9090, got: %s", cfg.Server.HTTPAddr)
	}
	if cfg.LogLevel != "warn" {
		t.Fatalf("expected file log_level warn, got: %s", cfg.LogLevel)
	}

	// 2. Env overrides file
	fakeEnv := map[string]string{
		"PULSE_HTTP_ADDR": "0.0.0.0:8000",
		"PULSE_LOG_LEVEL": "debug",
	}
	cfgWithEnv, err := Load(
		WithConfigFile(configPath),
		WithEnvLookup(func(k string) (string, bool) {
			val, ok := fakeEnv[k]
			return val, ok
		}),
	)
	if err != nil {
		t.Fatalf("failed to load with env: %v", err)
	}
	if cfgWithEnv.Server.HTTPAddr != "0.0.0.0:8000" {
		t.Fatalf("expected env http_addr 0.0.0.0:8000, got: %s", cfgWithEnv.Server.HTTPAddr)
	}
	if cfgWithEnv.LogLevel != "debug" {
		t.Fatalf("expected env log_level debug, got: %s", cfgWithEnv.LogLevel)
	}

	// 3. Explicit Option overrides env
	cfgExplicit, err := Load(
		WithConfigFile(configPath),
		WithEnvLookup(func(k string) (string, bool) {
			val, ok := fakeEnv[k]
			return val, ok
		}),
		WithHTTPAddr("127.0.0.1:7070"),
	)
	if err != nil {
		t.Fatalf("failed to load with explicit opt: %v", err)
	}
	if cfgExplicit.Server.HTTPAddr != "127.0.0.1:7070" {
		t.Fatalf("expected explicit opt http_addr 127.0.0.1:7070, got: %s", cfgExplicit.Server.HTTPAddr)
	}
}

func TestConfigValidation(t *testing.T) {
	// Invalid HTTP address
	cfg := NewDefault()
	cfg.Server.HTTPAddr = "invalid-no-port"
	if err := cfg.Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("expected ErrInvalidConfig for invalid addr, got: %v", err)
	}

	// Invalid log level
	cfg = NewDefault()
	cfg.LogLevel = "verbose"
	if err := cfg.Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("expected ErrInvalidConfig for invalid log level, got: %v", err)
	}

	// Invalid DB URL scheme
	cfg = NewDefault()
	cfg.Database.URL = "mysql://root:root@127.0.0.1:3306/db"
	if err := cfg.Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("expected ErrInvalidConfig for mysql url, got: %v", err)
	}

	// Valid postgres URLs
	cfg.Database.URL = "postgres://user:pass@127.0.0.1:5432/pulsemedia?sslmode=disable"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected valid postgres URL, got: %v", err)
	}
	cfg.Database.URL = "postgresql://user:pass@127.0.0.1:5432/pulsemedia?sslmode=verify-full"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected valid postgresql URL, got: %v", err)
	}
}
