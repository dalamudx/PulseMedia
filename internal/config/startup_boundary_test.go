package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEmptyDataDirectoryDoesNotNormalizeToWorkingDirectory(t *testing.T) {
	for _, value := range []string{"", "   "} {
		t.Run("empty", func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(p, []byte("data_dir: \""+value+"\"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(WithConfigFile(p), WithEnvLookup(func(string) (string, bool) { return "", false }))
			if !errors.Is(err, ErrInvalidConfig) {
				t.Fatal("empty configured data directory was not rejected")
			}
		})
	}
}

func TestMalformedYAMLDoesNotExposeScalarValues(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	const marker = "probeLeak"
	if err := os.WriteFile(p, []byte("server:\n  read_timeout: "+marker+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(WithConfigFile(p), WithEnvLookup(func(string) (string, bool) { return "", false }))
	if err == nil {
		t.Fatal("invalid duration accepted")
	}
	for current := err; current != nil; current = errors.Unwrap(current) {
		if strings.Contains(current.Error(), marker) {
			t.Fatal("configuration scalar exposed through error chain")
		}
	}
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatal("parse error lacks typed invalid configuration contract")
	}
	if !strings.Contains(err.Error(), "config.yaml") {
		t.Fatal("safe file diagnostic lost")
	}
}

func TestInvalidConfigDiagnosticsDoNotEchoRawValues(t *testing.T) {
	const marker = "probeLeak"
	for _, field := range []string{"server.http_addr", "log_level"} {
		t.Run(field, func(t *testing.T) {
			cfg := NewDefault()
			if field == "server.http_addr" {
				cfg.Server.HTTPAddr = "https://user:" + marker + "@fixture.invalid"
			} else {
				cfg.LogLevel = marker
			}
			err := cfg.Validate()
			if !errors.Is(err, ErrInvalidConfig) {
				t.Fatal("invalid input accepted")
			}
			if strings.Contains(err.Error(), marker) || !strings.Contains(err.Error(), field) {
				t.Fatal("unsafe or unhelpful field diagnostic")
			}
		})
	}
}
