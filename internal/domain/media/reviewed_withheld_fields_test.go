package media_test

import (
	"encoding/json"
	"fmt"
	"testing"
)

// Source labels are immutable. Explicit zero-authority year/scope refusals may
// report no identity/coverage while retaining the original labels separately.
// Positive cases still require exact original identity and full coverage.
func validatedWithheldSourceFields(declared []string, original map[string]json.RawMessage, raw json.RawMessage) (map[string]bool, error) {
	fields := map[string]bool{}
	if len(declared) == 0 {
		return fields, nil
	}
	var w struct {
		Title      string   `json:"title"`
		Year       int      `json:"year"`
		MediaType  string   `json:"media_type"`
		Season     int      `json:"season"`
		Episodes   []int    `json:"episodes"`
		Keys       []string `json:"unit_keys"`
		Absolute   int      `json:"absolute_episode"`
		Anime      bool     `json:"is_anime"`
		Confidence float64  `json:"confidence"`
		Review     bool     `json:"requires_review"`
		Reason     string   `json:"reason"`
	}
	if e := json.Unmarshal(raw, &w); e != nil {
		return nil, e
	}
	if !w.Review || w.Confidence != 0 || w.Season != 0 || len(w.Episodes) != 0 || len(w.Keys) != 0 || w.Absolute != 0 || w.Anime {
		return nil, fmt.Errorf("withheld labels require zero authority and no reported coverage")
	}
	ambiguousYear := w.Reason == "ambiguous_media_year" && w.MediaType == "movie" && w.Year == 0
	scopeRefusal := ((w.Reason == "episode_coverage_unresolved" || w.Reason == "conflicting_episode_coverage") && w.MediaType == "tv") || (w.Reason == "multi_unit_collection" && w.MediaType == "movie") || (w.Reason == "non_video_file" && (w.MediaType == "tv" || w.MediaType == "movie"))
	if !ambiguousYear && !scopeRefusal {
		return nil, fmt.Errorf("unsupported source-label refusal contract")
	}
	for _, field := range declared {
		label, exists := original[field]
		if !exists || fields[field] {
			return nil, fmt.Errorf("missing or duplicate withheld source field %q", field)
		}
		switch field {
		case "title":
			var title string
			if e := json.Unmarshal(label, &title); e != nil || title == "" || title == w.Title {
				return nil, fmt.Errorf("title withholding must explicitly retain a different original label")
			}
		case "year":
			var year int
			if e := json.Unmarshal(label, &year); e != nil || year <= 0 || w.Year != 0 {
				return nil, fmt.Errorf("year withholding must preserve an explicit positive source year")
			}
		case "season", "episode":
			if !scopeRefusal {
				return nil, fmt.Errorf("coverage withholding requires an explicit scope refusal")
			}
			values := corpusNumbers(label)
			if len(values) == 0 {
				return nil, fmt.Errorf("withheld coverage requires a nonempty original scalar or array")
			}
			seen := map[int]bool{}
			for _, n := range values {
				if n < 0 || (field == "episode" && n == 0) || seen[n] {
					return nil, fmt.Errorf("invalid or duplicate original coverage")
				}
				seen[n] = true
			}
			if field == "season" && len(values) == 1 && values[0] == w.Season {
				return nil, fmt.Errorf("unnecessary original season exception")
			}
		default:
			return nil, fmt.Errorf("unsupported withheld source field %q", field)
		}
		fields[field] = true
	}
	return fields, nil
}

func TestWithheldSourceFieldsCannotRelaxAcceptedGoldenLabels(t *testing.T) {
	original := map[string]json.RawMessage{"title": json.RawMessage(`"Calendar Title 1984"`), "year": json.RawMessage(`2020`), "episode": json.RawMessage(`1`)}
	base := map[string]any{"title": "Calendar Title 1984 2020", "year": 0, "media_type": "movie", "season": 0, "episodes": []int{}, "unit_keys": []string{}, "absolute_episode": 0, "is_anime": false, "confidence": 0, "requires_review": true, "reason": "ambiguous_media_year"}
	for _, c := range []struct {
		name     string
		declared []string
		field    string
		value    any
		valid    bool
	}{
		{name: "explicit withheld identity", declared: []string{"title", "year"}, valid: true},
		{name: "unnecessary title exception", declared: []string{"title"}, field: "title", value: "Calendar Title 1984"},
		{name: "accepted result", declared: []string{"year"}, field: "requires_review", value: false},
		{name: "positive confidence", declared: []string{"year"}, field: "confidence", value: 0.9},
		{name: "minted unit", declared: []string{"year"}, field: "unit_keys", value: []string{"movie"}},
		{name: "minted coverage", declared: []string{"year"}, field: "episodes", value: []int{1}},
		{name: "minted season", declared: []string{"year"}, field: "season", value: 1},
		{name: "minted absolute", declared: []string{"year"}, field: "absolute_episode", value: 25},
		{name: "positive year", declared: []string{"year"}, field: "year", value: 2020},
		{name: "other rejection", declared: []string{"year"}, field: "reason", value: "title_only_without_year"},
		{name: "wrong type", declared: []string{"year"}, field: "media_type", value: "tv"},
		{name: "missing original", declared: []string{"absent"}},
		{name: "unsupported field", declared: []string{"episode"}},
		{name: "duplicate", declared: []string{"year", "year"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			want := map[string]any{}
			for k, v := range base {
				want[k] = v
			}
			if c.field != "" {
				want[c.field] = c.value
			}
			raw, e := json.Marshal(want)
			if e != nil {
				t.Fatal(e)
			}
			fields, e := validatedWithheldSourceFields(c.declared, original, raw)
			if (e == nil) != c.valid {
				t.Fatal("unsafe source-label exception", fields, e)
			}
			if c.valid && len(fields) != len(c.declared) {
				t.Fatal("withheld field count mismatch")
			}
		})
	}
}

func TestWithheldSourceCoverageScalarAndArray(t *testing.T) {
	raw := json.RawMessage(`{"title":"","year":0,"media_type":"tv","season":0,"episodes":[],"unit_keys":[],"absolute_episode":0,"is_anime":false,"confidence":0,"requires_review":true,"reason":"episode_coverage_unresolved"}`)
	for _, tc := range []struct {
		field, value string
		valid        bool
	}{
		{"season", "1", true}, {"season", "[1]", true}, {"season", "[0,1]", true},
		{"episode", "1", true}, {"episode", "[1,2]", true},
		{"season", "-1", false}, {"episode", "0", false}, {"season", "[]", false},
		{"season", "null", false}, {"season", "\"1\"", false}, {"season", "1.1", false},
		{"season", "[1,1]", false}, {"episode", "[0,1]", false},
	} {
		original := map[string]json.RawMessage{tc.field: json.RawMessage(tc.value)}
		_, err := validatedWithheldSourceFields([]string{tc.field}, original, raw)
		if (err == nil) != tc.valid {
			t.Fatal("scalar/array source scope format changed acceptance", tc, err)
		}
	}
}
