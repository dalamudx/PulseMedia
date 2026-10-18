package torznab_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// This is source feasibility discovery, never a caps/search/fetch acceptance.
// It deliberately leaves Archive and every configured source untouched. Run only:
// PULSE_M4_LAWFUL_SOURCE_DISCOVER=1 PULSE_M4_SERVICES_DIR=<owned root>
// go test ./internal/adapter/indexer/torznab -run '^TestM4LawfulIndependentSourceFeasibility$' -v
//
// Before enabling actual acceptance, record a specific publisher's redistribution
// evidence and independent metainfo bytes, original-info hash and complete file
// inventory. Production NewClient -> Capabilities/SearchPage -> get and
// FetchTorrent -> get must perform the actual calls. SearchPage uses t=movie;
// a source offering only generic search cannot satisfy that production contract.
// Never substitute t=search, cached data or the production torrent inspector for
// an independent oracle. M4-01 remains pending NAS and the complete fault matrix.
func TestM4LawfulIndependentSourceFeasibility(t *testing.T) {
	if os.Getenv("PULSE_M4_LAWFUL_SOURCE_DISCOVER") != "1" {
		t.Skip("explicit independent lawful source discovery required")
	}
	root := os.Getenv("PULSE_M4_SERVICES_DIR")
	if root == "" {
		t.Fatal("explicit owned M4 service directory required")
	}
	// Check before openM4Prowlarr reads credentials. Its existing ownership,
	// canonical-path, regular-file, identity and bounded-request checks still run.
	config := filepath.Join(root, "prowlarr", "config.xml")
	resolved, err := filepath.EvalSymlinks(config)
	if err != nil || resolved != config {
		t.Fatal("canonical non-symlink Prowlarr credential configuration required")
	}
	info, err := os.Lstat(config)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatal("regular Prowlarr credential configuration required")
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("M4_LAWFUL_SOURCE_BLOCKED credential_mode=%04o required=0600; no credential read, schema request or public source request performed", info.Mode().Perm())
	}
	p := openM4Prowlarr(t)
	var definitions []struct {
		Name           string `json:"name"`
		DefinitionName string `json:"definitionName"`
		Privacy        string `json:"privacy"`
		Fields         []struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"fields"`
	}
	// Exactly one bounded local schema read; public sites are never contacted by
	// this discovery test. These two options are candidates, not license proof.
	if json.Unmarshal(p.read(t, "/api/v1/indexer/schema"), &definitions) != nil {
		t.Fatal("invalid bounded Prowlarr definition schema")
	}
	for _, candidate := range []string{"linuxtracker", "academictorrents"} {
		found := false
		for _, definition := range definitions {
			if definition.DefinitionName != candidate {
				continue
			}
			if found {
				t.Fatal("ambiguous source definition")
			}
			found = true
			t.Logf("M4_LAWFUL_SOURCE_CANDIDATE definition=%q name=%q privacy=%q", candidate, definition.Name, definition.Privacy)
			for _, field := range definition.Fields {
				// Defaults and arbitrary values can contain credentials. Disclose
				// field names/types only; no public designation proves access rights.
				t.Logf("M4_LAWFUL_SOURCE_FIELD definition=%q name=%q type=%q", candidate, field.Name, field.Type)
			}
		}
		if !found {
			t.Logf("M4_LAWFUL_SOURCE_UNAVAILABLE definition=%q", candidate)
		}
	}
	t.Log("M4_LAWFUL_SOURCE_DISCOVERY_ONLY: publisher permission, actual caps/search/fetch and independent torrent comparison remain unverified")
}
