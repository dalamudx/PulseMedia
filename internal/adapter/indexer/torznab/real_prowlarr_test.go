package torznab_test

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Only this task's explicitly selected container configuration supplies the key.
// It never appears in command arguments, test output or management API URLs.
type m4Prowlarr struct {
	ctx    context.Context
	client *http.Client
	key    string
}

func openM4Prowlarr(t *testing.T) *m4Prowlarr {
	t.Helper()
	root := os.Getenv("PULSE_M4_SERVICES_DIR")
	if root == "" {
		t.Skip("explicit owned Podman M4 service directory required")
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil || canonical != root || filepath.Dir(root) != "/data/temp" || !strings.HasPrefix(filepath.Base(root), "pulse-m4-services.") {
		t.Fatal("explicit canonical M4 service directory required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	label, err := exec.CommandContext(ctx, "podman", "inspect", "--format", "{{index .Config.Labels \"io.pulsemedia.state\"}}", "pulsemedia-m4-prowlarr").Output()
	if err != nil || strings.TrimSpace(string(label)) != root {
		t.Fatal("refusing Prowlarr outside the explicit M4 service root")
	}
	config := filepath.Join(root, "prowlarr", "config.xml")
	resolved, err := filepath.EvalSymlinks(config)
	if err != nil || resolved != config {
		t.Fatal("owned Prowlarr configuration path is not canonical")
	}
	before, err := os.Lstat(config)
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm() != 0o600 {
		t.Fatal("Prowlarr configuration is not a 0600 regular file")
	}
	file, err := os.Open(config)
	if err != nil {
		t.Fatal("cannot open owned Prowlarr configuration")
	}
	defer func() { _ = file.Close() }()
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) || !after.Mode().IsRegular() || after.Mode().Perm() != 0o600 {
		t.Fatal("owned Prowlarr configuration or permissions changed")
	}
	var secrets struct {
		APIKey string `xml:"ApiKey"`
	}
	if err := xml.NewDecoder(io.LimitReader(file, 65537)).Decode(&secrets); err != nil || len(secrets.APIKey) < 16 {
		t.Fatal("owned Prowlarr API credential unavailable")
	}
	return &m4Prowlarr{ctx: ctx, key: secrets.APIKey, client: &http.Client{Timeout: 45 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (p *m4Prowlarr) request(t *testing.T, method, endpoint string, body []byte) (int, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(p.ctx, method, "http://127.0.0.1:19696"+endpoint, bytes.NewReader(body))
	if err != nil {
		t.Fatal("Prowlarr request construction failed")
	}
	req.Header.Set("X-Api-Key", p.key)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := p.client.Do(req)
	if err != nil {
		t.Fatal("real Prowlarr request failed (URL and credentials withheld)")
	}
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(response.Body, (8<<20)+1))
	if err != nil || len(data) > 8<<20 {
		t.Fatal("Prowlarr response could not be bounded")
	}
	return response.StatusCode, data
}

func (p *m4Prowlarr) read(t *testing.T, endpoint string) []byte {
	t.Helper()
	status, data := p.request(t, http.MethodGet, endpoint, nil)
	if status != http.StatusOK {
		t.Fatalf("real Prowlarr HTTP %d", status)
	}
	return data
}

func TestM4RealProwlarr(t *testing.T) {
	p := openM4Prowlarr(t)
	var status struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(p.read(t, "/api/v1/system/status"), &status); err != nil || status.Version == "" {
		t.Fatal("real Prowlarr status invalid")
	}
	var indexers []struct {
		ID     int  `json:"id"`
		Enable bool `json:"enable"`
	}
	if err := json.Unmarshal(p.read(t, "/api/v1/indexer"), &indexers); err != nil || indexers == nil {
		t.Fatal("real configured-indexer snapshot invalid")
	}
	enabled := 0
	for _, source := range indexers {
		if source.Enable {
			enabled++
		}
	}
	t.Logf("M4_PROWLARR_API_READY version=%s configured_indexers=%d enabled_indexers=%d", status.Version, len(indexers), enabled)
	t.Run("AuthorizedTorznabCapabilities", func(t *testing.T) {
		if enabled == 0 {
			t.Skip("authorized real indexer source has not been configured; API readiness does not establish Torznab search acceptance")
		}
		for _, source := range indexers {
			if !source.Enable {
				continue
			}
			if source.ID <= 0 {
				t.Fatal("invalid real source identity")
			}
			caps := p.read(t, "/"+strconv.Itoa(source.ID)+"/api?t=caps")
			var parsed struct{ XMLName xml.Name }
			if err := xml.Unmarshal(caps, &parsed); err != nil || parsed.XMLName.Local != "caps" {
				t.Fatal("real authorized Torznab capabilities invalid")
			}
		}
		t.Logf("M4_AUTHORIZED_TORZNAB_CAPABILITIES_PASSED sources=%d", enabled)
	})
}
