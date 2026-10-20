package indexer_test

import (
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/indexer"
)

func TestBuiltinTemplatesCountAndUniqueness(t *testing.T) {
	templates := indexer.BuiltinTemplates()
	if len(templates) < 20 {
		t.Fatalf("expected at least 20 templates, got %d", len(templates))
	}

	seenIDs := make(map[string]bool)
	seenNames := make(map[string]bool)
	hasProwlarr := false
	protocols := make(map[indexer.Protocol]int)

	for _, tmpl := range templates {
		if tmpl.ID == "" || seenIDs[tmpl.ID] {
			t.Errorf("duplicate or empty ID: %q", tmpl.ID)
		}
		seenIDs[tmpl.ID] = true

		if tmpl.Name == "" || seenNames[tmpl.Name] {
			t.Errorf("duplicate or empty Name: %q", tmpl.Name)
		}
		seenNames[tmpl.Name] = true

		if tmpl.Domain == "" {
			t.Errorf("template %s has empty Domain", tmpl.ID)
		}
		if !strings.HasPrefix(tmpl.DefaultURL, "http://") && !strings.HasPrefix(tmpl.DefaultURL, "https://") {
			t.Errorf("template %s DefaultURL must be valid HTTP(S): %q", tmpl.ID, tmpl.DefaultURL)
		}

		if len(tmpl.RecommendedCategories) == 0 {
			t.Errorf("template %s has empty RecommendedCategories", tmpl.ID)
		}
		for _, cat := range tmpl.RecommendedCategories {
			if cat <= 0 {
				t.Errorf("template %s has invalid category code: %d", tmpl.ID, cat)
			}
		}

		if len(tmpl.CategoryMappings) == 0 {
			t.Errorf("template %s has empty CategoryMappings", tmpl.ID)
		}

		protocols[tmpl.Protocol]++
		if tmpl.ID == "prowlarr" {
			hasProwlarr = true
		}
	}

	if !hasProwlarr {
		t.Error("builtin templates must include Prowlarr")
	}
	if protocols[indexer.ProtocolTorznab] == 0 {
		t.Error("expected at least one Torznab template")
	}
	if protocols[indexer.ProtocolNexusPHP] == 0 {
		t.Error("expected at least one NexusPHP template")
	}
	if protocols[indexer.ProtocolRSS] == 0 {
		t.Error("expected at least one RSS template")
	}
}

func TestFindTemplate(t *testing.T) {
	tmpl, ok := indexer.FindTemplate("prowlarr")
	if !ok {
		t.Fatal("expected prowlarr template to be found")
	}
	if tmpl.Protocol != indexer.ProtocolTorznab {
		t.Errorf("expected torznab protocol, got %s", tmpl.Protocol)
	}

	// Verify defensive copying
	tmpl.RecommendedCategories[0] = 999999
	original, _ := indexer.FindTemplate("prowlarr")
	if original.RecommendedCategories[0] == 999999 {
		t.Error("FindTemplate did not defensively copy RecommendedCategories")
	}

	_, notFound := indexer.FindTemplate("nonexistent")
	if notFound {
		t.Error("expected nonexistent template to return false")
	}
}
