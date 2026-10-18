package media_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type reviewInputPool struct {
	Schema     string `json:"schema"`
	SourceID   string `json:"source_id"`
	Provenance struct {
		Repository  string `json:"repository"`
		Commit      string `json:"commit"`
		Path        string `json:"path"`
		SourceHash  string `json:"source_sha256"`
		LicenseHash string `json:"license_sha256"`
		Count       int    `json:"unique_entries"`
	} `json:"provenance"`
	License   string `json:"license"`
	Status    string `json:"review_status"`
	Accepted  *bool  `json:"formal_golden_acceptance"`
	Authority *bool  `json:"coverage_authority"`
	Scope     string `json:"label_scope"`
	Entries   []struct {
		Name         string `json:"release_name"`
		Observations []struct {
			Line   int                        `json:"source_line"`
			Fields map[string]json.RawMessage `json:"original_fields"`
		} `json:"observations"`
	} `json:"entries"`
}

func decodeUnapprovedPool(raw []byte) (reviewInputPool, error) {
	var pool reviewInputPool
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&pool); err != nil {
		return pool, err
	}
	if pool.Schema != "pulse.release-review-input.v1" || pool.Status != "unreviewed" || pool.License != "MIT" || pool.Accepted == nil || *pool.Accepted || pool.Authority == nil || *pool.Authority || !strings.Contains(pool.Scope, "not full coverage") {
		return pool, fmt.Errorf("unreviewed input cannot acquire golden or coverage authority")
	}
	return pool, nil
}

func TestAdditionalMITReviewInputsPreserveProvenanceWithoutGoldenAuthority(t *testing.T) {
	root := "../../../testdata/releases/review-inputs"
	baseline := map[string]bool{}
	for _, row := range readMITCorpora(t) {
		baseline[row.Name] = true
	}
	if len(baseline) != 683 {
		t.Fatal("review original baseline before changing counts", len(baseline))
	}
	total := map[string]bool{}
	for name := range baseline {
		total[name] = true
	}
	for _, source := range []struct {
		id, hash, commit, sourceHash, licenseHash string
		count                                     int
	}{
		{"rust-ptn", "54d2bf95a42c771d4aa2445df2cc5bacfce04490ad1b3e3f510c0220dcc1ff4a", "6f4f42c2c711c53ed787f535d5b8216e6f47c9c5", "2b600d6f0a051cd651b9ff74a3b9d2e528b62ff755b59e0f53c06294eab47d1d", "555de1884458c8c87eb69a5be47724d7b8ea50a5a2ef73864c8bcfe1e40eb36c", 92},
		{"titleparser", "fe05d03f3ed4031e6b78b382f03aa864b140fc9d09451dbbdb0b6824066e98bb", "604b716c30c3338fbc1d73157f8000056a3b5132", "ba1eb5502901f0f369995fc9db6df5827e830042fbab9ce616cad348789b65f3", "3009a1211aea38b4e08a403c44cea0002fd531cc1906ed448e387c33c00a1475", 27},
		{"rank-torrent-name", "918a1cd2a7d85659ac6cc71250c5b06bf4d82177fdb00d25ea45beb724b6ae42", "bdb9973109eb489be831af0c39bdf9c27e3378ed", "12ca8d45d18389a1dc7fa5da64be06744b760e6055ddd490b3d29dcbe800c90f", "48f46d8b1ea2773ad67bad973cd99c413bb90473591f7d1e9ab65910275e270c", 24},
	} {
		raw, err := os.ReadFile(filepath.Join(root, source.id+".json"))
		if err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256(raw)
		if hex.EncodeToString(hash[:]) != source.hash {
			t.Fatal("original labels changed", source.id)
		}
		pool, err := decodeUnapprovedPool(raw)
		if err != nil {
			t.Fatal(err)
		}
		if pool.SourceID != source.id || pool.Provenance.Commit != source.commit || pool.Provenance.SourceHash != source.sourceHash || pool.Provenance.LicenseHash != source.licenseHash || pool.Provenance.Count != source.count || len(pool.Entries) != source.count {
			t.Fatal("provenance/count changed", source.id)
		}
		notice, err := os.ReadFile(filepath.Join(root, source.id+".LICENSE"))
		if err != nil {
			t.Fatal(err)
		}
		hash = sha256.Sum256(notice)
		if hex.EncodeToString(hash[:]) != source.licenseHash || !bytes.Contains(notice, []byte("Permission is hereby granted")) {
			t.Fatal("MIT notice changed", source.id)
		}
		seen := map[string]bool{}
		for _, row := range pool.Entries {
			if strings.TrimSpace(row.Name) == "" || seen[row.Name] || len(row.Observations) == 0 {
				t.Fatal("empty/duplicate/unlocated input", source.id, row.Name)
			}
			seen[row.Name] = true
			total[row.Name] = true
			for _, observation := range row.Observations {
				if observation.Line <= 0 || observation.Fields == nil {
					t.Fatal("missing original label position", row.Name)
				}
				for field, raw := range observation.Fields {
					if field == "confidence" || field == "reason" || field == "unit_keys" || field == "requires_review" {
						t.Fatal("not an original scalar fixture fact", field)
					}
					var value any
					if err := json.Unmarshal(raw, &value); err != nil {
						t.Fatal(err)
					}
					switch value.(type) {
					case nil, string, float64:
					default:
						t.Fatal("unexpected non-scalar original assertion", field, string(raw))
					}
				}
			}
		}
	}
	if len(total) != 780 {
		t.Fatal("distinct literal-input audit changed", len(total))
	}
	// These 97 novel unreviewed names are not 97 accepted examples. The reviewed
	// output contract remains limited to individually reviewed entries, and no constructor scalar can authorize IO.
	seed, err := os.ReadFile("../../../testdata/releases/reviewed-full.json")
	if err != nil {
		t.Fatal(err)
	}
	var reviewed struct {
		Count    int  `json:"reviewed_entries"`
		Accepted bool `json:"formal_golden_acceptance"`
	}
	if err := json.Unmarshal(seed, &reviewed); err != nil {
		t.Fatal(err)
	}
	if reviewed.Count != 1015 || !reviewed.Accepted {
		t.Fatal("individually reviewed corpus count or sample acceptance changed", reviewed)
	}
}

func TestReviewInputPoolRejectsAuthorityPromotion(t *testing.T) {
	raw, err := os.ReadFile("../../../testdata/releases/review-inputs/rust-ptn.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []struct{ from, to string }{
		{`"formal_golden_acceptance": false`, `"formal_golden_acceptance": true`},
		{`"coverage_authority": false`, `"coverage_authority": true`},
		{`"review_status": "unreviewed"`, `"review_status": "approved"`},
		{`"coverage_authority": false,`, ``},
		{`"formal_golden_acceptance": false,`, ``},
	} {
		mutated := bytes.Replace(raw, []byte(change.from), []byte(change.to), 1)
		if bytes.Equal(raw, mutated) {
			t.Fatal("mutation did not target input pool")
		}
		if _, err := decodeUnapprovedPool(mutated); err == nil {
			t.Fatal("unreviewed scalar data acquired authority", change.to)
		}
	}
}
