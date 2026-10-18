package media_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// These are scalar assertions in inert, hash-pinned Rust source, not executed
// getters or complete coverage. In particular episode() may return only the first
// member of a named range. No constructor/API can confer IO authority.
var rustConstructors = regexp.MustCompile(`Metadata::from\s*\(\s*("(?:[^"\\]|\\[\s\S])*")\s*,?\s*\)`)
var rustScalarAssertions = regexp.MustCompile(`assert_eq!\(\s*m\.([a-z_]+)\(\),\s*(None|Some\([^)]*\)|"(?:[^"\\]|\\[\s\S])*")\s*\);`)

// Rust permits escaped apostrophes in normal strings; JSON does not. Decode
// only that additional literal escape, preserving escaped backslashes. Unknown
// escapes/continuations remain errors, never expressions to evaluate.
func decodeRustScalar(raw []byte) (any, error) {
	normalized := make([]byte, 0, len(raw))
	for i := 0; i < len(raw); i++ {
		if raw[i] == '\\' && i+1 < len(raw) {
			if raw[i+1] == '\'' {
				normalized = append(normalized, '\'')
				i++
				continue
			}
			normalized = append(normalized, raw[i], raw[i+1])
			i++
			continue
		}
		normalized = append(normalized, raw[i])
	}
	var scalar any
	err := json.Unmarshal(normalized, &scalar)
	return scalar, err
}

func TestPinnedRustScalarLabelsRemainOriginalAndPartial(t *testing.T) {
	root := "../../../testdata/releases/review-inputs/rust-ptn"
	raw, err := os.ReadFile(root + ".json")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != "54d2bf95a42c771d4aa2445df2cc5bacfce04490ad1b3e3f510c0220dcc1ff4a" {
		t.Fatal("original Rust labels changed")
	}
	pool, err := decodeUnapprovedPool(raw)
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile(root + ".source")
	if err != nil {
		t.Fatal(err)
	}
	sum = sha256.Sum256(source)
	if hex.EncodeToString(sum[:]) != "2b600d6f0a051cd651b9ff74a3b9d2e528b62ff755b59e0f53c06294eab47d1d" {
		t.Fatal("original source changed")
	}
	license, err := os.ReadFile(root + ".LICENSE")
	if err != nil {
		t.Fatal(err)
	}
	sum = sha256.Sum256(license)
	if hex.EncodeToString(sum[:]) != "555de1884458c8c87eb69a5be47724d7b8ea50a5a2ef73864c8bcfe1e40eb36c" || !bytes.Contains(license, []byte("Permission is hereby granted")) {
		t.Fatal("original MIT license changed")
	}
	if pool.SourceID != "rust-ptn" || pool.Provenance.Commit != "6f4f42c2c711c53ed787f535d5b8216e6f47c9c5" || pool.Provenance.Path != "src/test.rs" || pool.Provenance.Count != 92 || len(pool.Entries) != 92 {
		t.Fatal("original provenance changed")
	}
	constructors := rustConstructors.FindAllSubmatchIndex(source, -1)
	byLine := map[int]struct {
		name   string
		fields map[string]json.RawMessage
	}{}
	allowed := map[string]bool{}
	for _, k := range []string{"title", "year", "resolution", "season", "episode", "quality", "codec", "group", "extension", "country", "audio", "imdb_tag", "language"} {
		allowed[k] = true
	}
	for i, m := range constructors {
		decodedName, e := decodeRustScalar(source[m[2]:m[3]])
		name, literal := decodedName.(string)
		if e != nil || !literal {
			continue
		} // Unsupported Rust continuation is not repaired.
		end := len(source)
		if i+1 < len(constructors) {
			end = constructors[i+1][0]
		}
		fields := map[string]json.RawMessage{}
		for _, a := range rustScalarAssertions.FindAllSubmatch(source[m[1]:end], -1) {
			key := string(a[1])
			if !allowed[key] {
				continue
			}
			value := string(a[2])
			if value == "None" {
				value = "null"
			} else if strings.HasPrefix(value, "Some(") {
				value = strings.TrimSuffix(strings.TrimPrefix(value, "Some("), ")")
			}
			scalar, e := decodeRustScalar([]byte(value))
			if e != nil {
				t.Fatalf("unsupported original scalar %s: %s", key, value)
			}
			switch scalar.(type) {
			case nil, string, float64:
			default:
				t.Fatal("non-scalar getter cannot become a fact", key)
			}
			encoded, _ := json.Marshal(scalar)
			if old, ok := fields[key]; ok && !bytes.Equal(old, encoded) {
				t.Fatal("conflicting assertions in a constructor", name, key)
			}
			fields[key] = json.RawMessage(encoded)
		}
		line := bytes.Count(source[:m[0]], []byte("\n")) + 1
		byLine[line] = struct {
			name   string
			fields map[string]json.RawMessage
		}{name, fields}
	}
	for _, row := range pool.Entries {
		for _, o := range row.Observations {
			original, ok := byLine[o.Line]
			if !ok || original.name != row.Name || !reflect.DeepEqual(original.fields, o.Fields) {
				t.Fatalf("original scalar/position mismatch %s at %d: source=%v pool=%v", row.Name, o.Line, original.fields, o.Fields)
			}
		}
	}
}

func originalRustReviewLabels(pool reviewInputPool, input string) (*corpusEntry, error) {
	var found *corpusEntry
	for _, row := range pool.Entries {
		if row.Name != input {
			continue
		}
		if found != nil {
			return nil, fmt.Errorf("duplicate original input")
		}
		labels := map[string]json.RawMessage{}
		for _, o := range row.Observations {
			if o.Line <= 0 {
				return nil, fmt.Errorf("missing original source line")
			}
			for _, key := range []string{"title", "year", "season", "episode", "resolution", "quality", "codec", "group"} {
				value, ok := o.Fields[key]
				if !ok {
					continue
				}
				if key == "year" || key == "season" || key == "episode" {
					if !bytes.Equal(value, []byte("null")) {
						var n int
						if e := json.Unmarshal(value, &n); e != nil {
							return nil, fmt.Errorf("invalid literal %s", key)
						}
						if (key == "year" && (n < 1900 || n > 2099)) || (key == "season" && (n < 0 || n > 999)) || (key == "episode" && (n < 1 || n > 9999)) {
							return nil, fmt.Errorf("invalid literal %s", key)
						}
					}
				} else {
					var text string
					if e := json.Unmarshal(value, &text); e != nil || text == "" {
						return nil, fmt.Errorf("invalid literal %s", key)
					}
				}
				if old, ok := labels[key]; ok && !bytes.Equal(old, value) {
					return nil, fmt.Errorf("conflicting original %s", key)
				}
				labels[key] = append(json.RawMessage(nil), value...)
			}
		}
		if len(labels["title"]) == 0 {
			return nil, fmt.Errorf("independent title assertion required")
		}
		expected := map[string]json.RawMessage{}
		for _, key := range []string{"title", "year"} {
			if v, ok := labels[key]; ok {
				expected[key] = v
			}
		}
		if v, ok := labels["season"]; ok {
			expected["rust_season"] = v
		}
		if v, ok := labels["episode"]; ok {
			expected["rust_first_episode"] = v
		}
		// Preserve these as separate rebuttals, not normalized source observations.
		for _, key := range []string{"resolution", "quality", "codec", "group"} {
			if v, ok := labels[key]; ok {
				expected["rust_"+key] = v
			}
		}
		found = &corpusEntry{Name: input, Expected: expected}
	}
	if found == nil {
		return nil, fmt.Errorf("not an original labelled input")
	}
	return found, nil
}
