package media_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"strconv"
	"testing"
)

// Admission is test-only and individual: an immutable title-table assertion is
// required. Literal group-table assertions are negative-only review rebuttals.
// Enum references and audio snippets never confer measured quality or authority.
// Literal filename scope is retained only as negative coverage rebuttal evidence.
func originalVideoTitleReviewLabels(pool videoLiteralPool, input string) (*corpusEntry, error) {
	var found *corpusEntry
	for _, row := range pool.Entries {
		if row.Name != input {
			continue
		}
		if found != nil {
			return nil, fmt.Errorf("duplicate original input")
		}
		labels := map[string]json.RawMessage{}
		hasPinnedTitle := false
		for _, o := range row.Observations {
			titleCase := o.Path == "test/title.spec.ts" && o.Table == "cases"
			filenameCase := o.Path == "test/filenameParse.spec.ts" && (o.Table == "movieCases" || o.Table == "tvCases")
			groupCase := o.Path == "test/group.spec.ts" && o.Table == "cases"
			if !titleCase && !filenameCase && !groupCase {
				continue
			}
			if o.Line <= 0 {
				return nil, fmt.Errorf("original source location missing")
			}
			for _, key := range []string{"title", "year", "group"} {
				value, ok := o.Fields[key]
				if groupCase {
					if key != "group" {
						continue
					}
					value, ok = o.Fields["expected"]
					// An explicit null refutes a nonempty reviewed group; it is not
					// a generated group or permission to acquire.
					if bytes.Equal(value, []byte("null")) {
						value = []byte(`""`)
					}
				}
				if !ok || (key == "group" && !filenameCase && !groupCase) {
					continue
				}
				if key == "year" {
					if !bytes.Equal(value, []byte("null")) {
						var text string
						if e := json.Unmarshal(value, &text); e != nil {
							return nil, fmt.Errorf("year must remain a literal string or null")
						}
						year, e := strconv.Atoi(text)
						if e != nil || len(text) != 4 || year < 1900 || year > 2099 {
							return nil, fmt.Errorf("unsupported original year %q", text)
						}
						value, _ = json.Marshal(year)
					}
				} else {
					var text string
					if e := json.Unmarshal(value, &text); e != nil || (text == "" && (!groupCase || key != "group")) {
						return nil, fmt.Errorf("invalid original %s literal", key)
					}
				}
				if prior, ok := labels[key]; ok && !bytes.Equal(prior, value) {
					return nil, fmt.Errorf("conflicting original %s assertions", key)
				}
				labels[key] = append(json.RawMessage(nil), value...)
				if (titleCase || filenameCase) && key == "title" {
					hasPinnedTitle = true
				}
			}
			if filenameCase {
				if err := retainVideoLiteralCoverage(labels, o.Fields); err != nil {
					return nil, err
				}
			}
		}
		if !hasPinnedTitle || len(labels["title"]) == 0 {
			return nil, fmt.Errorf("explicit pinned title literal required")
		}
		found = &corpusEntry{Name: input, Expected: labels}
	}
	if found == nil {
		return nil, fmt.Errorf("not an original title-table input")
	}
	return found, nil
}

func TestVideoTitleAdmissionDoesNotInferPartialFacts(t *testing.T) {
	makePool := func(observations string) videoLiteralPool {
		t.Helper()
		var p videoLiteralPool
		if e := json.Unmarshal([]byte(`{"entries":[{"release_name":"Original","observations":`+observations+`}]}`), &p); e != nil {
			t.Fatal(e)
		}
		return p
	}
	valid := `[{"source_path":"test/title.spec.ts","source_line":1,"test_table":"cases","original_fields":{"title":"Original","year":"2008"}},{"source_path":"test/audioCodec.spec.ts","source_line":2,"test_table":"audioCodecCases","original_fields":{"source":"DTS-X","codec":{"symbol_ref":"AudioCodec.DTSHD"}}},{"source_path":"test/filenameParse.spec.ts","source_line":3,"test_table":"movieCases","original_fields":{"title":"Original","year":"2008","group":"CHD","resolution":{"symbol_ref":"Resolution.R2160P"},"seasons":[2],"episodeNumbers":[3,5],"isTv":false}}]`
	p := makePool(valid)
	row, e := originalVideoTitleReviewLabels(p, "Original")
	if e != nil || len(row.Expected) != 6 || string(row.Expected["year"]) != "2008" || string(row.Expected["group"]) != `"CHD"` {
		t.Fatal("literal identity labels changed", row, e)
	}
	for _, key := range []string{"source", "video_codec", "screen_size", "type", "season", "episode", "unit_keys", "authority"} {
		if _, ok := row.Expected[key]; ok {
			t.Fatalf("partial %s acquired fact authority", key)
		}
	}
	if _, e := originalVideoTitleReviewLabels(p, "synthetic variant"); e == nil {
		t.Fatal("invented input accepted")
	}
	p.Entries = append(p.Entries, p.Entries[0])
	if _, e := originalVideoTitleReviewLabels(p, "Original"); e == nil {
		t.Fatal("duplicate original accepted")
	}
	for _, tc := range []struct{ name, observations string }{
		{"unapproved-file", `[{"source_path":"test/unapproved.spec.ts","source_line":1,"test_table":"movieCases","original_fields":{"title":"Original","year":"2008"}}]`},
		{"getter-only", `[{"source_path":"test/title.spec.ts","source_line":1,"test_table":"cases","original_fields":{"expected":"Original"}}]`},
		{"enum-title", `[{"source_path":"test/title.spec.ts","source_line":1,"test_table":"cases","original_fields":{"title":{"symbol_ref":"Title.Original"}}}]`},
		{"empty-title", `[{"source_path":"test/title.spec.ts","source_line":1,"test_table":"cases","original_fields":{"title":""}}]`},
		{"missing-line", `[{"source_path":"test/title.spec.ts","test_table":"cases","original_fields":{"title":"Original"}}]`},
		{"numeric-year", `[{"source_path":"test/title.spec.ts","source_line":1,"test_table":"cases","original_fields":{"title":"Original","year":2008}}]`},
		{"unreviewed-year", `[{"source_path":"test/title.spec.ts","source_line":1,"test_table":"cases","original_fields":{"title":"Original","year":"08"}}]`},
		{"conflicting-title", `[{"source_path":"test/title.spec.ts","source_line":1,"test_table":"cases","original_fields":{"title":"Original"}},{"source_path":"test/filenameParse.spec.ts","source_line":2,"test_table":"movieCases","original_fields":{"title":"Other"}}]`},
		{"conflicting-year", `[{"source_path":"test/title.spec.ts","source_line":1,"test_table":"cases","original_fields":{"title":"Original","year":"2008"}},{"source_path":"test/filenameParse.spec.ts","source_line":2,"test_table":"movieCases","original_fields":{"year":"2009"}}]`},
		{"null-year-conflict", `[{"source_path":"test/title.spec.ts","source_line":1,"test_table":"cases","original_fields":{"title":"Original","year":null}},{"source_path":"test/filenameParse.spec.ts","source_line":2,"test_table":"movieCases","original_fields":{"year":"2009"}}]`},
		{"malformed-group", `[{"source_path":"test/title.spec.ts","source_line":1,"test_table":"cases","original_fields":{"title":"Original"}},{"source_path":"test/filenameParse.spec.ts","source_line":2,"test_table":"movieCases","original_fields":{"group":{"symbol_ref":"Group.Original"}}}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, e := originalVideoTitleReviewLabels(makePool(tc.observations), "Original"); e == nil {
				t.Fatal("partial original admission widened")
			}
		})
	}
	row, e = originalVideoTitleReviewLabels(makePool(`[{"source_path":"test/title.spec.ts","source_line":1,"test_table":"cases","original_fields":{"title":"Original","year":null}}]`), "Original")
	if e != nil || string(row.Expected["year"]) != "null" {
		t.Fatal("explicit absent year was invented", row, e)
	}
	row, e = originalVideoTitleReviewLabels(makePool(`[{"source_path":"test/title.spec.ts","source_line":1,"test_table":"cases","original_fields":{"title":"Original"}}]`), "Original")
	if e != nil || len(row.Expected) != 1 {
		t.Fatal("missing year invented", row, e)
	}
}

func TestVideoTitleAdmissionUsesIndependentGroupRebuttal(t *testing.T) {
	makePool := func(observations string) videoLiteralPool {
		t.Helper()
		var p videoLiteralPool
		if err := json.Unmarshal([]byte(`{"entries":[{"release_name":"Original","observations":`+observations+`}]}`), &p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	for _, tc := range []struct{ name, observations string }{
		{"explicit group", `[{"source_path":"test/title.spec.ts","source_line":1,"test_table":"cases","original_fields":{"title":"Original"}},{"source_path":"test/group.spec.ts","source_line":2,"test_table":"cases","original_fields":{"expected":"CHD"}}]`},
		{"explicit absent group", `[{"source_path":"test/title.spec.ts","source_line":1,"test_table":"cases","original_fields":{"title":"Original"}},{"source_path":"test/group.spec.ts","source_line":2,"test_table":"cases","original_fields":{"expected":null}}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row, err := originalVideoTitleReviewLabels(makePool(tc.observations), "Original")
			if err != nil {
				t.Fatal(err)
			}
			if len(row.Expected["group"]) == 0 {
				t.Fatal("independent group rebuttal discarded")
			}
			if validateReviewedQualityLabels(row.Expected, quality.Profile{Group: "OTHER"}) == nil {
				t.Fatal("reviewed group contradicted independent literal")
			}
			if validateReviewedQualityLabels(row.Expected, quality.Profile{}) != nil {
				t.Fatal("unknown group acquired evidence")
			}
		})
	}
	for _, observations := range []string{
		`[{"source_path":"test/group.spec.ts","source_line":1,"test_table":"cases","original_fields":{"expected":"CHD"}}]`,
		`[{"source_path":"test/title.spec.ts","source_line":1,"test_table":"cases","original_fields":{"title":"Original"}},{"source_path":"test/group.spec.ts","source_line":2,"test_table":"cases","original_fields":{"expected":{"symbol_ref":"Group.CHD"}}}]`,
		`[{"source_path":"test/title.spec.ts","source_line":1,"test_table":"cases","original_fields":{"title":"Original"}},{"source_path":"test/group.spec.ts","test_table":"cases","original_fields":{"expected":"CHD"}}]`,
		`[{"source_path":"test/title.spec.ts","source_line":1,"test_table":"cases","original_fields":{"title":"Original"}},{"source_path":"test/group.spec.ts","source_line":2,"test_table":"cases","original_fields":{"expected":"CHD"}},{"source_path":"test/filenameParse.spec.ts","source_line":3,"test_table":"movieCases","original_fields":{"group":"OTHER"}}]`,
	} {
		if _, err := originalVideoTitleReviewLabels(makePool(observations), "Original"); err == nil {
			t.Fatal("partial/conflicting group source widened admission")
		}
	}
}

func TestPinnedFilenameTitlesDoNotPromoteSymbolicFacts(t *testing.T) {
	for _, table := range []string{"movieCases", "tvCases"} {
		t.Run(table, func(t *testing.T) {
			text := `{"entries":[{"release_name":"Original","observations":[{"source_path":"test/filenameParse.spec.ts","source_line":15,"test_table":"` + table + `","original_fields":{"title":"Original","year":"2008","group":"CHD","resolution":{"symbol_ref":"Resolution.R2160P"},"sources":[{"symbol_ref":"Source.WEBDL"}],"videoCodec":{"symbol_ref":"VideoCodec.H265"},"audioChannels":{"symbol_ref":"Channels.SIX"},"seasons":[1],"episodeNumbers":[1,3],"edition":{"hdr":true}}}]}]}`
			var pool videoLiteralPool
			if err := json.Unmarshal([]byte(text), &pool); err != nil {
				t.Fatal(err)
			}
			before, err := json.Marshal(pool)
			if err != nil {
				t.Fatal(err)
			}
			row, err := originalVideoTitleReviewLabels(pool, "Original")
			if err != nil || len(row.Expected) != 5 || string(row.Expected["title"]) != `"Original"` || string(row.Expected["year"]) != "2008" || string(row.Expected["group"]) != `"CHD"` {
				t.Fatal("literal proof lost", row, err)
			}
			after, err := json.Marshal(pool)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("immutable original labels changed")
			}
			for _, key := range []string{"source", "screen_size", "codec", "video_codec", "audio", "type", "season", "episode", "unit_keys", "authority", "edition"} {
				if _, ok := row.Expected[key]; ok {
					t.Fatalf("symbolic or partial field promoted: %s", key)
				}
			}
		})
	}
}
