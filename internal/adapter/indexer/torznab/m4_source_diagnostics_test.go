package torznab_test

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

// This opt-in reads only existing local state; it never triggers an indexer query.
func TestM4ExistingSourceDiagnostics(t *testing.T) {
	if os.Getenv("PULSE_M4_SOURCE_DIAGNOSTICS") != "1" {
		t.Skip("explicit local source diagnostics required")
	}
	p := openM4Prowlarr(t)
	var sources []struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(p.read(t, "/api/v1/indexer"), &sources); err != nil {
		t.Fatal("invalid local indexer identity response")
	}
	ownedID := 0
	for _, source := range sources {
		if source.Name == m4ArchiveSourceName {
			ownedID = source.ID
		}
	}
	if ownedID == 0 {
		t.Fatal("dedicated lawful source not present")
	}
	var states []struct {
		IndexerID         int     `json:"indexerId"`
		InitialFailure    *string `json:"initialFailure"`
		MostRecentFailure *string `json:"mostRecentFailure"`
		DisabledTill      *string `json:"disabledTill"`
	}
	if err := json.Unmarshal(p.read(t, "/api/v1/indexerstatus"), &states); err != nil {
		t.Fatal("invalid local indexer status response")
	}
	for _, state := range states {
		if state.IndexerID == ownedID {
			t.Logf("M4_EXISTING_SOURCE_STATE source=%d initial_failure=%t recent_failure=%t disabled_until_present=%t", ownedID, state.InitialFailure != nil, state.MostRecentFailure != nil, state.DisabledTill != nil)
		}
	}
	var logs struct {
		Records []struct {
			Level     string `json:"level"`
			Logger    string `json:"logger"`
			Message   string `json:"message"`
			Exception string `json:"exception"`
		} `json:"records"`
	}
	if err := json.Unmarshal(p.read(t, "/api/v1/log?page=1&pageSize=30&sortDirection=descending&sortKey=time"), &logs); err != nil {
		t.Fatal("invalid local bounded diagnostic response")
	}
	urlPattern := regexp.MustCompile(`https?://[^\s<>"']+`)
	secretPattern := regexp.MustCompile(`(?i)(api[_-]?key|password|passkey|token|authorization|cookie)\s*[:=]\s*[^\s,;]+`)
	matched := 0
	for _, record := range logs.Records {
		text := record.Message + " " + record.Exception
		if !strings.Contains(strings.ToLower(text), "archive") {
			continue
		}
		text = strings.ReplaceAll(text, p.key, "[credential]")
		text = urlPattern.ReplaceAllString(text, "[URL]")
		text = secretPattern.ReplaceAllString(text, "$1=[credential]")
		text = strings.Join(strings.Fields(text), " ")
		if len(text) > 900 {
			text = text[:900]
		}
		t.Logf("M4_EXISTING_SOURCE_LOG level=%q logger=%q detail=%q", record.Level, record.Logger, text)
		matched++
	}
	t.Logf("M4_LOCAL_DIAGNOSTICS_COMPLETE source=%d matching_log_records=%d outbound_source_requests=0", ownedID, matched)
}
