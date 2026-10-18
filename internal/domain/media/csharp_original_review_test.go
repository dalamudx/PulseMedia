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
	"slices"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
)

// These patterns read inert literal assertions only; they do not execute C#,
// constructors, getters or expressions. Getter audio is not a measured fact.
var csharpConstructors = regexp.MustCompile(`new Torrent\s*\(\s*("(?:[^"\\]|\\.)*")\s*\)`)
var csharpAssertions = regexp.MustCompile(`Assert\.AreEqual\(\s*("(?:[^"\\]|\\.)*"|[0-9]+)\s*,\s*torrent\.([A-Za-z]+)\s*\);`)

func TestPinnedCSharpAssertionsRemainOriginalAndPartial(t *testing.T) {
	root := "../../../testdata/releases/review-inputs/titleparser"
	files := []struct{ suffix, hash string }{
		{".json", "fe05d03f3ed4031e6b78b382f03aa864b140fc9d09451dbbdb0b6824066e98bb"},
		{".source", "ba1eb5502901f0f369995fc9db6df5827e830042fbab9ce616cad348789b65f3"},
		{".LICENSE", "3009a1211aea38b4e08a403c44cea0002fd531cc1906ed448e387c33c00a1475"},
	}
	data := map[string][]byte{}
	for _, f := range files {
		raw, err := os.ReadFile(root + f.suffix)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(raw)
		if hex.EncodeToString(sum[:]) != f.hash {
			t.Fatal("original C# bytes changed", f.suffix)
		}
		data[f.suffix] = raw
	}
	if !bytes.Contains(data[".LICENSE"], []byte("Permission is hereby granted")) {
		t.Fatal("MIT notice missing")
	}
	pool, err := decodeUnapprovedPool(data[".json"])
	if err != nil {
		t.Fatal(err)
	}
	if pool.SourceID != "titleparser" || pool.Provenance.Commit != "604b716c30c3338fbc1d73157f8000056a3b5132" || pool.Provenance.Path != "TorrentTitleParser.Tests/TitleTests.cs" || pool.Provenance.SourceHash != files[1].hash || pool.Provenance.LicenseHash != files[2].hash || pool.Provenance.Count != 27 || len(pool.Entries) != 27 {
		t.Fatal("original provenance changed")
	}
	source := data[".source"]
	matches := csharpConstructors.FindAllSubmatchIndex(source, -1)
	type assertion struct {
		name   string
		fields map[string]json.RawMessage
	}
	located := map[int]assertion{}
	for i, m := range matches {
		var name string
		if err := json.Unmarshal(source[m[2]:m[3]], &name); err != nil {
			t.Fatal("unsupported source literal", err)
		}
		end := len(source)
		if i+1 < len(matches) {
			end = matches[i+1][0]
		}
		fields := map[string]json.RawMessage{}
		for _, a := range csharpAssertions.FindAllSubmatch(source[m[1]:end], -1) {
			var scalar any
			if err := json.Unmarshal(a[1], &scalar); err != nil {
				t.Fatal(err)
			}
			switch scalar.(type) {
			case string, float64:
			default:
				t.Fatal("nonliteral assertion")
			}
			raw, _ := json.Marshal(scalar)
			key := string(a[2])
			if old, ok := fields[key]; ok && !bytes.Equal(old, raw) {
				t.Fatal("conflicting literal source assertions", name, key)
			}
			fields[key] = raw
		}
		line := bytes.Count(source[:m[0]], []byte("\n")) + 1
		located[line] = assertion{name, fields}
	}
	count := 0
	for _, row := range pool.Entries {
		for _, o := range row.Observations {
			a, ok := located[o.Line]
			if !ok || a.name != row.Name || !reflect.DeepEqual(a.fields, o.Fields) {
				t.Fatalf("original literal/position mismatch %q line %d: source=%v pool=%v", row.Name, o.Line, a.fields, o.Fields)
			}
			delete(located, o.Line)
			count++
		}
	}
	if count != len(matches) || len(located) != 0 {
		t.Fatal("unrepresented or repeated source assertion location")
	}
}

func originalCSharpReviewLabels(pool reviewInputPool, input string) (*corpusEntry, error) {
	var found *corpusEntry
	for _, row := range pool.Entries {
		if row.Name != input {
			continue
		}
		if found != nil {
			return nil, fmt.Errorf("duplicate original input")
		}
		labels := map[string]json.RawMessage{}
		if len(row.Observations) == 0 {
			return nil, fmt.Errorf("unlocated original")
		}
		for _, o := range row.Observations {
			if o.Line <= 0 {
				return nil, fmt.Errorf("missing original source position")
			}
			for _, key := range []string{"Title", "Year", "Season", "Episode", "Resolution", "Quality", "Codec", "Group"} {
				raw, ok := o.Fields[key]
				if !ok {
					continue
				}
				if key == "Year" || key == "Season" || key == "Episode" {
					var n int
					if err := json.Unmarshal(raw, &n); err != nil {
						return nil, fmt.Errorf("nonliteral %s", key)
					}
					if (key == "Year" && (n < 1900 || n > 2099)) || (key == "Season" && (n < 0 || n > 999)) || (key == "Episode" && (n < 1 || n > 9999)) {
						return nil, fmt.Errorf("invalid literal %s", key)
					}
				} else {
					var text string
					if err := json.Unmarshal(raw, &text); err != nil || text == "" {
						return nil, fmt.Errorf("nonliteral or empty %s", key)
					}
				}
				if old, ok := labels[key]; ok && !bytes.Equal(old, raw) {
					return nil, fmt.Errorf("conflicting original %s", key)
				}
				labels[key] = append(json.RawMessage(nil), raw...)
			}
		}
		if len(labels["Title"]) == 0 {
			return nil, fmt.Errorf("independent title assertion required")
		}
		expected := map[string]json.RawMessage{"title": labels["Title"]}
		if year, ok := labels["Year"]; ok {
			expected["year"] = year
		}
		for _, key := range []string{"Season", "Episode", "Resolution", "Quality", "Codec", "Group"} {
			if raw, ok := labels[key]; ok {
				expected["csharp_"+key] = raw
			}
		}
		found = &corpusEntry{Name: input, Expected: expected}
	}
	if found == nil {
		return nil, fmt.Errorf("not an original labelled input")
	}
	return found, nil
}

func validateCSharpRebuttals(labels map[string]json.RawMessage, q quality.Profile, kind string, season int, episodes []int, units []string) error {
	for _, key := range []string{"Resolution", "Quality", "Codec", "Group"} {
		raw, ok := labels["csharp_"+key]
		if !ok {
			continue
		}
		var text string
		if err := json.Unmarshal(raw, &text); err != nil || text == "" {
			return fmt.Errorf("invalid original %s", key)
		}
		switch key {
		case "Resolution":
			if q.Resolution != "" && string(q.Resolution) != text {
				return fmt.Errorf("resolution contradicts original")
			}
		case "Quality":
			allowed, known := map[string][]quality.Source{"UHD BluRay": {quality.SourceBluray, quality.SourceRemux}, "Bluray": {quality.SourceBluray, quality.SourceRemux}, "BluRay": {quality.SourceBluray, quality.SourceRemux}, "WEB-DL": {quality.SourceWEBDL}, "WEBRip": {quality.SourceWEBRip}, "HDTV": {quality.SourceHDTV}, "HDRip": {}, "DVDRip": {}, "Cam": {}, "TELESYNC": {}, "TS": {}, "TeleSync": {}}[text]
			if !known || (q.Source != "" && !slices.Contains(allowed, q.Source)) {
				return fmt.Errorf("source contradicts/unreviewed original")
			}
		case "Codec":
			code, known := map[string]quality.Codec{"x264": quality.CodecH264, "X264": quality.CodecH264, "H264": quality.CodecH264, "X265": quality.CodecHEVC, "x265": quality.CodecHEVC, "H265": quality.CodecHEVC, "HEVC": quality.CodecHEVC}[text]
			if !known || (q.Codec != "" && q.Codec != code) {
				return fmt.Errorf("codec contradicts/unreviewed original")
			}
		case "Group":
			if q.Group != "" && q.Group != text {
				return fmt.Errorf("group contradicts original")
			}
		}
	}
	for _, key := range []string{"Season", "Episode"} {
		raw, ok := labels["csharp_"+key]
		if !ok {
			continue
		}
		var n int
		if err := json.Unmarshal(raw, &n); err != nil {
			return err
		}
		if len(units) == 0 {
			continue
		} // Held source assertions cannot confer units.
		if kind != "tv" || (key == "Season" && n != season) || (key == "Episode" && !slices.Contains(episodes, n)) {
			return fmt.Errorf("canonical coverage contradicts partial %s getter", key)
		}
	}
	return nil
}

func TestCSharpScalarAdmissionCannotMintOrRewriteEvidence(t *testing.T) {
	pool := func(raw string) reviewInputPool {
		t.Helper()
		var p reviewInputPool
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	valid := `{"entries":[{"release_name":"Original","observations":[{"source_line":4,"original_fields":{"Title":"Original","Season":1,"Episode":9,"Audio":"DTS","AudioChannels":"7.1"}}]}]}`
	original, err := originalCSharpReviewLabels(pool(valid), "Original")
	if err != nil || len(original.Expected) != 3 || len(original.Expected["episode"]) != 0 || len(original.Expected["audio"]) != 0 || len(original.Expected["unit_keys"]) != 0 {
		t.Fatal("partial getter became a fact", original, err)
	}
	for _, observations := range []string{
		`[]`,
		`[{"source_line":0,"original_fields":{"Title":"Original"}}]`,
		`[{"source_line":4,"original_fields":{"Quality":"BluRay"}}]`,
		`[{"source_line":4,"original_fields":{"Title":""}}]`,
		`[{"source_line":4,"original_fields":{"Title":"Original","Year":"2000"}}]`,
		`[{"source_line":4,"original_fields":{"Title":"Original","Season":-1}}]`,
		`[{"source_line":4,"original_fields":{"Title":"Original","Episode":0}}]`,
		`[{"source_line":4,"original_fields":{"Title":"Original","Episode":10000}}]`,
		`[{"source_line":4,"original_fields":{"Title":"Original","Codec":{"symbol_ref":"Codec.H264"}}}]`,
		`[{"source_line":4,"original_fields":{"Title":"Original","Year":2000}},{"source_line":8,"original_fields":{"Title":"Original","Year":2001}}]`,
		`[{"source_line":4,"original_fields":{"Title":"Original"}},{"source_line":8,"original_fields":{"Title":"Other"}}]`,
	} {
		if _, err := originalCSharpReviewLabels(pool(`{"entries":[{"release_name":"Original","observations":`+observations+`}]}`), "Original"); err == nil {
			t.Fatal("unsupported original admitted", observations)
		}
	}
	duplicate := pool(valid)
	duplicate.Entries = append(duplicate.Entries, duplicate.Entries[0])
	if _, err := originalCSharpReviewLabels(duplicate, "Original"); err == nil {
		t.Fatal("duplicate input admitted")
	}
	for _, tc := range []struct {
		labels string
		q      quality.Profile
		bad    bool
	}{
		{`{"csharp_Quality":"DVDRip"}`, quality.Profile{Source: quality.SourceBluray}, true},
		{`{"csharp_Quality":"HDRip"}`, quality.Profile{Source: quality.SourceWEBDL}, true},
		{`{"csharp_Quality":"UHD BluRay"}`, quality.Profile{Source: quality.SourceRemux}, false},
		{`{"csharp_Resolution":"1080p"}`, quality.Profile{Resolution: quality.Res2160p}, true},
		{`{"csharp_Codec":"HEVC"}`, quality.Profile{Codec: quality.CodecH264}, true},
		{`{"csharp_Group":"EVO"}`, quality.Profile{Group: "Other"}, true},
		{`{"csharp_Group":"EVO"}`, quality.Profile{}, false},
		{`{"csharp_Quality":"Unknown"}`, quality.Profile{}, true},
	} {
		var labels map[string]json.RawMessage
		if err := json.Unmarshal([]byte(tc.labels), &labels); err != nil {
			t.Fatal(err)
		}
		if bad := validateCSharpRebuttals(labels, tc.q, "movie", 0, nil, nil) != nil; bad != tc.bad {
			t.Fatal("original quality rebuttal weakened", tc.labels)
		}
	}
	for _, tc := range []struct {
		kind     string
		season   int
		episodes []int
		units    []string
		bad      bool
	}{
		{"tv", 1, []int{9, 10}, []string{"S01E09", "S01E10"}, false},
		{"tv", 1, []int{10}, []string{"S01E10"}, true},
		{"tv", 2, []int{9}, []string{"S02E09"}, true},
		{"movie", 0, nil, []string{"movie"}, true},
		{"tv", 0, nil, nil, false},
	} {
		if bad := validateCSharpRebuttals(original.Expected, quality.Profile{}, tc.kind, tc.season, tc.episodes, tc.units) != nil; bad != tc.bad {
			t.Fatal("partial first getter certified wrong canonical coverage", tc)
		}
	}
}

func TestCSharpPreReleaseClaimsRemainUnknownForAcquisitionAndOwnership(t *testing.T) {
	rules := quality.PolicyRules{
		ResolutionOrder: map[quality.Resolution]int{quality.Res1080p: 100},
		SourceOrder:     map[quality.Source]int{quality.SourceBluray: 100, quality.SourceWEBDL: 100},
	}
	for _, input := range []string{
		"Inside Out 2 2024 1080p TELESYNC x264 COLLECTiVE",
		"The Flash 2023 1080p V2 Cam X264 Will1869",
	} {
		t.Run(input, func(t *testing.T) {
			parsed := media.RecognizeRelease(input)
			if parsed.Quality.Source != "" || quality.KnownProfile(parsed.Quality) {
				t.Fatal("unknown pre-release source became a quality fact", parsed)
			}
			decision := quality.EvaluateCandidate(rules, input, parsed.Quality)
			if decision.Passed || decision.MeetsCutoff || decision.Score != 0 || decision.RejectedReason != "quality_unknown" {
				t.Fatal("syntax confidence conferred candidate eligibility", decision)
			}
			owned := quality.AssessOwnership(rules, []quality.FileEvidence{{Title: input, Profile: parsed.Quality, Verified: true}})
			if owned.Owned || !owned.Unknown || owned.Score != 0 {
				t.Fatal("unknown quality conferred ownership", owned)
			}
		})
	}
}
