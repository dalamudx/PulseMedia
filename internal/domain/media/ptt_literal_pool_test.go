package media_test

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type pttLiteralPool struct {
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
			Path     string                     `json:"source_path"`
			Line     int                        `json:"source_line"`
			Function string                     `json:"test_function"`
			Fields   map[string]json.RawMessage `json:"original_fields"`
		} `json:"observations"`
	} `json:"entries"`
}

func decodePTTLiteralPool(raw []byte) (pttLiteralPool, error) {
	var p pttLiteralPool
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&p); err != nil {
		return p, err
	}
	if p.Schema != "pulse.python-literal-fixtures.v1" || p.Source != "ptt-original-assertions" || p.License != "MIT" || p.Status != "unreviewed" || p.Accepted == nil || *p.Accepted || p.Authority == nil || *p.Authority || !strings.Contains(p.Scope, "not full coverage") || p.Count != len(p.Entries) {
		return p, fmt.Errorf("raw fixture cannot gain golden or execution authority")
	}
	return p, nil
}

func TestPinnedPTTLiteralInputsRemainUnreviewedAndReproducible(t *testing.T) {
	root := "../../../testdata/releases/review-inputs"
	raw, err := os.ReadFile(filepath.Join(root, "ptt-original-assertions.json"))
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(raw)
	if hex.EncodeToString(h[:]) != "a317bd47b4622bc678c566d8e284977d69ca40ace814689f3b70fa90e568dbba" {
		t.Fatal("original fixture labels changed")
	}
	pool, err := decodePTTLiteralPool(raw)
	if err != nil {
		t.Fatal(err)
	}
	if pool.Count != 646 || pool.Provenance.Repository != "dreulavelle/PTT" || pool.Provenance.Commit != "88429bb90acef55673f421f45038878809b1e577" {
		t.Fatal("unreviewed origin or count changed")
	}
	srcRoot := filepath.Join(root, "ptt-original-assertions.sources")
	rawManifest, err := os.ReadFile(filepath.Join(srcRoot, "source-manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	h = sha256.Sum256(rawManifest)
	if hex.EncodeToString(h[:]) != "2385d476fe552a7d2be2a6a3eb59b6856f4de0bedfbe05c650a37b0a6cb5db50" || pool.Provenance.Manifest != hex.EncodeToString(h[:]) {
		t.Fatal("immutable source manifest changed")
	}
	var manifest struct {
		Repository  string `json:"repository"`
		Commit      string `json:"commit"`
		License     string `json:"license"`
		LicenseHash string `json:"license_sha256"`
		Sources     []struct {
			Path string `json:"path"`
			Hash string `json:"sha256"`
			Blob string `json:"git_blob_sha1"`
		} `json:"sources"`
	}
	d := json.NewDecoder(bytes.NewReader(rawManifest))
	d.DisallowUnknownFields()
	if err = d.Decode(&manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Repository != pool.Provenance.Repository || manifest.Commit != pool.Provenance.Commit || manifest.License != "MIT" || len(manifest.Sources) != 35 {
		t.Fatal("source provenance mismatch")
	}
	notice, err := os.ReadFile(filepath.Join(srcRoot, "LICENSE"))
	if err != nil {
		t.Fatal(err)
	}
	h = sha256.Sum256(notice)
	if hex.EncodeToString(h[:]) != "285e18dd1c15cbfa29380cd73ec9804bfebae088c54be6cd3b3d1ca765c25370" || pool.Provenance.License != hex.EncodeToString(h[:]) || manifest.LicenseHash != hex.EncodeToString(h[:]) || !bytes.Contains(notice, []byte("Copyright (c) 2024 Spoked")) {
		t.Fatal("original MIT notice changed")
	}
	lines := map[string]int{}
	for _, src := range manifest.Sources {
		if !strings.HasPrefix(src.Path, "tests/test_") || !strings.HasSuffix(src.Path, ".py") || strings.Contains(strings.TrimPrefix(src.Path, "tests/"), "/") {
			t.Fatal("unsafe original source path")
		}
		data, err := os.ReadFile(filepath.Join(srcRoot, filepath.Base(src.Path)+".source"))
		if err != nil {
			t.Fatal(err)
		}
		h = sha256.Sum256(data)
		if hex.EncodeToString(h[:]) != src.Hash {
			t.Fatal("original bytes changed", src.Path)
		}
		blob := sha1.Sum(append([]byte(fmt.Sprintf("blob %d\x00", len(data))), data...))
		if hex.EncodeToString(blob[:]) != src.Blob {
			t.Fatal("Git blob identity changed", src.Path)
		}
		lines[src.Path] = bytes.Count(data, []byte("\n")) + 1
	}
	names := map[string]bool{}
	union := map[string]bool{}
	for _, r := range readMITCorpora(t) {
		union[r.Name] = true
	}
	for _, id := range []string{"rust-ptn", "titleparser", "rank-torrent-name"} {
		b, err := os.ReadFile(filepath.Join(root, id+".json"))
		if err != nil {
			t.Fatal(err)
		}
		old, err := decodeUnapprovedPool(b)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range old.Entries {
			union[r.Name] = true
		}
	}
	baseline := len(union)
	observations := 0
	for _, row := range pool.Entries {
		if strings.TrimSpace(row.Name) == "" || names[row.Name] || len(row.Observations) == 0 || strings.Contains(row.Name, "://") {
			t.Fatal("duplicate or invalid raw input")
		}
		names[row.Name] = true
		union[row.Name] = true
		for _, o := range row.Observations {
			if o.Line < 1 || o.Line > lines[o.Path] || !strings.HasPrefix(o.Function, "test_") || len(o.Fields) == 0 {
				t.Fatal("missing original source location")
			}
			for field, value := range o.Fields {
				if !strings.HasPrefix(field, "expect") && field != "should_have_region" {
					t.Fatal("original parameter renamed", field)
				}
				if !json.Valid(value) {
					t.Fatal("invalid original literal label")
				}
			}
			observations++
		}
	}
	if baseline != 780 || len(union) != 1193 || observations != 708 {
		t.Fatal("distinct raw-input or observation count changed", baseline, len(union), observations)
	}
	if len(pool.Skipped) != 2 {
		t.Fatal("dynamic fixture lists cannot silently enter")
	}
	for _, s := range pool.Skipped {
		if lines[s.Path] == 0 || s.Line < 1 || s.Reason != "nonliteral_parametrize" {
			t.Fatal("invalid dynamic skip record")
		}
	}
}

// Partial original fields are translated only when explicitly present; missing
// fields are never filled with guessed type/season/episode/quality values. This
// helper returns labels, not permissions or production recognition results.
func originalPTTReviewLabels(pool pttLiteralPool, input string) (*corpusEntry, error) {
	for _, row := range pool.Entries {
		if row.Name != input {
			continue
		}
		labels := map[string]json.RawMessage{}
		mapping := map[string]string{"expected_title": "title", "expected_year": "year", "expected_seasons": "season", "expected_episode": "episode", "expected_resolution": "screen_size"}
		for _, o := range row.Observations {
			for key, target := range mapping {
				value, ok := o.Fields[key]
				if !ok || bytes.Equal(value, []byte("null")) {
					continue
				}
				if prior, ok := labels[target]; ok && !bytes.Equal(prior, value) {
					return nil, fmt.Errorf("conflicting original %s assertions", target)
				}
				labels[target] = value
			}
			for key, target := range map[string]string{"expected_quality": "source", "expected_codec": "video_codec"} {
				value, ok := o.Fields[key]
				if !ok || bytes.Equal(value, []byte("null")) {
					continue
				}
				var word string
				if err := json.Unmarshal(value, &word); err != nil {
					return nil, err
				}
				var label string
				if key == "expected_quality" {
					switch word {
					case "BluRay", "BDRip", "BRRip", "REMUX":
						label = "Blu-ray"
					case "WEB", "WEB-DL", "WEBRip":
						label = "Web"
					case "HDTV":
						label = "HDTV"
					case "DVD", "DVDRip", "DVD-R":
						label = "DVD"
					default:
						return nil, fmt.Errorf("unreviewed original source vocabulary %q", word)
					}
				} else {
					switch word {
					case "avc":
						label = "H.264"
					case "hevc":
						label = "H.265"
					case "xvid":
						label = "Xvid"
					case "mpeg":
						label = "MPEG"
					case "vp8":
						label = "VP8"
					default:
						return nil, fmt.Errorf("unreviewed original codec vocabulary %q", word)
					}
				}
				encoded, _ := json.Marshal(label)
				if prior, ok := labels[target]; ok && !bytes.Equal(prior, encoded) {
					return nil, fmt.Errorf("conflicting original %s assertions", target)
				}
				labels[target] = encoded
			}
		}
		if _, ok := labels["title"]; !ok {
			return nil, fmt.Errorf("complete review needs an explicit independent title assertion")
		}
		return &corpusEntry{Name: row.Name, Expected: labels}, nil
	}
	return nil, fmt.Errorf("not an original pinned PTT input")
}

func TestPTTFullReviewSourceLabelsNeverInferOrOverwriteFacts(t *testing.T) {
	makePool := func(fields ...map[string]json.RawMessage) pttLiteralPool {
		raw := `{"schema":"pulse.python-literal-fixtures.v1","entries":[{"release_name":"Original","observations":[]}]}`
		var p pttLiteralPool
		_ = json.Unmarshal([]byte(raw), &p)
		for _, f := range fields {
			b, _ := json.Marshal(map[string]any{"original_fields": f})
			var value struct {
				Path     string                     `json:"source_path"`
				Line     int                        `json:"source_line"`
				Function string                     `json:"test_function"`
				Fields   map[string]json.RawMessage `json:"original_fields"`
			}
			_ = json.Unmarshal(b, &value)
			p.Entries[0].Observations = append(p.Entries[0].Observations, value)
		}
		return p
	}
	title := json.RawMessage(`"Original"`)
	p := makePool(map[string]json.RawMessage{"expected_title": title, "expected_audio": json.RawMessage(`["DTS Lossless"]`), "expected_episode": json.RawMessage(`[3,5]`)})
	row, err := originalPTTReviewLabels(p, "Original")
	if err != nil {
		t.Fatal(err)
	}
	if len(row.Expected) != 2 || string(row.Expected["episode"]) != "[3,5]" {
		t.Fatal("partial labels invented facts", row)
	}
	if _, err := originalPTTReviewLabels(p, "not-original"); err == nil {
		t.Fatal("synthetic input accepted")
	}
	for _, tc := range []struct {
		name   string
		fields []map[string]json.RawMessage
	}{
		{"missing-title", []map[string]json.RawMessage{{"expected_audio": json.RawMessage(`["DTS Lossless"]`)}}},
		{"conflicting-title", []map[string]json.RawMessage{{"expected_title": title}, {"expected_title": json.RawMessage(`"Other"`)}}},
		{"unknown-codec", []map[string]json.RawMessage{{"expected_title": title, "expected_codec": json.RawMessage(`"new-codec"`)}}},
		{"unknown-source", []map[string]json.RawMessage{{"expected_title": title, "expected_quality": json.RawMessage(`"new-source"`)}}},
		{"conflicting-coverage", []map[string]json.RawMessage{{"expected_title": title, "expected_episode": json.RawMessage(`[3]`)}, {"expected_episode": json.RawMessage(`[5]`)}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := originalPTTReviewLabels(makePool(tc.fields...), "Original"); err == nil {
				t.Fatal("original label guard weakened")
			}
		})
	}
}

func TestPTTPartialLabelsCannotClaimGoldenOrExecutionAuthority(t *testing.T) {
	raw, err := os.ReadFile("../../../testdata/releases/review-inputs/ptt-original-assertions.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		field string
		value any
	}{{"formal_golden_acceptance", true}, {"coverage_authority", true}, {"coverage_authority", nil}, {"formal_golden_acceptance", nil}, {"review_status", "approved"}, {"unique_entries", 1000}, {"unit_keys", []string{"S01E01"}}} {
		t.Run(tc.field+fmt.Sprint(tc.value), func(t *testing.T) {
			var p map[string]any
			if err := json.Unmarshal(raw, &p); err != nil {
				t.Fatal(err)
			}
			p[tc.field] = tc.value
			b, _ := json.Marshal(p)
			if _, err := decodePTTLiteralPool(b); err == nil {
				t.Fatal("partial source labels acquired authority")
			}
		})
	}
}
