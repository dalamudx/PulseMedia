package media_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Decode independently authored literals only. Neither upstream code nor its
// intRange helper runs, and no PulseMedia recognizer participates in this oracle.
var virenFullBlock = regexp.MustCompile(`(?s)\{\s+title:\s+('(?:\\.|[^'\\])*'|"(?:\\.|[^"\\])*")\s*,\s+expected:\s*\{(.*?)\n\s+\}\n\s+\}`)
var virenFullQuoted = regexp.MustCompile(`'(?:\\.|[^'\\])*'|"(?:\\.|[^"\\])*"`)
var virenFullKey = regexp.MustCompile(`(?m)^([ \t]*)([A-Za-z][A-Za-z0-9]*):`)

func decodeVirenFullDictionary(body string) (map[string]json.RawMessage, error) {
	var decodeErr error
	body = virenFullQuoted.ReplaceAllStringFunc(body, func(s string) string {
		v, err := decodeVirenLiteral(s)
		if err != nil {
			decodeErr = err
			return s
		}
		b, err := json.Marshal(v)
		if err != nil {
			decodeErr = err
			return s
		}
		return string(b)
	})
	if decodeErr != nil {
		return nil, decodeErr
	}
	body = virenFullKey.ReplaceAllString(body, `${1}"${2}":`)
	body = strings.TrimSuffix(strings.TrimSpace(body), ",")
	var labels map[string]json.RawMessage
	if err := json.Unmarshal([]byte("{"+body+"}"), &labels); err != nil {
		return nil, fmt.Errorf("only literal expected dictionaries admitted: %w", err)
	}
	var title string
	if err := json.Unmarshal(labels["title"], &title); err != nil || title == "" {
		return nil, fmt.Errorf("independent nonempty literal title required")
	}
	if raw, ok := labels["year"]; ok {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return nil, fmt.Errorf("original year must be a literal string")
		}
		n, err := strconv.Atoi(text)
		if err != nil || len(text) != 4 || n < 1 {
			return nil, fmt.Errorf("range or computed year cannot become a scalar year")
		}
		labels["year"], _ = json.Marshal(n)
	}
	return labels, nil
}

func originalVirenFullLabels(manifest []byte, input string) (*corpusEntry, error) {
	var p struct {
		Source    string `json:"source_id"`
		Status    string `json:"review_status"`
		Count     int    `json:"reviewed_entries"`
		Gold      *bool  `json:"formal_golden_acceptance"`
		Authority *bool  `json:"coverage_authority"`
	}
	if err := json.Unmarshal(manifest, &p); err != nil {
		return nil, err
	}
	if p.Source != "viren-full-main-original" || p.Status != "unreviewed_verbatim_source" || p.Count != 0 || p.Gold == nil || *p.Gold || p.Authority == nil || *p.Authority {
		return nil, fmt.Errorf("pending source cannot gain authority")
	}
	raw, err := os.ReadFile("../../../testdata/releases/upstream-mit/viren-full-main-original.test.ts")
	if err != nil {
		return nil, err
	}
	license, err := os.ReadFile("../../../testdata/releases/review-inputs/viren-title-original-assertions.LICENSE")
	if err != nil {
		return nil, err
	}
	for _, c := range []struct {
		data []byte
		hash string
	}{{raw, "4b25201f0f2e4a04929ad154b91252f8bc4c9a24a36aac322987e12e3d79f5cd"}, {license, "5c44be7aac21777dcc84933675ee7f0b08c736aeaed6c78aedd0e3d7a6856d3a"}} {
		h := sha256.Sum256(c.data)
		if hex.EncodeToString(h[:]) != c.hash {
			return nil, fmt.Errorf("fixed source or licence changed")
		}
	}
	var found map[string]json.RawMessage
	for _, m := range virenFullBlock.FindAllSubmatch(raw, -1) {
		name, err := decodeVirenLiteral(string(m[1]))
		if err != nil {
			return nil, err
		}
		if name != input {
			continue
		}
		if found != nil {
			return nil, fmt.Errorf("duplicate original input")
		}
		found, err = decodeVirenFullDictionary(string(m[2]))
		if err != nil {
			return nil, err
		}
	}
	if found == nil {
		return nil, fmt.Errorf("not an original literal full-source input")
	}
	return mappedPTTMainReviewLabels(found, input)
}

func TestVirenFullLiteralAdmissionRefusesComputedAndPartialLabels(t *testing.T) {
	for _, body := range []string{`title: 'Show',
 episodes: intRange(1, 1071)`, `title: '',
 episodes: [1]`, `title: 'Show',
 year: '2000-2020'`, `title: 'Show',
 year: parseInt('2020')`} {
		if _, err := decodeVirenFullDictionary(body); err == nil {
			t.Fatalf("nonliteral/incomplete source admitted: %s", body)
		}
	}
	labels, err := decodeVirenFullDictionary(`title: "The Killer's Game",
 year: '2024',
 seasons: [1, 2],
 episodes: [1, 2, 3]`)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(labels["year"], []byte("2024")) || !bytes.Equal(labels["episodes"], []byte("[1, 2, 3]")) {
		t.Fatal("source declarations lost")
	}
	entry, err := mappedPTTMainReviewLabels(labels, "literal-input")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(entry.Expected["season"], []byte("[1, 2]")) || !bytes.Equal(entry.Expected["episode"], labels["episodes"]) {
		t.Fatal("full independent scope truncated")
	}
	for _, bad := range []string{`[1, 1]`, `[0]`, `null`, `[1.5]`} {
		labels["episodes"] = json.RawMessage(bad)
		if _, err := mappedPTTMainReviewLabels(labels, "literal-input"); err == nil {
			t.Fatalf("invalid original coverage admitted: %s", bad)
		}
	}
}
