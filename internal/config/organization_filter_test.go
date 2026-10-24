package config

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOrganizationMinimumMediaBytesConfiguration(t *testing.T) {
	if NewDefault().Organization.MinMediaBytes != 1 {
		t.Fatal("legacy minimum changed")
	}
	directory := t.TempDir()
	file := filepath.Join(directory, "config.yaml")
	if err := os.WriteFile(file, []byte("organization:\n  min_media_bytes: 209715200\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(WithConfigFile(file), WithEnvLookup(func(string) (string, bool) { return "", false }))
	if err != nil || cfg.Organization.MinMediaBytes != 209715200 {
		t.Fatal("YAML minimum", err)
	}
	cfg, err = Load(WithConfigFile(file), WithEnvLookup(func(k string) (string, bool) { return "52428800", k == "PULSE_ORGANIZATION_MIN_MEDIA_BYTES" }))
	if err != nil || cfg.Organization.MinMediaBytes != 52428800 {
		t.Fatal("environment must override YAML", err)
	}
	for _, raw := range []string{"", "0", "-1", "209715200.5", "secret-invalid-scalar", "9223372036854775808"} {
		t.Run("invalid_"+raw, func(t *testing.T) {
			_, err := Load(WithConfigFile(file), WithEnvLookup(func(k string) (string, bool) { return raw, k == "PULSE_ORGANIZATION_MIN_MEDIA_BYTES" }))
			if !errors.Is(err, ErrInvalidConfig) {
				t.Fatal("invalid minimum accepted")
			}
			if raw == "secret-invalid-scalar" && strings.Contains(err.Error(), raw) {
				t.Fatal("configuration error exposed input")
			}
		})
	}
	for _, value := range []int64{0, -1} {
		cfg := NewDefault()
		cfg.Organization.MinMediaBytes = value
		if !errors.Is(cfg.Validate(), ErrInvalidConfig) {
			t.Fatal("invalid constructed minimum accepted")
		}
	}
	cfg = NewDefault()
	cfg.Organization.MinMediaBytes = math.MaxInt64
	if err := cfg.Validate(); err != nil {
		t.Fatal("positive integer threshold rejected", err)
	}
}
