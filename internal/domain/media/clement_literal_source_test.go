package media_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/quality"
)

// Clement's original assertions are data, never executable source. The manifest
// Each admitted row is audited separately; source-wide authorization remains false.
var clementLiteralBlock = regexp.MustCompile(`(?ms)^    it\s*\([^\n]+\{\n(.*?)^    \}\);?`)
var clementLiteralAssertion = regexp.MustCompile(`(?s)expect\(parse\(releaseName\)\)\.to\.deep\.(?:equal|include)\(\{(.*?)\}\);?`)
var clementLiteralVariable = regexp.MustCompile(`const releaseName = (` + beastQuotedLiteral + `);`)
var clementSingleNumbering = regexp.MustCompile(`(?i)\b(?:S[0-9]{1,2}E[0-9]{1,3}|[0-9]{1,2}x[0-9]{1,3})\b`)

type clementLiteralRecord struct {
	Labels        map[string]json.RawMessage
	Path          string
	Line          int
	EndLine       int
	AssertionLine int
	Synthetic     bool
}

func clementNumberSet(raw json.RawMessage, episode bool) ([]int, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, fmt.Errorf("missing original numbering")
	}
	var values []int
	if raw[0] == '[' {
		if err := json.Unmarshal(raw, &values); err != nil {
			return nil, err
		}
	} else {
		var n int
		if err := json.Unmarshal(raw, &n); err != nil {
			return nil, err
		}
		values = []int{n}
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("empty original numbering")
	}
	seen := map[int]bool{}
	for _, n := range values {
		if n < 0 || (episode && n == 0) || seen[n] {
			return nil, fmt.Errorf("invalid or duplicate original numbering")
		}
		seen[n] = true
	}
	return values, nil
}

func clementCompleteNumbers(labels map[string]json.RawMessage, singular, plural string) ([]int, error) {
	var result []int
	for _, key := range []string{singular, plural} {
		raw, exists := labels[key]
		if !exists {
			continue
		}
		values, err := clementNumberSet(raw, singular == "episode")
		if err != nil {
			return nil, fmt.Errorf("original %s: %w", key, err)
		}
		if result != nil {
			left, right := slices.Clone(result), slices.Clone(values)
			slices.Sort(left)
			slices.Sort(right)
			if !slices.Equal(left, right) {
				return nil, fmt.Errorf("original scalar/array %s conflict", singular)
			}
		} else {
			result = values
		}
	}
	return result, nil
}

func decodeClementLiteralObject(body string) (map[string]json.RawMessage, error) {
	// This shared decoder only quotes literal keys/strings and JSON-decodes them;
	// calls, computed properties, duplicate keys and expressions cannot run.
	labels, err := decodeBeastLiteralObject(body)
	if err != nil {
		return nil, err
	}
	for _, key := range []string{"title", "source", "resolution", "codec", "group", "type", "quality", "color", "audio", "service", "language", "container", "encoder", "downscaled", "region"} {
		if raw, ok := labels[key]; ok {
			var value string
			if len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, &value) != nil || value == "" {
				return nil, fmt.Errorf("invalid original scalar %s", key)
			}
			if key == "type" && value != "movie" && value != "episode" {
				return nil, fmt.Errorf("unknown original type")
			}
		}
	}
	if raw, exists := labels["year"]; exists {
		var n int
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &n) != nil || n < 1 {
			return nil, fmt.Errorf("invalid original year")
		}
	}
	seasons, err := clementCompleteNumbers(labels, "season", "seasons")
	if err != nil {
		return nil, err
	}
	episodes, err := clementCompleteNumbers(labels, "episode", "episodes")
	if err != nil {
		return nil, err
	}
	if string(labels["type"]) == `"movie"` && (len(seasons) > 0 || len(episodes) > 0) {
		return nil, fmt.Errorf("original movie type conflicts with numbering")
	}
	for _, key := range []string{"sourcelist", "colorlist", "audiolist", "languagelist"} {
		if raw, ok := labels[key]; ok {
			var values []string
			if json.Unmarshal(raw, &values) != nil || len(values) == 0 {
				return nil, fmt.Errorf("invalid original %s", key)
			}
			seen := map[string]bool{}
			for _, v := range values {
				if v == "" || seen[v] {
					return nil, fmt.Errorf("invalid original %s member", key)
				}
				seen[v] = true
			}
		}
	}
	return labels, nil
}

func clementOriginalAssertionIndex() (map[string]clementLiteralRecord, error) {
	root := "../../../testdata/releases/"
	raw, err := os.ReadFile(root + "review-inputs/clement-full-original.manifest.json")
	if err != nil {
		return nil, err
	}
	var manifest struct {
		ID         string `json:"id"`
		Repository string `json:"repository"`
		Commit     string `json:"commit"`
		License    string `json:"license"`
		Status     string `json:"review_status"`
		Flags      struct {
			Accepted  *bool `json:"accepted"`
			Authority *bool `json:"authority"`
			Gold      *bool `json:"formal_golden_acceptance"`
			Canonical *bool `json:"canonical_unit_authorization"`
			Coverage  *bool `json:"coverage_authority"`
			Reviewed  int   `json:"reviewed_entries"`
		} `json:"flags"`
		Counts struct {
			Positive  int `json:"positive_literal_assertions"`
			Unique    int `json:"positive_unique_inputs"`
			Synthetic int `json:"synthetic_unique_inputs"`
		} `json:"counts"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, err
	}
	if manifest.ID != "clement-full-original" || manifest.Repository != "https://github.com/clement-escolano/parse-torrent-title" || manifest.Commit != "98f7d8386aeadaa44a6d43198a5c6fa501273031" || manifest.License != "MIT" || manifest.Status != "partially_audited_literal_assertions" || manifest.Flags.Reviewed != 15 || manifest.Counts.Positive != 31 || manifest.Counts.Unique != 31 || manifest.Counts.Synthetic != 4 {
		return nil, fmt.Errorf("pending source identity/status changed")
	}
	for _, flag := range []*bool{manifest.Flags.Accepted, manifest.Flags.Authority, manifest.Flags.Gold, manifest.Flags.Canonical, manifest.Flags.Coverage} {
		if flag == nil || *flag {
			return nil, fmt.Errorf("pending source cannot gain authority")
		}
	}
	index := map[string]clementLiteralRecord{}
	for _, file := range []struct{ path, hash string }{
		{"review-inputs/clement-full-original.LICENSE", "4b204d94be3a7487c902684b4ef5d240d169d367b17de72d54360f8351fa27ac"},
		{"upstream-mit/clement-full-original.main.js", "b8922941a9f9bd0effb7c61cc440e1a1181e5f824993558650820657662f666c"},
		{"upstream-mit/clement-full-original.title.js", "56a23101d5a19cac27a353b9cfd8bb821d2ad51fd1dc2058bd02dbdf2e89f160"},
	} {
		data, err := os.ReadFile(root + file.path)
		if err != nil {
			return nil, err
		}
		h := sha256.Sum256(data)
		if hex.EncodeToString(h[:]) != file.hash {
			return nil, fmt.Errorf("fixed Clement source/license/manifest hash changed: %s", file.path)
		}
		if !strings.HasSuffix(file.path, ".js") {
			continue
		}
		for _, span := range clementLiteralBlock.FindAllSubmatchIndex(data, -1) {
			body := string(data[span[2]:span[3]])
			variables := clementLiteralVariable.FindAllStringSubmatch(body, -1)
			assertions := clementLiteralAssertion.FindAllStringSubmatchIndex(body, -1)
			if len(variables) != 1 || len(assertions) != 1 {
				return nil, fmt.Errorf("ambiguous original literal block")
			}
			input, err := decodeVirenLiteral(variables[0][1])
			if err != nil {
				return nil, err
			}
			a := assertions[0]
			labels, err := decodeClementLiteralObject(body[a[2]:a[3]])
			if err != nil {
				return nil, err
			}
			if _, exists := index[input]; exists {
				return nil, fmt.Errorf("duplicate original literal input")
			}
			index[input] = clementLiteralRecord{Labels: labels, Path: "testdata/releases/" + file.path, Line: bytes.Count(data[:span[0]], []byte("\n")) + 1, EndLine: bytes.Count(data[:span[1]], []byte("\n")) + 1, AssertionLine: bytes.Count(data[:span[2]+a[0]], []byte("\n")) + 1, Synthetic: strings.HasSuffix(file.path, ".title.js")}
		}
	}
	if len(index) != 31 {
		return nil, fmt.Errorf("original positive literal capacity changed: %d", len(index))
	}
	return index, nil
}

func mappedClementReviewLabels(labels map[string]json.RawMessage, input string) (*corpusEntry, error) {
	// Revalidate the data even when this helper is used by synthetic refusal tests.
	raw, err := json.Marshal(labels)
	if err != nil {
		return nil, err
	}
	checked, err := decodeClementLiteralObject(strings.TrimSuffix(strings.TrimPrefix(string(raw), "{"), "}"))
	if err != nil {
		return nil, err
	}
	mapped := map[string]json.RawMessage{}
	for _, key := range []string{"title", "year", "type", "group"} {
		if v, ok := checked[key]; ok {
			mapped[key] = v
		}
	}
	for _, pair := range [][2]string{{"season", "seasons"}, {"episode", "episodes"}} {
		values, err := clementCompleteNumbers(checked, pair[0], pair[1])
		if err != nil {
			return nil, err
		}
		if values != nil {
			mapped[pair[0]], _ = json.Marshal(values)
		}
	}
	if v, ok := checked["resolution"]; ok {
		mapped["screen_size"] = v
	}
	if v, ok := checked["codec"]; ok {
		mapped["ptt_main_codec"] = v
	}
	if v, ok := checked["source"]; ok {
		var source string
		_ = json.Unmarshal(v, &source)
		names := map[string]string{"bluray": "BluRay", "bdrip": "BDRip", "brrip": "BRRip", "web-dl": "WEB-DL", "webrip": "WEBRip", "web": "WEB", "hdtv": "HDTV", "dvdrip": "DVDRip", "hdrip": "HDRip", "dvd": "DVDRip"}
		name, exists := names[source]
		if !exists {
			return nil, fmt.Errorf("unaudited original source %q", source)
		}
		mapped["ptt_main_quality"], _ = json.Marshal(name)
	}
	for key, value := range labels {
		mapped["clement_original_"+key] = slices.Clone(value)
	}
	return &corpusEntry{Name: input, Expected: mapped}, nil
}

func originalClementReviewLabels(input string) (*corpusEntry, error) {
	index, err := clementOriginalAssertionIndex()
	if err != nil {
		return nil, err
	}
	row, exists := index[input]
	if !exists || row.Synthetic {
		return nil, fmt.Errorf("not a real independently asserted Clement input")
	}
	return mappedClementReviewLabels(row.Labels, input)
}

// Reject partial endpoints, default seasons and type/quality contradictions
// before the shared gold runner is allowed to compare with PulseMedia output.
func validateClementOriginalRebuttals(original *corpusEntry, raw json.RawMessage) error {
	if original == nil {
		return fmt.Errorf("missing independent Clement input")
	}
	var w struct {
		Kind       string          `json:"media_type"`
		Season     int             `json:"season"`
		Episodes   []int           `json:"episodes"`
		Keys       []string        `json:"unit_keys"`
		Absolute   int             `json:"absolute_episode"`
		Confidence float64         `json:"confidence"`
		Review     bool            `json:"requires_review"`
		Quality    json.RawMessage `json:"quality"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return err
	}
	labels := map[string]json.RawMessage{}
	for key, value := range original.Expected {
		if strings.HasPrefix(key, "clement_original_") {
			labels[strings.TrimPrefix(key, "clement_original_")] = value
		}
	}
	mapped, err := mappedClementReviewLabels(labels, original.Name)
	if err != nil {
		return err
	}
	seasons, err := clementCompleteNumbers(labels, "season", "seasons")
	if err != nil {
		return err
	}
	episodes, err := clementCompleteNumbers(labels, "episode", "episodes")
	if err != nil {
		return err
	}
	if v, ok := labels["type"]; ok {
		var kind string
		_ = json.Unmarshal(v, &kind)
		if (kind == "movie" && w.Kind != "movie") || (kind == "episode" && w.Kind != "tv") {
			return fmt.Errorf("original media type changed")
		}
	}
	if len(seasons) > 0 || len(episodes) > 0 || string(labels["type"]) == `"episode"` {
		tokens := clementSingleNumbering.FindAllString(original.Name, -1)
		canonical := len(seasons) == 1 && len(episodes) == 1 && len(tokens) == 1
		if canonical {
			var season, episode int
			var n int
			if strings.HasPrefix(strings.ToUpper(tokens[0]), "S") {
				n, _ = fmt.Sscanf(strings.ToUpper(tokens[0]), "S%dE%d", &season, &episode)
			} else {
				n, _ = fmt.Sscanf(strings.ToLower(tokens[0]), "%dx%d", &season, &episode)
			}
			canonical = n == 2 && season == seasons[0] && episode == episodes[0]
		}
		if !canonical {
			if !w.Review || w.Confidence != 0 || w.Kind != "tv" || w.Season != 0 || len(w.Episodes) != 0 || len(w.Keys) != 0 || w.Absolute != 0 {
				return fmt.Errorf("unbound original complete numbering gained coverage")
			}
		} else if len(w.Keys) > 0 || len(w.Episodes) > 0 || w.Confidence != 0 || w.Season != 0 || !w.Review || w.Absolute != 0 {
			key := fmt.Sprintf("S%02dE%02d", seasons[0], episodes[0])
			if w.Kind != "tv" || w.Season != seasons[0] || !slices.Equal(w.Episodes, episodes) || !slices.Equal(w.Keys, []string{key}) || w.Absolute != 0 {
				return fmt.Errorf("original full episode/season set changed")
			}
		}
	}
	type plainQuality quality.Profile
	var q plainQuality
	if err := json.Unmarshal(w.Quality, &q); err != nil {
		return err
	}
	if err := validateReviewedQualityLabels(mapped.Expected, quality.Profile(q)); err != nil {
		return err
	}
	if err := validatePTTMainQualityRebuttals(mapped.Expected, quality.Profile(q)); err != nil {
		return err
	}
	if raw, ok := labels["sourcelist"]; ok {
		var sources []string
		_ = json.Unmarshal(raw, &sources)
		for _, source := range sources {
			one := map[string]json.RawMessage{"title": labels["title"], "source": json.RawMessage(fmt.Sprintf("%q", source))}
			m, err := mappedClementReviewLabels(one, original.Name)
			// PAL and other unsupported source declarations cannot disappear while an
			// approved competing source survives. Their original values stay preserved.
			if err != nil {
				if q.Source != "" {
					return fmt.Errorf("unsupported original competing source")
				}
				continue
			}
			if err := validatePTTMainQualityRebuttals(m.Expected, quality.Profile(q)); err != nil {
				return err
			}
		}
	}
	if value, ok := labels["quality"]; ok {
		one := map[string]json.RawMessage{"title": labels["title"], "source": value}
		m, err := mappedClementReviewLabels(one, original.Name)
		if err != nil {
			return err
		}
		if err := validatePTTMainQualityRebuttals(m.Expected, quality.Profile(q)); err != nil {
			return err
		}
	}
	if q.HDR != "" {
		var colors []string
		if raw, ok := labels["colorlist"]; ok {
			_ = json.Unmarshal(raw, &colors)
		}
		if raw, ok := labels["color"]; ok {
			var color string
			_ = json.Unmarshal(raw, &color)
			colors = append(colors, color)
		}
		allowed := map[string][]quality.HDR{"SDR": {quality.HDRSDR}, "HDR": {quality.HDR10, quality.HDR10Plus, quality.HDRDolbyVision}, "DV": {quality.HDRDolbyVision}}
		for _, color := range colors {
			if !slices.Contains(allowed[color], q.HDR) {
				return fmt.Errorf("original color contradicted")
			}
		}
	}
	if q.Audio != "" || q.Edition != "" {
		return fmt.Errorf("unmeasured original audio/edition cannot become measured quality")
	}
	return nil
}

func TestClementOriginalSourceIntegrityAndLiteralAdmission(t *testing.T) {
	index, err := clementOriginalAssertionIndex()
	if err != nil {
		t.Fatal(err)
	}
	synthetic, real := 0, 0
	for input, row := range index {
		if row.Path == "" || row.Line < 1 || row.EndLine < row.Line || row.AssertionLine < row.Line || row.AssertionLine > row.EndLine {
			t.Fatal("missing original source position")
		}
		original, err := originalClementReviewLabels(input)
		if row.Synthetic {
			synthetic++
			if err == nil {
				t.Fatal("synthetic title control admitted")
			}
			continue
		}
		real++
		if err != nil {
			t.Fatal(input, err)
		}
		for k, v := range row.Labels {
			if !bytes.Equal(v, original.Expected["clement_original_"+k]) {
				t.Fatal("original field changed", input, k)
			}
		}
	}
	if real != 27 || synthetic != 4 {
		t.Fatal("raw capacity is not a reviewed count", real, synthetic)
	}
	if _, err := originalClementReviewLabels("not an original input"); err == nil {
		t.Fatal("invented input admitted")
	}
}

func TestClementOriginalQualityAndNumberingRebuttals(t *testing.T) {
	for _, tc := range []struct {
		body, input, good string
		bad               []string
	}{
		{`title: "Show", season: 2, episodes: [3], type: "episode"`, "Show.S02E03", `{"media_type":"tv","season":2,"episodes":[3],"unit_keys":["S02E03"],"confidence":0.95,"quality":{}}`, []string{
			`{"media_type":"tv","season":2,"episodes":[3,4],"unit_keys":["S02E03","S02E04"],"confidence":0.95,"quality":{}}`,
			`{"media_type":"tv","season":1,"episodes":[3],"unit_keys":["S01E03"],"confidence":0.95,"quality":{}}`,
			`{"media_type":"movie","unit_keys":["movie"],"confidence":0.9,"quality":{}}`,
		}},
		{`title: "Show", season: 2, episode: 3`, "Show.S01E03", `{"media_type":"tv","confidence":0,"requires_review":true,"quality":{}}`, []string{
			`{"media_type":"tv","season":2,"episodes":[3],"unit_keys":["S02E03"],"confidence":0.95,"quality":{}}`,
		}},
		{`title: "Film", source: "bluray", quality: "dvdrip", color: "HDR", colorlist: ["HDR", "SDR"], codec: "x264", group: "Original"`, "Film.2020", `{"media_type":"movie","quality":{}}`, []string{
			`{"media_type":"movie","quality":{"source":"Bluray"}}`,
			`{"media_type":"movie","quality":{"codec":"HEVC"}}`,
			`{"media_type":"movie","quality":{"hdr":"HDR10"}}`,
			`{"media_type":"movie","quality":{"group":"Other"}}`,
			`{"media_type":"movie","quality":{"audio":"DDP"}}`,
			`{"media_type":"movie","quality":{"edition":"Hybrid"}}`,
		}},
	} {
		labels, err := decodeClementLiteralObject(tc.body)
		if err != nil {
			t.Fatal(err)
		}
		row, err := mappedClementReviewLabels(labels, tc.input)
		if err != nil {
			t.Fatal(err)
		}
		if err := validateClementOriginalRebuttals(row, json.RawMessage(tc.good)); err != nil {
			t.Fatal(err)
		}
		for _, bad := range tc.bad {
			if validateClementOriginalRebuttals(row, json.RawMessage(bad)) == nil {
				t.Fatalf("original contradiction accepted: %s", bad)
			}
		}
	}
}

func TestClementOriginalScalarArrayAndTypeRefusal(t *testing.T) {
	for _, body := range []string{
		`title: "Show", episodes: intRange(1, 12)`, `title: "Show", title: "Other"`, `title: ""`,
		`title: "Show", episode: null`, `title: "Show", episodes: []`, `title: "Show", episodes: [1, 1]`,
		`title: "Show", episodes: [1, 1.5]`, `title: "Show", episode: "1-3"`, `title: "Show", episode: 0`,
		`title: "Show", season: -1`, `title: "Show", seasons: null`, `title: "Show", seasons: [1, 1]`,
		`title: "Show", episode: 1, episodes: [1, 3]`, `title: "Show", season: 1, seasons: [1, 2]`,
		`title: "Show", type: "series"`, `title: "Show", type: null`, `title: "Show", type: 1`,
		`title: "Show", type: "movie", season: 1`, `title: "Show", year: null`,
		`title: "Show", source: null`, `title: "Show", codec: ["x264"]`, `title: "Show", sourcelist: []`,
	} {
		if _, err := decodeClementLiteralObject(body); err == nil {
			t.Fatalf("nonliteral/invalid complete evidence admitted: %s", body)
		}
	}
	labels, err := decodeClementLiteralObject(`title: "A, title: Original", season: 0, seasons: [0], episode: [1, 3], episodes: [3, 1], type: "episode", source: "bluray", sourcelist: ["bluray", "dvd"], audio: "dd", custom: {flag: true}`)
	if err != nil {
		t.Fatal(err)
	}
	original, err := mappedClementReviewLabels(labels, "Show.S00E01-E03")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(original.Expected["episode"], []byte(`[1,3]`)) || !bytes.Equal(original.Expected["clement_original_episodes"], []byte(`[3, 1]`)) || !bytes.Equal(original.Expected["clement_original_custom"], []byte(`{"flag": true}`)) {
		t.Fatal("complete original arrays/fields lost", original)
	}
	refusal := json.RawMessage(`{"media_type":"tv","season":0,"episodes":[],"unit_keys":[],"absolute_episode":0,"confidence":0,"requires_review":true,"quality":{}}`)
	if err := validateClementOriginalRebuttals(original, refusal); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		`{"media_type":"tv","season":0,"episodes":[1],"unit_keys":["S00E01"],"confidence":0.95,"quality":{}}`,
		`{"media_type":"tv","season":0,"episodes":[1,2,3],"unit_keys":["S00E01","S00E02","S00E03"],"confidence":0.95,"quality":{}}`,
		`{"media_type":"tv","confidence":0,"requires_review":true,"quality":{"source":"Bluray"}}`,
		`{"media_type":"movie","confidence":0,"requires_review":true,"quality":{}}`,
	} {
		if validateClementOriginalRebuttals(original, json.RawMessage(bad)) == nil {
			t.Fatalf("partial/expanded/contradictory source accepted: %s", bad)
		}
	}
	for _, body := range []string{`title: "Show", episode: 8`, `title: "Show", season: 2`, `title: "Show", type: "episode"`} {
		labels, err := decodeClementLiteralObject(body)
		if err != nil {
			t.Fatal(err)
		}
		row, err := mappedClementReviewLabels(labels, "Show - 08")
		if err != nil {
			t.Fatal(err)
		}
		if err := validateClementOriginalRebuttals(row, refusal); err != nil {
			t.Fatal(err)
		}
		if validateClementOriginalRebuttals(row, json.RawMessage(`{"media_type":"tv","season":1,"episodes":[8],"unit_keys":["S01E08"],"confidence":0.95,"quality":{}}`)) == nil {
			t.Fatal("original numbering inferred default season")
		}
	}
}
