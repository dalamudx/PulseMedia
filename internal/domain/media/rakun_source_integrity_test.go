package media_test

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestRakunFixedSourceStaysPinnedAndUnexecuted(t *testing.T) {
	root := "../../../testdata/releases/"
	manifestBytes, err := os.ReadFile(root + "review-inputs/rakun-full-original.manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		SourceID     string            `json:"source_id"`
		License      string            `json:"license"`
		ReviewStatus string            `json:"review_status"`
		Accepted     bool              `json:"formal_golden_acceptance"`
		Authority    bool              `json:"coverage_authority"`
		Reviewed     int               `json:"reviewed_entries"`
		Count        int               `json:"literal_input_count"`
		Provenance   map[string]string `json:"provenance"`
	}
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.SourceID != "rakun-full-original" || manifest.License != "MIT" || manifest.ReviewStatus != "partially_reviewed_verbatim_source" || manifest.Accepted || manifest.Authority || manifest.Reviewed != 50 || manifest.Count != 93 {
		t.Fatal("raw source cannot promote itself to reviewed coverage", manifest)
	}
	for key, want := range map[string]string{
		"repository":     "https://github.com/lowlighter/rakun",
		"commit":         "9cb4960da05b79c4bcea3eafc1b5f2ca6c816b56",
		"path":           "tests/cases/all.js",
		"source_file":    "../upstream-mit/rakun-full-original.test.js",
		"assertion_file": "../upstream-mit/rakun-full-original.tester.js",
		"license_file":   "rakun-full-original.LICENSE",
	} {
		if manifest.Provenance[key] != want {
			t.Fatal("fixed original provenance changed", key)
		}
	}
	var fixture, assertion []byte
	for _, snap := range []struct{ path, key, sum string }{
		{"upstream-mit/rakun-full-original.test.js", "source_sha256", "888c44b6f29deeb52fcf2ced59873326973881aae4976c725c38d258ba33f91c"},
		{"upstream-mit/rakun-full-original.tester.js", "assertion_sha256", "56a8bcd6273986354ffc61d0d933d8567ac785737fceaa4268c20113682ed569"},
		{"review-inputs/rakun-full-original.LICENSE", "license_sha256", "d14c13422112e483e91faf496cc0bfd75ec072766053a340b2ff9afc483fb468"},
	} {
		body, err := os.ReadFile(root + snap.path)
		if err != nil {
			t.Fatal(err)
		}
		if fmt.Sprintf("%x", sha256.Sum256(body)) != snap.sum || manifest.Provenance[snap.key] != snap.sum {
			t.Fatal("original fixture or license bytes changed", snap.path)
		}
		switch snap.key {
		case "source_sha256":
			fixture = body
		case "assertion_sha256":
			assertion = body
		}
	}
	if !strings.Contains(string(assertion), "expect(rakun.parse(testable.filename)).toEqual(testable)") || !strings.Contains(string(fixture), "].forEach(tester)") {
		t.Fatal("literal objects no longer have an original equality assertion")
	}
	inputs := regexp.MustCompile(`(?m)^\s+filename:\s*("(?:\\.|[^"\\])*")\s*,?$`).FindAllSubmatch(fixture, -1)
	titles := regexp.MustCompile(`(?m)^\s+name:\s*"(?:\\.|[^"\\])*"\s*,?$`).FindAll(fixture, -1)
	if len(inputs) != manifest.Count || len(titles) != manifest.Count {
		t.Fatal("raw literal/title declarations changed", len(inputs), len(titles))
	}
	seen := map[string]bool{}
	for _, match := range inputs {
		var raw string
		if err := json.Unmarshal(match[1], &raw); err != nil || strings.TrimSpace(raw) == "" || seen[raw] {
			t.Fatal("nonliteral, duplicate or empty source input", string(match[1]), err)
		}
		seen[raw] = true
	}
	goldBytes, err := os.ReadFile(root + "reviewed-full.json")
	if err != nil {
		t.Fatal(err)
	}
	var gold struct {
		Entries []struct {
			Source string `json:"source"`
			Input  string `json:"input"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(goldBytes, &gold); err != nil {
		t.Fatal(err)
	}
	reviewed := 0
	for _, row := range gold.Entries {
		if row.Source != manifest.SourceID {
			continue
		}
		reviewed++
		if !seen[row.Input] {
			t.Fatal("reviewed row is absent from original literals", row.Input)
		}
		if _, err := originalRakunReviewLabels(row.Input); err != nil {
			t.Fatal(err)
		}
	}
	if reviewed != manifest.Reviewed {
		t.Fatal("source audit count differs from individually reviewed rows")
	}
}
