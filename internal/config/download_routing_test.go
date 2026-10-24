package config

import (
	"errors"
	"github.com/google/uuid"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDownloadRoutingDeploymentConfiguration(t *testing.T) {
	if len(NewDefault().DownloadRouting.Rules) != 0 {
		t.Fatal("routing must be opt-in")
	}
	id := uuid.MustParse("00000000-0000-4000-8000-000000000001")
	file := filepath.Join(t.TempDir(), "routing.yaml")
	if err := os.WriteFile(file, []byte("download_routing:\n  rules:\n    - media_type: movie\n      downloader_id: "+id.String()+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	none := WithEnvLookup(func(string) (string, bool) { return "", false })
	cfg, err := Load(WithConfigFile(file), none)
	if err != nil || len(cfg.DownloadRouting.Rules) != 1 || cfg.DownloadRouting.Rules[0].DownloaderID != id {
		t.Fatal("YAML routing was not loaded", err)
	}
	cfg, err = Load(WithConfigFile(file), WithEnvLookup(func(k string) (string, bool) { return "[]", k == "PULSE_DOWNLOAD_ROUTE_RULES" }))
	if err != nil || len(cfg.DownloadRouting.Rules) != 0 {
		t.Fatal("environment must explicitly clear YAML rules", err)
	}
	cfg, err = Load(WithConfigFile(file), none)
	if err != nil {
		t.Fatal(err)
	}
	copied := cfg.DownloadRouting.RulesForSelection()
	copied[0].DownloaderID = uuid.Nil
	if cfg.DownloadRouting.Rules[0].DownloaderID != id {
		t.Fatal("policy shares caller-mutable configuration")
	}
	for _, raw := range []string{"", "null", "{}", "[{}]", "[] []", `[{"downloader_id":"private-invalid-value"}]`, `[{"downloader_id":"00000000-0000-4000-8000-000000000001","extra":true}]`, `[{"media_type":"series","downloader_id":"00000000-0000-4000-8000-000000000001"}]`, strings.Repeat("x", 16385)} {
		_, err = Load(WithConfigFile(file), WithEnvLookup(func(k string) (string, bool) { return raw, k == "PULSE_DOWNLOAD_ROUTE_RULES" }))
		if !errors.Is(err, ErrInvalidConfig) || strings.Contains(err.Error(), "private-invalid-value") {
			t.Fatal("invalid rules must fail without exposing input")
		}
	}
	cfg = NewDefault()
	cfg.DownloadRouting.Rules = []DownloadRoutingRule{{MediaType: "movie", DownloaderID: id}, {MediaType: "movie", DownloaderID: id}}
	if !errors.Is(cfg.Validate(), ErrInvalidConfig) {
		t.Fatal("duplicate routing scopes accepted")
	}
}
