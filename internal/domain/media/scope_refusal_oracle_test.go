package media_test

import (
	"encoding/json"
	"testing"
)

func TestScopeRefusalCannotCertifyPartialOrAcceptedCoverage(t *testing.T) {
	original := map[string]json.RawMessage{"title": json.RawMessage(`"Independent Show"`), "year": json.RawMessage(`2024`), "season": json.RawMessage(`[1,2,3]`), "episode": json.RawMessage(`[1,2,3,4]`)}
	base := map[string]any{"title": "", "year": 0, "media_type": "tv", "season": 0, "episodes": []int{}, "unit_keys": []string{}, "absolute_episode": 0, "is_anime": false, "confidence": 0, "requires_review": true, "reason": "episode_coverage_unresolved"}
	for _, c := range []struct {
		name        string
		field       string
		value       any
		sourceField string
		sourceValue string
		declared    []string
		valid       bool
	}{
		{name: "all original facts retained separately", declared: []string{"title", "year", "season", "episode"}, valid: true},
		{name: "movie package refusal", field: "reason", value: "multi_unit_collection", valid: true},
		{name: "accepted", field: "requires_review", value: false},
		{name: "positive confidence", field: "confidence", value: 0.01},
		{name: "movie unit", field: "unit_keys", value: []string{"movie"}},
		{name: "first-only unit", field: "unit_keys", value: []string{"S01E01"}},
		{name: "first-only episode", field: "episodes", value: []int{1}},
		{name: "partial season", field: "season", value: 1},
		{name: "absolute number", field: "absolute_episode", value: 1},
		{name: "anime mapping", field: "is_anime", value: true},
		{name: "other hold reason", field: "reason", value: "absolute_mapping_required"},
		{name: "positive match reason", field: "reason", value: "standard_season_episode"},
		{name: "wrong type", field: "media_type", value: "movie"},
		// Scalar season is an original-source format, not a replacement for
		// authenticated array labels (checked by source hashes and the loader).
		{name: "scalar source season retains zero authority", sourceField: "season", sourceValue: `1`, valid: true},
		{name: "fractional scalar season", sourceField: "season", sourceValue: `1.5`},
		{name: "empty array", sourceField: "season", sourceValue: `[]`},
		{name: "duplicate source seasons", sourceField: "season", sourceValue: `[1,1]`},
		{name: "negative source season", sourceField: "season", sourceValue: `[-1]`},
		{name: "duplicate source episodes", sourceField: "episode", sourceValue: `[1,1]`},
		{name: "zero source episode", sourceField: "episode", sourceValue: `[0]`},
		{name: "source enum expression", sourceField: "episode", sourceValue: `{"symbol_ref":"Episode.One"}`},
		{name: "noninteger array", sourceField: "episode", sourceValue: `[1,1.5]`},
		{name: "unchanged original season", sourceField: "season", sourceValue: `[0]`},
		{name: "year is not withheld", field: "year", value: 2024},
		{name: "missing source", declared: []string{"missing"}},
		{name: "duplicate declaration", declared: []string{"season", "season"}},
		{name: "quality cannot be withheld", declared: []string{"quality"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := map[string]any{}
			for k, v := range base {
				w[k] = v
			}
			labels := map[string]json.RawMessage{}
			for k, v := range original {
				labels[k] = v
			}
			if c.field != "" {
				w[c.field] = c.value
			}
			if c.name == "movie package refusal" {
				w["media_type"] = "movie"
			}
			if c.sourceField != "" {
				labels[c.sourceField] = json.RawMessage(c.sourceValue)
			}
			declared := c.declared
			if declared == nil {
				declared = []string{"title", "year", "season", "episode"}
			}
			raw, err := json.Marshal(w)
			if err != nil {
				t.Fatal(err)
			}
			fields, err := validatedWithheldSourceFields(declared, labels, raw)
			if (err == nil) != c.valid {
				t.Fatalf("unsafe scope exception: fields=%v err=%v", fields, err)
			}
			if c.valid && len(fields) != len(declared) {
				t.Fatal("source facts lost")
			}
		})
	}
}

func TestConflictingSourceScopeCannotCertifyPartialCoverage(t *testing.T) {
	original := map[string]json.RawMessage{"title": json.RawMessage(`"Soul Land"`), "episode": json.RawMessage(`[121,122,123,124,125]`)}
	for _, tc := range []struct {
		field string
		value any
		valid bool
	}{
		{"", nil, true}, {"requires_review", false, false}, {"episodes", []int{121}, false},
		{"unit_keys", []string{"S01E121"}, false}, {"absolute_episode", 125, false},
		{"confidence", 0.01, false}, {"season", 1, false}, {"media_type", "movie", false},
	} {
		raw := map[string]any{"title": "", "year": 0, "media_type": "tv", "season": 0, "episodes": []int{}, "unit_keys": []string{}, "absolute_episode": 0, "is_anime": false, "confidence": 0, "requires_review": true, "reason": "conflicting_episode_coverage"}
		if tc.field != "" {
			raw[tc.field] = tc.value
		}
		b, err := json.Marshal(raw)
		if err != nil {
			t.Fatal(err)
		}
		_, err = validatedWithheldSourceFields([]string{"title", "episode"}, original, b)
		if (err == nil) != tc.valid {
			t.Fatalf("conflicting scope promoted partial coverage: %+v err=%v", tc, err)
		}
	}
}
