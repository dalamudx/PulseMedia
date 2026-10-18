package naming_test

import (
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/naming"
)

func TestNamingTemplateFormatting(t *testing.T) {
	movieTemplate := "{title} ({year})/{title} ({year}) [{resolution} {source}].mkv"
	vars := map[string]string{
		"title":      "Inception",
		"year":       "2010",
		"resolution": "2160p",
		"source":     "Remux",
	}

	rendered, err := naming.FormatNamingTemplate(movieTemplate, vars)
	if err != nil {
		t.Fatalf("format template failed: %v", err)
	}

	expected := "Inception (2010)/Inception (2010) [2160p Remux].mkv"
	if rendered != expected {
		t.Fatalf("expected %q, got %q", expected, rendered)
	}

	// Unsafe path traversal rejection
	unsafeTpl := "../../etc/{title}.mkv"
	_, err = naming.FormatNamingTemplate(unsafeTpl, vars)
	if err == nil {
		t.Fatal("expected unsafe template rejected")
	}
}
