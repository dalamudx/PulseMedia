package media_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

type videoLiteralPool struct {
	Schema     string `json:"schema"`
	Source     string `json:"source_id"`
	License    string `json:"license"`
	Status     string `json:"review_status"`
	Accepted   *bool  `json:"formal_golden_acceptance"`
	Authority  *bool  `json:"coverage_authority"`
	Scope      string `json:"label_scope"`
	Count      int    `json:"unique_entries"`
	Provenance struct {
		Repository string `json:"repository"`
		Commit     string `json:"commit"`
		Manifest   string `json:"manifest_sha256"`
		License    string `json:"license_sha256"`
	} `json:"provenance"`
	Skipped []struct {
		Path   string `json:"path"`
		Line   int    `json:"line"`
		Reason string `json:"reason"`
	} `json:"skipped_nonliteral"`
	Entries []struct {
		Name         string `json:"release_name"`
		Observations []struct {
			Path   string                     `json:"source_path"`
			Line   int                        `json:"source_line"`
			Table  string                     `json:"test_table"`
			Fields map[string]json.RawMessage `json:"original_fields"`
		} `json:"observations"`
	} `json:"entries"`
}

func decodeVideoLiteralPool(raw []byte) (videoLiteralPool, error) {
	var p videoLiteralPool
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if e := d.Decode(&p); e != nil {
		return p, e
	}
	var extra any
	if e := d.Decode(&extra); e != io.EOF {
		return p, fmt.Errorf("trailing fixture content")
	}
	if p.Schema != "pulse.typescript-literal-fixtures.v1" || p.Source != "video-filename-parser-original-assertions" || p.License != "MIT" || p.Status != "unreviewed" || p.Accepted == nil || *p.Accepted || p.Authority == nil || *p.Authority || !strings.Contains(p.Scope, "not full coverage") || p.Count != len(p.Entries) {
		return p, fmt.Errorf("literal fixture cannot acquire golden or coverage authority")
	}
	return p, nil
}
func videoSHA(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
func TestVideoLiteralFixturesPinnedOriginalsWithoutCoverageAuthority(t *testing.T) {
	root := "../../../testdata/releases/review-inputs"
	raw, e := os.ReadFile(filepath.Join(root, "video-filename-parser-original-assertions.json"))
	if e != nil {
		t.Fatal(e)
	}
	p, e := decodeVideoLiteralPool(raw)
	if e != nil {
		t.Fatal(e)
	}
	if videoSHA(raw) != "9e9c0dbef35ae22c6b2012c8ade6bdcc56afe2a3ab438971572d6a32f6344d82" {
		t.Fatal("original pending assertion JSON was rewritten")
	}
	if p.Count != 565 || p.Provenance.Repository != "scttcper/video-filename-parser" || p.Provenance.Commit != "84fece54bf097ddd2d7980523d00bb46126cf51f" {
		t.Fatal("review pin/count before changes")
	}
	sourceRoot := filepath.Join(root, "video-filename-parser-original-assertions.sources")
	manifest, e := os.ReadFile(filepath.Join(sourceRoot, "source-manifest.json"))
	if e != nil {
		t.Fatal(e)
	}
	const manifestHash = "23f289ed8fff3b1e2f2d8f944cade27b8073c9702f09e14205857ed46b448485"
	const licenseHash = "da337cfa918334bb9d4c84dd8610485b9a4fecd85b6b373028951028deb2b252"
	if videoSHA(manifest) != manifestHash || p.Provenance.Manifest != manifestHash {
		t.Fatal("pinned source manifest changed")
	}
	license, e := os.ReadFile(filepath.Join(sourceRoot, "LICENSE"))
	if e != nil {
		t.Fatal(e)
	}
	if videoSHA(license) != licenseHash || p.Provenance.License != licenseHash || !bytes.Contains(license, []byte("Copyright (c) Scott Cooper")) {
		t.Fatal("original MIT license changed")
	}
	var m struct {
		Sources []struct {
			Path  string `json:"path"`
			Local string `json:"local_name"`
			SHA   string `json:"sha256"`
		} `json:"sources"`
	}
	if e = json.Unmarshal(manifest, &m); e != nil {
		t.Fatal(e)
	}
	if len(m.Sources) != 17 {
		t.Fatal("source set changed")
	}
	lines := map[string]int{}
	for _, s := range m.Sources {
		if !strings.HasPrefix(s.Path, "test/") || s.Local != filepath.Base(s.Path)+".source" {
			t.Fatal("unsafe original path")
		}
		b, e := os.ReadFile(filepath.Join(sourceRoot, s.Local))
		if e != nil {
			t.Fatal(e)
		}
		if videoSHA(b) != s.SHA {
			t.Fatal("original source byte hash changed", s.Path)
		}
		lines[s.Path] = bytes.Count(b, []byte("\n")) + 1
	}
	seen := map[string]bool{}
	observations := 0
	hasUnresolvedAudio := false
	for _, r := range p.Entries {
		if r.Name == "" || seen[r.Name] || !utf8.ValidString(r.Name) || strings.ContainsRune(r.Name, '\uFFFD') || len(r.Observations) == 0 {
			t.Fatal("invalid or duplicate original input")
		}
		seen[r.Name] = true
		for _, o := range r.Observations {
			observations++
			if lines[o.Path] == 0 || o.Line < 1 || o.Line > lines[o.Path] || o.Table == "" {
				t.Fatal("observation lacks exact original location")
			}
			for _, key := range []string{"unit_keys", "media_unit_id", "file_id", "facts", "reservation_id", "coverage_authority"} {
				if _, ok := o.Fields[key]; ok {
					t.Fatal("pending field cannot masquerade as execution authority", key)
				}
			}
			if o.Path == "test/audioChannels.spec.ts" && bytes.Contains(o.Fields["channels"], []byte("symbol_ref")) && len(o.Fields["source"]) > 0 {
				hasUnresolvedAudio = true
			}
		}
	}
	if observations != 630 || !hasUnresolvedAudio {
		t.Fatal("partial getter labels silently canonicalized")
	}
	foundInvalid := false
	for _, s := range p.Skipped {
		if s.Path == "test/title.spec.ts" && s.Reason == "nonliteral_or_invalid_utf8" {
			foundInvalid = true
		}
	}
	if !foundInvalid {
		t.Fatal("malformed original UTF8 was silently repaired")
	}
	seed, e := os.ReadFile("../../../testdata/releases/reviewed-full.json")
	if e != nil {
		t.Fatal(e)
	}
	var gold struct {
		Count    int  `json:"reviewed_entries"`
		Accepted bool `json:"formal_golden_acceptance"`
	}
	if e = json.Unmarshal(seed, &gold); e != nil {
		t.Fatal(e)
	}
	if gold.Count != 1015 || !gold.Accepted {
		t.Fatal("individually reviewed corpus count or sample acceptance changed")
	}
}
func TestVideoLiteralFixtureAuthorityMutationDenied(t *testing.T) {
	raw, e := os.ReadFile("../../../testdata/releases/review-inputs/video-filename-parser-original-assertions.json")
	if e != nil {
		t.Fatal(e)
	}
	for _, key := range []string{"formal_golden_acceptance", "coverage_authority", "review_status", "schema", "unit_keys"} {
		t.Run(key, func(t *testing.T) {
			var m map[string]any
			if e := json.Unmarshal(raw, &m); e != nil {
				t.Fatal(e)
			}
			switch key {
			case "formal_golden_acceptance", "coverage_authority":
				m[key] = true
			case "review_status":
				m[key] = "reviewed"
			case "schema":
				m[key] = "pulse.release-review.v1"
			default:
				m[key] = []string{"movie"}
			}
			mut, e := json.Marshal(m)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = decodeVideoLiteralPool(mut); e == nil {
				t.Fatal("authority mutation accepted")
			}
		})
	}
	if _, e = decodeVideoLiteralPool(append(raw, []byte("{}")...)); e == nil {
		t.Fatal("trailing content accepted")
	}
}
