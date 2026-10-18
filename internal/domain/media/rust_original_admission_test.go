package media_test

import (
	"encoding/json"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"slices"
	"strings"
	"testing"
)

// Scalar source assertions refute manual reviews; they never grant source,
// codec, group, mapped coverage, measured-file or execution authority.
func validateRustQualityRebuttals(labels map[string]json.RawMessage, q quality.Profile) error {
	for _, key := range []string{"resolution", "quality", "codec", "group"} {
		raw, ok := labels["rust_"+key]
		if !ok {
			continue
		}
		var text string
		if e := json.Unmarshal(raw, &text); e != nil || text == "" {
			return fmt.Errorf("invalid original Rust %s", key)
		}
		switch key {
		case "resolution":
			if q.Resolution != "" && string(q.Resolution) != text {
				return fmt.Errorf("resolution contradicts original getter")
			}
		case "quality":
			allowed, known := map[string][]quality.Source{"BrRip": {quality.SourceBluray, quality.SourceRemux}, "HDRip": {}, "WEB-DL": {quality.SourceWEBDL}, "WEB": {quality.SourceWEBDL, quality.SourceWEBRip}}[text]
			if !known {
				return fmt.Errorf("unreviewed source getter %q", text)
			}
			if q.Source != "" && !slices.Contains(allowed, q.Source) {
				return fmt.Errorf("source contradicts original getter")
			}
		case "codec":
			code, known := map[string]quality.Codec{"x264": quality.CodecH264, "H264": quality.CodecH264, "XViD": ""}[text]
			if !known {
				return fmt.Errorf("unreviewed codec getter %q", text)
			}
			if q.Codec != "" && q.Codec != code {
				return fmt.Errorf("codec contradicts original getter")
			}
		case "group":
			canonical := text
			for _, relay := range []string{"[ettv]", "[eztv]", "[rartv]", "[tgx]", "[rarbg]"} {
				if strings.HasSuffix(strings.ToLower(text), relay) {
					canonical = text[:len(text)-len(relay)]
					break
				}
			}
			if q.Group != "" && q.Group != text && q.Group != canonical {
				return fmt.Errorf("group contradicts original getter")
			}
		}
	}
	return nil
}

func TestRustScalarAdmissionCannotCreateCoverageOrRewriteLabels(t *testing.T) {
	makePool := func(observations string) reviewInputPool {
		t.Helper()
		var p reviewInputPool
		if e := json.Unmarshal([]byte(`{"entries":[{"release_name":"Original","observations":`+observations+`}]}`), &p); e != nil {
			t.Fatal(e)
		}
		return p
	}
	p := makePool(`[{"source_line":1,"original_fields":{"title":"Original","year":null,"season":1,"episode":9,"resolution":"1080p","audio":"DD5.1","imdb_tag":"tt1234567","language":"English"}}]`)
	row, e := originalRustReviewLabels(p, "Original")
	if e != nil || len(row.Expected) != 5 || string(row.Expected["title"]) != `"Original"` || string(row.Expected["year"]) != "null" || string(row.Expected["rust_resolution"]) != `"1080p"` || string(row.Expected["rust_season"]) != "1" || string(row.Expected["rust_first_episode"]) != "9" {
		t.Fatal("literal labels changed", row, e)
	}
	for _, key := range []string{"season", "episode", "unit_keys", "audio", "language", "imdb_tag", "source", "confidence", "reason", "requires_review"} {
		if _, ok := row.Expected[key]; ok {
			t.Fatal("partial getter gained authority", key)
		}
	}
	if _, e := originalRustReviewLabels(p, "local variation"); e == nil {
		t.Fatal("invented input admitted")
	}
	p.Entries = append(p.Entries, p.Entries[0])
	if _, e := originalRustReviewLabels(p, "Original"); e == nil {
		t.Fatal("duplicate input admitted")
	}
	for _, bad := range []string{
		`[{"source_line":1,"original_fields":{"year":2008}}]`,
		`[{"source_line":1,"original_fields":{"title":""}}]`,
		`[{"source_line":0,"original_fields":{"title":"Original"}}]`,
		`[{"source_line":1,"original_fields":{"title":"Original","year":"2008"}}]`,
		`[{"source_line":1,"original_fields":{"title":"Original","year":2008}},{"source_line":2,"original_fields":{"title":"Original","year":2009}}]`,
		`[{"source_line":1,"original_fields":{"title":"Original","codec":{"symbol_ref":"Codec.H264"}}}]`,
		`[{"source_line":1,"original_fields":{"title":"Original","season":1000}}]`,
		`[{"source_line":1,"original_fields":{"title":"Original","season":-1}}]`,
		`[{"source_line":1,"original_fields":{"title":"Original","episode":0}}]`,
		`[{"source_line":1,"original_fields":{"title":"Original","episode":10000}}]`,
		`[{"source_line":1,"original_fields":{"title":"Original","episode":9}},{"source_line":2,"original_fields":{"title":"Original","episode":10}}]`,
		`[{"source_line":1,"original_fields":{"title":"Original","resolution":true}}]`,
		`[{"source_line":1,"original_fields":{"title":"Original"}},{"source_line":2,"original_fields":{"title":"OTHER"}}]`,
	} {
		if _, e := originalRustReviewLabels(makePool(bad), "Original"); e == nil {
			t.Fatal("unsupported/conflicting original admitted", bad)
		}
	}
	for _, tc := range []struct {
		labels string
		q      quality.Profile
		bad    bool
	}{
		{`{"rust_resolution":"1080p"}`, quality.Profile{Resolution: quality.Res2160p}, true},
		{`{"rust_quality":"HDRip"}`, quality.Profile{Source: quality.SourceWEBDL}, true},
		{`{"rust_codec":"XViD"}`, quality.Profile{Codec: quality.CodecH264}, true},
		{`{"rust_group":"ASAP[ettv]"}`, quality.Profile{Group: "OTHER"}, true},
		{`{"rust_group":"ASAP[ettv]"}`, quality.Profile{Group: "asap"}, true},
		{`{"rust_group":"Qman[UTR]"}`, quality.Profile{Group: "Qman"}, true},
		{`{"rust_quality":"unknown"}`, quality.Profile{}, true},
		{`{"rust_codec":{"symbol_ref":"Codec.H264"}}`, quality.Profile{}, true},
		{`{"rust_resolution":"1080p","rust_quality":"WEB-DL","rust_codec":"H264"}`, quality.Profile{Resolution: quality.Res1080p, Source: quality.SourceWEBDL, Codec: quality.CodecH264}, false},
		{`{"rust_group":"ASAP[ettv]"}`, quality.Profile{Group: "ASAP"}, false},
		{`{"rust_group":"Qman[UTR]"}`, quality.Profile{Group: "Qman[UTR]"}, false},
		{`{"rust_quality":"HDRip","rust_codec":"XViD"}`, quality.Profile{}, false},
	} {
		var labels map[string]json.RawMessage
		if e := json.Unmarshal([]byte(tc.labels), &labels); e != nil {
			t.Fatal(e)
		}
		if bad := validateRustQualityRebuttals(labels, tc.q) != nil; bad != tc.bad {
			t.Fatal("original attribute rebuttal weakened", tc.labels, tc.q, bad)
		}
	}
	for _, raw := range []string{`"\u{1234}"`, `"unterminated`, `"\q"`} {
		if _, e := decodeRustScalar([]byte(raw)); e == nil {
			t.Fatal("unsupported Rust literal repaired", raw)
		}
	}
}
