package media_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
)

// Only original literal assertions are read. Upstream JavaScript never runs.
const beastQuotedLiteral = `"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'`

var beastBlock = regexp.MustCompile(`(?ms)^    it\([^\n]+\{\n(.*?)^    \}\);`)
var beastVariable = regexp.MustCompile(`const releaseName = (` + beastQuotedLiteral + `);`)
var beastObjectAssertion = regexp.MustCompile(`(?s)expect\(parse\((releaseName|` + beastQuotedLiteral + `)\)\)\.to\.deep\.(?:equal|include)\(\{(.*?)\}\);`)
var beastTitleAssertion = regexp.MustCompile(`expect\(parse\((releaseName|` + beastQuotedLiteral + `)\)\.title\)\.to\.equal\((` + beastQuotedLiteral + `)\);`)
var beastObjectKey = regexp.MustCompile(`(^|[{},])([ \t\r\n]*)([A-Za-z][A-Za-z0-9]*):`)
var beastTrailingComma = regexp.MustCompile(`,([ \t\r\n]*[}\]])`)

func decodeBeastLiteralObject(body string) (map[string]json.RawMessage, error) {
	var out strings.Builder
	out.WriteByte('{')
	cursor := 0
	plain := func(s string) {
		out.WriteString(beastTrailingComma.ReplaceAllString(beastObjectKey.ReplaceAllString(s, `${1}${2}"${3}":`), `${1}`))
	}
	for _, span := range virenFullQuoted.FindAllStringIndex(body, -1) {
		plain(body[cursor:span[0]])
		s, err := decodeVirenLiteral(body[span[0]:span[1]])
		if err != nil {
			return nil, err
		}
		b, _ := json.Marshal(s)
		out.Write(b)
		cursor = span[1]
	}
	plain(strings.TrimSuffix(strings.TrimSpace(body[cursor:]), ","))
	out.WriteByte('}')
	d := json.NewDecoder(strings.NewReader(out.String()))
	if _, err := d.Token(); err != nil {
		return nil, err
	}
	labels := map[string]json.RawMessage{}
	for d.More() {
		key, err := d.Token()
		if err != nil {
			return nil, err
		}
		s, ok := key.(string)
		if !ok {
			return nil, fmt.Errorf("nonliteral key")
		}
		if _, exists := labels[s]; exists {
			return nil, fmt.Errorf("duplicate original key")
		}
		var value json.RawMessage
		if err := d.Decode(&value); err != nil {
			return nil, err
		}
		labels[s] = value
	}
	if _, err := d.Token(); err != nil {
		return nil, err
	}
	var title string
	if err := json.Unmarshal(labels["title"], &title); err != nil || title == "" {
		return nil, fmt.Errorf("independent nonempty original title required")
	}
	return labels, nil
}

func beastOriginalAssertionIndex(manifest []byte) (map[string]map[string]json.RawMessage, error) {
	var m struct {
		Source    string `json:"source_id"`
		License   string `json:"license"`
		Status    string `json:"review_status"`
		Reviewed  int    `json:"reviewed_entries"`
		Count     int    `json:"candidate_count"`
		Accepted  *bool  `json:"accepted"`
		Gold      *bool  `json:"formal_golden_acceptance"`
		Authority *bool  `json:"canonical_unit_authorization"`
		Coverage  *bool  `json:"coverage_authority"`
	}
	if err := json.Unmarshal(manifest, &m); err != nil {
		return nil, err
	}
	if m.Source != "beast-full-original" || m.License != "MIT" || m.Status != "partially_reviewed_literal_source" || m.Reviewed != 27 || m.Count != 119 || m.Accepted == nil || *m.Accepted || m.Gold == nil || *m.Gold || m.Authority == nil || *m.Authority || m.Coverage == nil || *m.Coverage {
		return nil, fmt.Errorf("pending source cannot gain authority")
	}
	index := map[string]map[string]json.RawMessage{}
	for _, file := range []struct{ path, hash string }{
		{"../../../testdata/releases/review-inputs/beast-full-original.LICENSE", "4b204d94be3a7487c902684b4ef5d240d169d367b17de72d54360f8351fa27ac"},
		{"../../../testdata/releases/upstream-mit/beast-full-main-original.test.js", "2ec8d9bdd8efbb2f6fd872a1cc5d0b095aafddc52cc92dbfbe7d9ff5389e1116"},
		{"../../../testdata/releases/upstream-mit/beast-full-title-original.test.js", "8eb2936eba975f4fd8c003bbcc89c067b1682a69e7f84cac36bdc86bbb2da3c4"},
	} {
		raw, err := os.ReadFile(file.path)
		if err != nil {
			return nil, err
		}
		h := sha256.Sum256(raw)
		if hex.EncodeToString(h[:]) != file.hash {
			return nil, fmt.Errorf("fixed source/license hash changed: %s", file.path)
		}
		if strings.HasSuffix(file.path, ".LICENSE") {
			continue
		}
		for _, block := range beastBlock.FindAllSubmatch(raw, -1) {
			body := string(block[1])
			variable := beastVariable.FindStringSubmatch(body)
			resolve := func(arg string) (string, error) {
				if arg == "releaseName" {
					if len(variable) != 2 {
						return "", fmt.Errorf("missing original literal input")
					}
					arg = variable[1]
				}
				return decodeVirenLiteral(arg)
			}
			put := func(arg string, labels map[string]json.RawMessage) error {
				input, err := resolve(arg)
				if err != nil {
					return err
				}
				if _, exists := index[input]; exists {
					return fmt.Errorf("duplicate positive assertion input")
				}
				index[input] = labels
				return nil
			}
			for _, assertion := range beastObjectAssertion.FindAllStringSubmatch(body, -1) {
				labels, err := decodeBeastLiteralObject(assertion[2])
				if err != nil {
					return nil, err
				}
				if err := put(assertion[1], labels); err != nil {
					return nil, err
				}
			}
			for _, assertion := range beastTitleAssertion.FindAllStringSubmatch(body, -1) {
				title, err := decodeVirenLiteral(assertion[2])
				if err != nil || title == "" {
					return nil, fmt.Errorf("missing positive title")
				}
				value, _ := json.Marshal(title)
				if err := put(assertion[1], map[string]json.RawMessage{"title": value}); err != nil {
					return nil, err
				}
			}
		}
	}
	if len(index) != 119 {
		return nil, fmt.Errorf("original positive title capacity differs: %d", len(index))
	}
	return index, nil
}

func originalBeastReviewLabels(manifest []byte, input string) (*corpusEntry, error) {
	index, err := beastOriginalAssertionIndex(manifest)
	if err != nil {
		return nil, err
	}
	labels, exists := index[input]
	if !exists {
		return nil, fmt.Errorf("not an independently asserted literal input")
	}
	// Preserve original full fields in fixed files. The established adapter uses
	// quality as its source-fact key; only that key name changes in this view.
	view := make(map[string]json.RawMessage, len(labels))
	for k, v := range labels {
		view[k] = v
	}
	if source, exists := labels["source"]; exists {
		view["quality"] = source
	}
	for singular, plural := range map[string]string{"season": "seasons", "episode": "episodes"} {
		if raw, exists := labels[singular]; exists {
			var n int
			if json.Unmarshal(raw, &n) != nil || n < 0 || (singular == "episode" && n == 0) {
				return nil, fmt.Errorf("invalid original scalar numbering")
			}
			if _, arrayExists := labels[plural]; !arrayExists {
				view[plural], _ = json.Marshal([]int{n})
			}
		}
	}
	if !beastStandardNumbering.MatchString(input) {
		delete(view, "episodes")
	}
	out, err := mappedPTTMainReviewLabels(view, input)
	if err != nil {
		return nil, err
	}
	for key, value := range labels {
		out.Expected["beast_original_"+key] = value
	}
	return out, nil
}

func TestBeastOriginalSourceIntegrityAndLiteralAdmission(t *testing.T) {
	manifest, err := os.ReadFile("../../../testdata/releases/review-inputs/beast-full-original.manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	index, err := beastOriginalAssertionIndex(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if len(index) != 119 {
		t.Fatal("raw capacity is not gold")
	}
	for _, body := range []string{`title: "Show", episodes: intRange(1,100)`, `title: "Show", title: "Other"`, `title: ""`, `title: "Show", year: undefined`} {
		if _, err := decodeBeastLiteralObject(body); err == nil {
			t.Fatalf("nonliteral or incomplete assertion accepted: %s", body)
		}
	}
	labels, err := decodeBeastLiteralObject(`title: "A, title: Original", year: 2020, episodes: [1, 3]`)
	if err != nil || !bytes.Equal(labels["title"], []byte(`"A, title: Original"`)) || !bytes.Equal(labels["episodes"], []byte(`[1, 3]`)) {
		t.Fatal("original title or full coverage changed", labels, err)
	}
}
