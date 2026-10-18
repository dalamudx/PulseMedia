package torznab_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// Discovery is explicitly opt-in and outputs only public definition metadata.
func TestM4RealProwlarrSourceDefinitions(t *testing.T) {
	if os.Getenv("PULSE_M4_PROWLARR_DISCOVER") != "1" {
		t.Skip("explicit real source discovery required")
	}
	p := openM4Prowlarr(t)
	var definitions []struct {
		Name           string `json:"name"`
		Implementation string `json:"implementation"`
		ConfigContract string `json:"configContract"`
		DefinitionName string `json:"definitionName"`
		Privacy        string `json:"privacy"`
		Fields         []struct {
			Name  string          `json:"name"`
			Label string          `json:"label"`
			Value json.RawMessage `json:"value"`
			Type  string          `json:"type"`
		} `json:"fields"`
	}
	if json.Unmarshal(p.read(t, "/api/v1/indexer/schema"), &definitions) != nil {
		t.Fatal("invalid public definition schema")
	}
	for _, d := range definitions {
		if d.Privacy != "public" {
			continue
		}
		t.Logf("PUBLIC_DEFINITION name=%q implementation=%q definition=%q", d.Name, d.Implementation, d.DefinitionName)
		name := strings.ToLower(d.Name)
		if strings.Contains(name, "linux") || strings.Contains(name, "archive") || strings.Contains(name, "public domain") {
			for _, f := range d.Fields {
				t.Logf("PUBLIC_FIELD source=%q name=%q type=%q default=%s", d.Name, f.Name, f.Type, string(f.Value))
			}
		}
	}
}
