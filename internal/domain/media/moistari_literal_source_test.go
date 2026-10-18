package media_test

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
)

// Private source reader only. No shared source registration or parser execution.
func moistariOriginalIndex() (map[string]*corpusEntry, error) {
	const base = "../../../testdata/releases/review-inputs/moistari-rls-original"
	raw, err := os.ReadFile(base + ".manifest.json")
	if err != nil {
		return nil, err
	}
	var m struct {
		ID        string `json:"source_id"`
		Commit    string `json:"commit"`
		Reviewed  int    `json:"reviewed_entries"`
		Accepted  *bool  `json:"accepted"`
		Authority *bool  `json:"authority"`
		Gold      *bool  `json:"formal_golden_acceptance"`
		Coverage  *bool  `json:"coverage_authority"`
		Units     *bool  `json:"canonical_unit_authorization"`
	}
	if err = json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	if m.ID != "moistari-rls-original" || m.Commit != "f84797726c6af8d64a228d9349832002c8f33888" || m.Reviewed != 104 {
		return nil, fmt.Errorf("source pin or individual review count changed")
	}
	for _, flag := range []*bool{m.Accepted, m.Authority, m.Gold, m.Coverage, m.Units} {
		if flag == nil || *flag {
			return nil, fmt.Errorf("raw source must have explicit false authority flags")
		}
	}
	var original []byte
	for _, f := range []struct{ suffix, hash string }{
		{".sources/LICENSE", "4fd0093da480945ec7ead12424001aab0e3678f5dddbc2af1a580a2bfe4668cf"},
		{".sources/tests.yaml", "59a168ae628271f51f361ed1bbe6e91ba97ec03befdb7b400079861337f87620"},
		{".sources/rls_test.go", "924c0367698b5a37a05401cd9d6944969fd2f568b3768d580d9cdb71a852b976"},
		{".sources/scan_test.go", "09a6b2be3009521495dac0d65ff6e680fce5a34e7ded359c476fed5a91570a3b"},
		{".sources/test.sh", "7b625ec52cda776aeadee5e4be99e3d4b9f2b7735de13981a653869f238800f1"},
		{".audit.json", "a28fe992f5aabab8b604b2e5949ebeed7b35d288a55a200cf0817ea0bb8e64ac"},
		{".literals.jsonl", "a881492e1ce8ce4a3027166eeeead36113aeb2b087624153684550592028fbc7"},
	} {
		b, e := os.ReadFile(base + f.suffix)
		if e != nil {
			return nil, e
		}
		sum := sha256.Sum256(b)
		if hex.EncodeToString(sum[:]) != f.hash {
			return nil, fmt.Errorf("immutable source or preprobe audit changed: %s", f.suffix)
		}
		if f.suffix == ".sources/tests.yaml" {
			original = b
		}
	}
	out := map[string]*corpusEntry{}
	seen := map[string]bool{}
	var name string
	var labels map[string]json.RawMessage
	count := 0
	flush := func() error {
		if labels == nil {
			return nil
		}
		if seen[name] {
			return fmt.Errorf("duplicate source input")
		}
		seen[name] = true
		count++
		if title := labels["title"]; len(title) > 0 && !bytes.Equal(title, []byte(`""`)) {
			// Keep every raw field under a source namespace. Only independently
			// understood equivalents feed the existing shared contradiction checks.
			expected := moistariExpectedLabels(labels)
			out[name] = &corpusEntry{Name: name, Expected: expected}
		}
		return nil
	}
	s := bufio.NewScanner(bytes.NewReader(original))
	s.Buffer(make([]byte, 4096), 128*1024)
	for s.Scan() {
		line := s.Text()
		switch {
		case strings.HasPrefix(line, `"`):
			if err = flush(); err != nil {
				return nil, err
			}
			if !strings.HasSuffix(line, ":") {
				return nil, fmt.Errorf("invalid raw input")
			}
			name, err = strconv.Unquote(strings.TrimSuffix(line, ":"))
			if err != nil {
				return nil, err
			}
			labels = map[string]json.RawMessage{}
		case strings.HasPrefix(line, "  "):
			k, v, ok := strings.Cut(line[2:], ": ")
			if !ok || labels == nil || k == "" {
				return nil, fmt.Errorf("invalid raw assertion")
			}
			if _, ok := labels[k]; ok {
				return nil, fmt.Errorf("duplicate raw assertion")
			}
			if strings.HasPrefix(v, `"`) {
				text, e := strconv.Unquote(v)
				if e != nil {
					return nil, e
				}
				labels[k], err = json.Marshal(text)
			} else {
				n, e := strconv.Atoi(v)
				if e != nil {
					return nil, e
				}
				labels[k], err = json.Marshal(n)
			}
			if err != nil {
				return nil, err
			}
		case line == "" || strings.HasPrefix(line, "#"):
		default:
			return nil, fmt.Errorf("unexpected source syntax")
		}
	}
	if err = s.Err(); err != nil {
		return nil, err
	}
	if err = flush(); err != nil {
		return nil, err
	}
	if count != 407 || len(out) != 404 {
		return nil, fmt.Errorf("literal capacity changed: records=%d titles=%d", count, len(out))
	}
	return out, nil
}

func originalMoistariReviewLabels(input string) (*corpusEntry, error) {
	index, err := moistariOriginalIndex()
	if err != nil {
		return nil, err
	}
	row := index[input]
	if row == nil {
		return nil, fmt.Errorf("not an original positive moistari title assertion")
	}
	return row, nil
}

func TestMoistariOriginalSourceRemainsUnreviewed(t *testing.T) {
	if _, err := moistariOriginalIndex(); err != nil {
		t.Fatal(err)
	}
}
