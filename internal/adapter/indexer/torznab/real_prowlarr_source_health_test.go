package torznab_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// Diagnostics disclose only fixed reason classes, never provider messages or URLs.
func TestM4RealProwlarrSourceHealth(t *testing.T) {
	if os.Getenv("PULSE_M4_PROWLARR_DIAGNOSE") != "1" {
		t.Skip("explicit real source diagnostics required")
	}
	p := openM4Prowlarr(t)
	var logs struct {
		Records []struct {
			Time      string `json:"time"`
			Logger    string `json:"logger"`
			Message   string `json:"message"`
			Exception string `json:"exception"`
		} `json:"records"`
	}
	if json.Unmarshal(p.read(t, "/api/v1/log?page=1&pageSize=50&sortKey=time&sortDirection=descending"), &logs) != nil {
		t.Fatal("invalid log response")
	}
	for _, entry := range logs.Records {
		text := strings.ToLower(entry.Message + " " + entry.Exception)
		if !strings.Contains(text, "archive.org") && !strings.Contains(text, strings.ToLower(m4ArchiveSourceName)) {
			continue
		}
		reasons := []string{}
		for _, reason := range []string{"timed out", "timeout", "operation was canceled", "task was canceled", "unable to connect", "name or service not known", "connection refused", "429", "403", "502", "503", "504", "ssl", "certificate", "cloudflare", "rate limit", "http request failed", "invalid torrent", "no results", "test failed"} {
			if strings.Contains(text, reason) {
				reasons = append(reasons, reason)
			}
		}
		t.Logf("M4_SOURCE_DIAGNOSTIC time=%s logger=%s reasons=%q", entry.Time, entry.Logger, reasons)
	}
}
