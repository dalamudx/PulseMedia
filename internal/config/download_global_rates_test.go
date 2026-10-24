package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDownloadGlobalRatesExplicitSwitch(t *testing.T) {
	file := filepath.Join(t.TempDir(), "rates.yaml")
	if err := os.WriteFile(file, []byte("download_global_rates:\n  enabled: true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(WithEnvLookup(func(string) (string, bool) { return "", false }))
	if err != nil || cfg.DownloadGlobalRates.Enabled {
		t.Fatal("existing deployment unexpectedly enabled native polling", err)
	}
	cfg, err = Load(WithConfigFile(file), WithEnvLookup(func(string) (string, bool) { return "", false }))
	if err != nil || !cfg.DownloadGlobalRates.Enabled {
		t.Fatal("YAML switch not honored", err)
	}
	cfg, err = Load(WithConfigFile(file), WithEnvLookup(func(k string) (string, bool) { return "false", k == "PULSE_DOWNLOAD_GLOBAL_RATES_ENABLED" }))
	if err != nil || cfg.DownloadGlobalRates.Enabled {
		t.Fatal("explicit disable did not override YAML", err)
	}
	cfg, err = Load(WithEnvLookup(func(k string) (string, bool) { return "true", k == "PULSE_DOWNLOAD_GLOBAL_RATES_ENABLED" }))
	if err != nil || !cfg.DownloadGlobalRates.Enabled {
		t.Fatal("environment enable not honored", err)
	}
	_, err = Load(WithEnvLookup(func(k string) (string, bool) {
		return "private-invalid-switch", k == "PULSE_DOWNLOAD_GLOBAL_RATES_ENABLED"
	}))
	if !errors.Is(err, ErrInvalidConfig) || strings.Contains(err.Error(), "private-invalid-switch") {
		t.Fatal("invalid switch accepted or exposed")
	}
}
