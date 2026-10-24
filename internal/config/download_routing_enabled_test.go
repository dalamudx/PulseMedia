package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDownloadRoutingExplicitEnableAndDisable(t *testing.T) {
	file := filepath.Join(t.TempDir(), "routing.yaml")
	if err := os.WriteFile(file, []byte("download_routing:\n  rules:\n    - media_type: movie\n      downloader_id: 00000000-0000-4000-8000-000000000001\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(WithConfigFile(file), WithEnvLookup(func(string) (string, bool) { return "", false }))
	if err != nil || !cfg.DownloadRouting.IsEnabled() {
		t.Fatal("configured rules should opt in", err)
	}
	cfg, err = Load(WithConfigFile(file), WithEnvLookup(func(k string) (string, bool) { return "false", k == "PULSE_DOWNLOAD_ROUTING_ENABLED" }))
	if err != nil || cfg.DownloadRouting.IsEnabled() {
		t.Fatal("explicit disable did not override configured rules", err)
	}
	cfg, err = Load(WithConfigFile(file), WithEnvLookup(func(k string) (string, bool) {
		switch k {
		case "PULSE_DOWNLOAD_ROUTING_ENABLED":
			return "true", true
		case "PULSE_DOWNLOAD_ROUTE_RULES":
			return "[]", true
		}
		return "", false
	}))
	if err != nil || !cfg.DownloadRouting.IsEnabled() || len(cfg.DownloadRouting.Rules) != 0 {
		t.Fatal("single-client automatic fallback cannot be enabled", err)
	}
	_, err = Load(WithConfigFile(file), WithEnvLookup(func(k string) (string, bool) { return "private-invalid-switch", k == "PULSE_DOWNLOAD_ROUTING_ENABLED" }))
	if !errors.Is(err, ErrInvalidConfig) || strings.Contains(err.Error(), "private-invalid-switch") {
		t.Fatal("invalid switch was accepted or leaked")
	}
}
