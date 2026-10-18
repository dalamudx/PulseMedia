package media_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

// Original literals are source evidence, never complete recognition expectations.
func TestVirenTitleOriginalEvidence(t *testing.T) {
	root := "../../../testdata/releases/review-inputs/viren-title-original-assertions"
	source, err := os.ReadFile(root + ".title.test.ts.source")
	if err != nil {
		t.Fatal(err)
	}
	license, err := os.ReadFile(root + ".LICENSE")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		data []byte
		hash string
	}{
		{source, "c21deed849fd334ed72bd4244aca3e0637a019b74e7237b8902708e035ea70ec"},
		{license, "5c44be7aac21777dcc84933675ee7f0b08c736aeaed6c78aedd0e3d7a6856d3a"},
	} {
		hash := sha256.Sum256(check.data)
		if hex.EncodeToString(hash[:]) != check.hash {
			t.Fatal("fixed upstream artifact changed")
		}
	}
	if !bytes.Contains(license, []byte("MIT License")) || !bytes.Contains(license, []byte("Copyright (c) 2025 Viren070")) {
		t.Fatal("missing attribution")
	}
	var pool struct {
		Source     string `json:"source_id"`
		License    string `json:"license"`
		Status     string `json:"review_status"`
		Accepted   *bool  `json:"formal_golden_acceptance"`
		Authority  *bool  `json:"coverage_authority"`
		Provenance struct {
			Repository  string `json:"repository"`
			Commit      string `json:"commit"`
			Path        string `json:"path"`
			SourceHash  string `json:"source_sha256"`
			LicenseHash string `json:"license_sha256"`
			Count       int    `json:"unique_entries"`
		} `json:"provenance"`
		Entries []struct {
			Input        string            `json:"release_name"`
			Fields       map[string]string `json:"original_fields"`
			Path         string            `json:"source_path"`
			Line         int               `json:"source_line"`
			InputLiteral string            `json:"input_literal"`
			TitleLiteral string            `json:"title_literal"`
		} `json:"entries"`
	}
	b, err := os.ReadFile(root + ".json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(b, &pool); err != nil {
		t.Fatal(err)
	}
	if pool.Source != "viren-title-original-assertions" || pool.License != "MIT" || pool.Status != "unreviewed" || pool.Accepted == nil || *pool.Accepted || pool.Authority == nil || *pool.Authority {
		t.Fatal("pending title pool grants authority")
	}
	if pool.Provenance.Repository != "Viren070/parse-torrent-title" || pool.Provenance.Commit != "46bc6fef150649410d8d307a08e97ba3a3559eb6" || pool.Provenance.Path != "src/__tests__/title.test.ts" || pool.Provenance.SourceHash != "c21deed849fd334ed72bd4244aca3e0637a019b74e7237b8902708e035ea70ec" || pool.Provenance.LicenseHash != "5c44be7aac21777dcc84933675ee7f0b08c736aeaed6c78aedd0e3d7a6856d3a" {
		t.Fatal("origin changed")
	}
	rx := regexp.MustCompile(`const result = parseTorrentTitle\(\s*('(?:\\.|[^'\\])*'|"(?:\\.|[^"\\])*")\s*\);\s*expect\(result\.title\)\.toBe\(\s*('(?:\\.|[^'\\])*'|"(?:\\.|[^"\\])*")\s*\);`)
	matches := rx.FindAllSubmatchIndex(source, -1)
	if len(matches) != 66 || len(pool.Entries) != 66 || pool.Provenance.Count != 66 {
		t.Fatal("literal evidence count changed")
	}
	seen := map[string]bool{}
	for i, m := range matches {
		e := pool.Entries[i]
		inputLiteral := string(source[m[2]:m[3]])
		titleLiteral := string(source[m[4]:m[5]])
		if e.InputLiteral != inputLiteral || e.TitleLiteral != titleLiteral || e.Line != bytes.Count(source[:m[0]], []byte("\n"))+1 || e.Path != pool.Provenance.Path {
			t.Fatal("not original literal location", i)
		}
		input, err := decodeVirenLiteral(inputLiteral)
		if err != nil {
			t.Fatal(err)
		}
		title, err := decodeVirenLiteral(titleLiteral)
		if err != nil {
			t.Fatal(err)
		}
		if e.Input != input || e.Fields["expected_title"] != title || len(e.Fields) != 1 || seen[input] || strings.TrimSpace(title) == "" {
			t.Fatal("invented or duplicate original label", i)
		}
		seen[input] = true
	}
}

func decodeVirenLiteral(s string) (string, error) {
	if strings.HasPrefix(s, `"`) {
		var v string
		err := json.Unmarshal([]byte(s), &v)
		return v, err
	}
	var b strings.Builder
	for i := 1; i < len(s)-1; i++ {
		if s[i] != '\\' {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch s[i] {
		case '\\', '\'', '"':
			b.WriteByte(s[i])
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		default:
			return "", &json.SyntaxError{}
		}
	}
	return b.String(), nil
}

// Only independently written title labels are translated; complete expected
// recognition fields are individually reviewed, never produced by a parser.
func originalVirenTitleLabels(b []byte, input string) (*corpusEntry, error) {
	var pool struct {
		Entries []struct {
			Input  string            `json:"release_name"`
			Fields map[string]string `json:"original_fields"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(b, &pool); err != nil {
		return nil, err
	}
	for _, e := range pool.Entries {
		if e.Input == input && len(e.Fields) == 1 && e.Fields["expected_title"] != "" {
			labels, err := json.Marshal(e.Fields["expected_title"])
			if err != nil {
				return nil, err
			}
			return &corpusEntry{Name: input, Expected: map[string]json.RawMessage{"title": labels}}, nil
		}
	}
	return nil, os.ErrNotExist
}
