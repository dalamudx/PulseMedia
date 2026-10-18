package media_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
)

const chillAuditDir = "../../../testdata/releases"

func chillPinnedPath(name string) string {
	if name == "original.LICENSE" {
		return chillAuditDir + "/review-inputs/chill-parser-original.LICENSE"
	}
	return chillAuditDir + "/upstream-mit/chill-parser-original." + name
}

type chillOriginal struct {
	Input     string                     `json:"input"`
	File      string                     `json:"original_file"`
	Line      int                        `json:"input_line"`
	Index     int                        `json:"original_index"`
	Start     int                        `json:"assertion_start_line"`
	End       int                        `json:"assertion_end_line"`
	Block     string                     `json:"original_assertion_block"`
	Literal   map[string]json.RawMessage `json:"original_literal_fields"`
	Asserted  map[string]json.RawMessage `json:"original_asserted_fields"`
	Selected  []string                   `json:"selected_fields"`
	Positions map[string]int             `json:"field_lines"`
	Expected  map[string]json.RawMessage `json:"expected"`
}

func chillOriginals() (map[string]chillOriginal, error) {
	pins := map[string]string{"parser_test.go.source": "736c8736b4ac98e911a2d4add59673a9c555fb99fe0e2a0bb802d2df70dbadea", "fixture_accuracy_test.go.source": "0f6a30e98072e4706baa8678faa6aa47b57116620133d770057e7c543e5b57bf", "original.LICENSE": "5c01505f1974f6562d9f972acbcba24504ce20e68a58241b5f8d0dfc85512b84"}
	index := []byte(chillFrozenSourceIndex)
	for _, embedded := range []struct{ raw, sha string }{{chillFrozenSourceIndex, "42ef6a95c4183ebe7add3c130ca8e4a1b64ea1154496ac3bc73436c16c93e885"}, {chillFrozenContracts, "458b69a1eb1e5dd5c8e42219aadfe9a025fe5f426b2e55e56f59fd45e69b39be"}} {
		sum := sha256.Sum256([]byte(embedded.raw))
		if hex.EncodeToString(sum[:]) != embedded.sha {
			return nil, fmt.Errorf("frozen embedded evidence changed")
		}
	}
	for name, pin := range pins {
		raw, e := os.ReadFile(chillPinnedPath(name))
		if e != nil {
			return nil, e
		}
		sum := sha256.Sum256(raw)
		if hex.EncodeToString(sum[:]) != pin {
			return nil, fmt.Errorf("chill immutable hash mismatch: %s", name)
		}
		if name == "source-index.json" {
			index = raw
		}
	}
	var rows map[string]chillOriginal
	if e := json.Unmarshal(index, &rows); e != nil {
		return nil, e
	}
	if len(rows) != 33 {
		return nil, fmt.Errorf("chill source count changed")
	}
	for input, r := range rows {
		raw, e := os.ReadFile(chillPinnedPath(r.File + ".source"))
		if e != nil {
			return nil, e
		}
		lines := strings.Split(string(raw), "\n")
		if r.Input != input || r.Line < 1 || r.Line > len(lines) || !strings.Contains(lines[r.Line-1], input) || r.Start > r.Line || r.End < r.Line || !strings.Contains(strings.Join(lines[r.Start-1:r.End], "\n"), r.Block) {
			return nil, fmt.Errorf("original input/block/line changed: %s", input)
		}
	}
	return rows, nil
}
func originalChillReviewLabels(input string) (*corpusEntry, error) {
	rows, e := chillOriginals()
	if e != nil {
		return nil, e
	}
	r, ok := rows[input]
	if !ok {
		return nil, fmt.Errorf("input not pinned chill literal")
	}
	if _, ok := r.Asserted["Title"]; !ok {
		return nil, fmt.Errorf("missing independently asserted original title")
	}
	return &corpusEntry{Name: input, Expected: r.Expected}, nil
}
func validateChillComplete(input string, raw json.RawMessage) error {
	rows, e := chillOriginals()
	if e != nil {
		return e
	}
	o, ok := rows[input]
	if !ok {
		return fmt.Errorf("not an original chill input")
	}
	var want struct {
		Title      string            `json:"title"`
		Year       int               `json:"year"`
		Type       string            `json:"media_type"`
		Season     int               `json:"season"`
		Episodes   []int             `json:"episodes"`
		Keys       []string          `json:"unit_keys"`
		Absolute   int               `json:"absolute_episode"`
		Anime      bool              `json:"is_anime"`
		Confidence float64           `json:"confidence"`
		Review     bool              `json:"requires_review"`
		Reason     string            `json:"reason"`
		Quality    map[string]string `json:"quality"`
	}
	if e = json.Unmarshal(raw, &want); e != nil {
		return e
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	for _, k := range []string{"title", "year", "media_type", "season", "episodes", "unit_keys", "absolute_episode", "is_anime", "confidence", "requires_review", "reason", "quality"} {
		if _, ok := fields[k]; !ok {
			return fmt.Errorf("incomplete manual field %s", k)
		}
	}
	if len(want.Quality) != 7 {
		return fmt.Errorf("incomplete quality")
	}
	for _, k := range []string{"resolution", "source", "codec", "hdr", "audio", "edition", "group"} {
		if _, ok := want.Quality[k]; !ok {
			return fmt.Errorf("missing quality %s", k)
		}
	}
	str := func(k string) (string, bool) {
		r, ok := o.Asserted[k]
		var v string
		if ok {
			_ = json.Unmarshal(r, &v)
		}
		return v, ok
	}
	num := func(k string) (int, bool) {
		r, ok := o.Asserted[k]
		var v int
		if ok {
			_ = json.Unmarshal(r, &v)
		}
		return v, ok
	}
	if s, ok := str("Title"); ok && s != want.Title {
		return fmt.Errorf("rewrote original title")
	}
	if n, ok := num("Year"); ok && n != want.Year {
		return fmt.Errorf("rewrote original year")
	}
	if n, ok := num("Season"); ok && n != want.Season {
		return fmt.Errorf("rewrote original season")
	}
	if n, ok := num("Episode"); ok {
		full := []int{n}
		if end, ok := num("EpisodeEnd"); ok && end > 0 {
			full = nil
			for x := n; x <= end; x++ {
				full = append(full, x)
			}
		}
		if !slices.Equal(full, want.Episodes) {
			return fmt.Errorf("rewrote full original scalar/interval coverage")
		}
		keys := []string{}
		for _, ep := range full {
			keys = append(keys, fmt.Sprintf("S%02dE%02d", want.Season, ep))
		}
		if want.Type != "tv" || want.Absolute != 0 || want.Anime || !slices.Equal(keys, want.Keys) {
			return fmt.Errorf("rewrote full original unit keys")
		}
	}
	if _, season := num("Season"); season {
		if _, ep := num("Episode"); !ep && (len(want.Episodes) > 0 || len(want.Keys) > 0 || want.Confidence != 0 || !want.Review) {
			return fmt.Errorf("promoted season-only coverage")
		}
	}
	if complete, ok := o.Asserted["Complete"]; ok && string(complete) == "true" && (len(want.Episodes) > 0 || len(want.Keys) > 0 || want.Confidence != 0 || !want.Review) {
		return fmt.Errorf("promoted complete-series declaration")
	}
	if title, ok := str("Title"); !ok || title == "" {
		return fmt.Errorf("missing independently asserted original title")
	}
	q := want.Quality
	if q["audio"] != "" || q["edition"] != "" {
		return fmt.Errorf("unsupported measured audio or edition promotion")
	}
	if !slices.Contains([]string{"", "Bluray", "Remux", "WEB-DL", "WEBRip", "HDTV"}, q["source"]) || !slices.Contains([]string{"", "H264", "HEVC", "AV1"}, q["codec"]) || !slices.Contains([]string{"", "1080p", "720p", "2160p", "480p"}, q["resolution"]) || !slices.Contains([]string{"", "DV", "HDR10+", "HDR10", "SDR"}, q["hdr"]) {
		return fmt.Errorf("unsupported quality value")
	}
	if s, ok := str("Resolution"); ok && q["resolution"] != s {
		return fmt.Errorf("rewrote original resolution")
	}
	if s, ok := str("Quality"); ok {
		m := map[string]string{"BluRay": "Bluray", "REMUX": "Remux", "WEB-DL": "WEB-DL", "WEBRip": "WEBRip", "HDTV": "HDTV", "HDRip": "", "WEB": ""}
		v, known := m[s]
		if !known || q["source"] != v {
			return fmt.Errorf("unsupported or contradictory source promotion from %q", s)
		}
	}
	if s, ok := str("Codec"); ok {
		m := map[string]string{"x264": "H264", "H264": "H264", "AVC": "H264", "H265": "HEVC", "x265": "HEVC", "HEVC": "HEVC", "XViD": ""}
		v, known := m[s]
		if !known || q["codec"] != v {
			return fmt.Errorf("unsupported or contradictory codec promotion from %q", s)
		}
	}
	if s, ok := str("HDR"); ok {
		m := map[string]string{"DV HDR": "DV", "HDR10+ DV": "DV", "HDR10+": "HDR10+"}
		v, known := m[s]
		if !known || q["hdr"] != v {
			return fmt.Errorf("contradictory HDR representative")
		}
	}
	if s, ok := str("Group"); ok && q["group"] != "" && q["group"] != s {
		return fmt.Errorf("rewrote original group")
	}
	if s, ok := str("Website"); ok && q["group"] != "" && q["group"] == s {
		return fmt.Errorf("promoted website into group")
	}
	if q["group"] != "" && !strings.Contains(input, "-"+q["group"]) && !strings.Contains(input, "["+q["group"]+"]") {
		return fmt.Errorf("promoted unbound group")
	}
	return nil
}
func TestChillPinnedReaderAndContradictionControls(t *testing.T) {
	rows, e := chillOriginals()
	if e != nil {
		t.Fatal(e)
	}
	if len(rows) != 33 {
		t.Fatal("source count")
	}
	raw := []byte(chillFrozenContracts)
	var corpus struct {
		Entries []struct {
			ID       string          `json:"id"`
			Input    string          `json:"input"`
			Expected json.RawMessage `json:"expected"`
		}
	}
	if e = json.Unmarshal(raw, &corpus); e != nil {
		t.Fatal(e)
	}
	byID := map[string]int{}
	admitted, pending := 0, 0
	for i, r := range corpus.Entries {
		byID[r.ID] = i
		e = validateChillComplete(r.Input, r.Expected)
		_, hasTitle := rows[r.Input].Asserted["Title"]
		if !hasTitle {
			pending++
			if e == nil || !strings.Contains(e.Error(), "original title") {
				t.Errorf("titleless contract admitted %s: %v", r.ID, e)
			}
			continue
		}
		if e != nil {
			t.Errorf("valid frozen source contract %s: %v", r.ID, e)
		} else {
			admitted++
		}
	}
	if admitted != 24 || pending != 9 {
		t.Fatalf("original oracle count %d/%d", admitted, pending)
	}
	controls := []struct {
		Name, ID string
		Change   func(map[string]any)
	}{
		{"range_truncated", "chill-f-215", func(v map[string]any) { v["episodes"] = []int{1} }},
		{"range_extra", "chill-f-215", func(v map[string]any) { v["episodes"] = []int{1, 2, 3, 4, 5, 6, 7, 8, 9} }},
		{"range_key_truncated", "chill-f-215", func(v map[string]any) { v["unit_keys"] = []string{"S01E01"} }},
		{"range_key_extra", "chill-f-215", func(v map[string]any) {
			v["unit_keys"] = []string{"S01E01", "S01E02", "S01E03", "S01E04", "S01E05", "S01E06", "S01E07", "S01E08", "S01E09"}
		}},
		{"scalar_extra", "chill-p-669", func(v map[string]any) { v["episodes"] = []int{1, 2} }},
		{"unsupported_hdrip", "chill-f-80", func(v map[string]any) { v["quality"].(map[string]any)["source"] = "Bluray" }},
		{"unsupported_xvid", "chill-f-80", func(v map[string]any) { v["quality"].(map[string]any)["codec"] = "H264" }},
		{"platform_source", "chill-f-48", func(v map[string]any) { v["quality"].(map[string]any)["source"] = "CR" }},
		{"website_group", "chill-f-32", func(v map[string]any) { v["quality"].(map[string]any)["group"] = "Anime Time" }},
		{"unsupported_audio", "chill-f-32", func(v map[string]any) { v["quality"].(map[string]any)["audio"] = "AAC" }},
		{"unsupported_edition", "chill-f-64", func(v map[string]any) { v["quality"].(map[string]any)["edition"] = "Director's Cut" }},
		{"season_invented_episode", "chill-f-157", func(v map[string]any) {
			v["episodes"] = []int{1}
			v["unit_keys"] = []string{"S01E01"}
			v["confidence"] = 0.95
			v["requires_review"] = false
		}},
	}
	for _, c := range controls {
		t.Run(c.Name, func(t *testing.T) {
			r := corpus.Entries[byID[c.ID]]
			var v map[string]any
			_ = json.Unmarshal(r.Expected, &v)
			c.Change(v)
			b, _ := json.Marshal(v)
			if validateChillComplete(r.Input, b) == nil {
				t.Fatal("contradiction control admitted")
			}
		})
	}
}

const chillFrozenSourceIndex = `{
  "56 дней / 56 Days / S1E1-8 of 8 (Шена Стейн, Алетеа Джонс) [2026, США, триллер, драма, мелодрама, криминал, детектив, HEVC, HDR10+, WEB-DL 2160p] 3 x MVO (Дубляжная, LE-Production, Dragon Money Studio) + Original + Sub (Eng)": {
    "input": "56 дней / 56 Days / S1E1-8 of 8 (Шена Стейн, Алетеа Джонс) [2026, США, триллер, драма, мелодрама, криминал, детектив, HEVC, HDR10+, WEB-DL 2160p] 3 x MVO (Дубляжная, LE-Production, Dragon Money Studio) + Original + Sub (Eng)",
    "original_file": "fixture_accuracy_test.go",
    "input_line": 215,
    "original_index": 19,
    "assertion_start_line": 213,
    "assertion_end_line": 226,
    "original_assertion_block": "{\n\t\tname:  \"episode range hdr web dl\",\n\t\ttitle: \"56 дней / 56 Days / S1E1-8 of 8 (Шена Стейн, Алетеа Джонс) [2026, США, триллер, драма, мелодрама, криминал, детектив, HEVC, HDR10+, WEB-DL 2160p] 3 x MVO (Дубляжная, LE-Production, Dragon Money Studio) + Original + Sub (Eng)\",\n\t\twant: TorrentInfo{\n\t\t\tSeason:     1,\n\t\t\tEpisode:    1,\n\t\t\tEpisodeEnd: 8,\n\t\t\tResolution: \"2160p\",\n\t\t\tQuality:    \"WEB-DL\",\n\t\t\tCodec:      \"H265\",\n\t\t\tHDR:        \"HDR10+\",\n\t\t},\n\t\tfields: []string{\"season\", \"episode\", \"episode_end\", \"resolution\", \"quality\", \"codec\", \"hdr\"},\n\t}",
    "original_literal_fields": {
      "Codec": "H265",
      "Episode": 1,
      "EpisodeEnd": 8,
      "HDR": "HDR10+",
      "Quality": "WEB-DL",
      "Resolution": "2160p",
      "Season": 1
    },
    "original_asserted_fields": {
      "Codec": "H265",
      "Episode": 1,
      "EpisodeEnd": 8,
      "HDR": "HDR10+",
      "Quality": "WEB-DL",
      "Resolution": "2160p",
      "Season": 1
    },
    "selected_fields": [
      "season",
      "episode",
      "episode_end",
      "resolution",
      "quality",
      "codec",
      "hdr"
    ],
    "field_lines": {
      "Codec": 222,
      "Episode": 218,
      "EpisodeEnd": 219,
      "HDR": 223,
      "Quality": 221,
      "Resolution": 220,
      "Season": 217
    },
    "expected": {
      "episode": [
        1,
        2,
        3,
        4,
        5,
        6,
        7,
        8
      ],
      "screen_size": "2160p",
      "season": 1,
      "source": "Web",
      "video_codec": "H.265"
    }
  },
  "Childhood.039.s.End.2008.1080p.BluRay.x264-GRP": {
    "input": "Childhood.039.s.End.2008.1080p.BluRay.x264-GRP",
    "original_file": "parser_test.go",
    "input_line": 35,
    "original_index": 2,
    "assertion_start_line": 33,
    "assertion_end_line": 37,
    "original_assertion_block": "{\n\t\t\tname:     \"dot separated tracker marker\",\n\t\t\tfilename: \"Childhood.039.s.End.2008.1080p.BluRay.x264-GRP\",\n\t\t\twant:     \"Childhood's End\",\n\t\t}",
    "original_literal_fields": {
      "Title": "Childhood's End"
    },
    "original_asserted_fields": {
      "Title": "Childhood's End"
    },
    "selected_fields": null,
    "field_lines": {
      "Title": 36
    },
    "expected": {
      "title": "Childhood's End"
    }
  },
  "Critical Role S01E01 1080p WEB x264": {
    "input": "Critical Role S01E01 1080p WEB x264",
    "original_file": "parser_test.go",
    "input_line": 1700,
    "original_index": 1,
    "assertion_start_line": 1698,
    "assertion_end_line": 1707,
    "original_assertion_block": "{\n\t\t\tname:     \"critical title is not cr source\",\n\t\t\tfilename: \"Critical Role S01E01 1080p WEB x264\",\n\t\t\tcheck: func(t *testing.T, got TorrentInfo) {\n\t\t\t\tt.Helper()\n\t\t\t\tif got.Source != \"\" {\n\t\t\t\t\tt.Fatalf(\"Source = %q, want empty\", got.Source)\n\t\t\t\t}\n\t\t\t},\n\t\t}",
    "original_literal_fields": {
      "Source": ""
    },
    "original_asserted_fields": {
      "Source": ""
    },
    "selected_fields": null,
    "field_lines": {
      "Source": 1703
    },
    "expected": {}
  },
  "Crossing Jordan 2001 Complete Series Seasons 1 to 6 1080p WEB x264 [i_c]": {
    "input": "Crossing Jordan 2001 Complete Series Seasons 1 to 6 1080p WEB x264 [i_c]",
    "original_file": "parser_test.go",
    "input_line": 1009,
    "original_index": 90,
    "assertion_start_line": 1007,
    "assertion_end_line": 1019,
    "original_assertion_block": "{\n\t\t\tname:     \"complete series bracket group\",\n\t\t\tfilename: \"Crossing Jordan 2001 Complete Series Seasons 1 to 6 1080p WEB x264 [i_c]\",\n\t\t\twant: TorrentInfo{\n\t\t\t\tTitle:      \"Crossing Jordan\",\n\t\t\t\tYear:       2001,\n\t\t\t\tResolution: \"1080p\",\n\t\t\t\tQuality:    \"WEB\",\n\t\t\t\tCodec:      \"x264\",\n\t\t\t\tGroup:      \"i_c\",\n\t\t\t\tComplete:   true,\n\t\t\t},\n\t\t}",
    "original_literal_fields": {
      "Codec": "x264",
      "Complete": true,
      "Group": "i_c",
      "Quality": "WEB",
      "Resolution": "1080p",
      "Title": "Crossing Jordan",
      "Year": 2001
    },
    "original_asserted_fields": {
      "Codec": "x264",
      "Complete": true,
      "Group": "i_c",
      "Quality": "WEB",
      "Resolution": "1080p",
      "Title": "Crossing Jordan",
      "Year": 2001
    },
    "selected_fields": null,
    "field_lines": {
      "Codec": 1015,
      "Complete": 1017,
      "Group": 1016,
      "Quality": 1014,
      "Resolution": 1013,
      "Title": 1011,
      "Year": 1012
    },
    "expected": {
      "group": "i_c",
      "screen_size": "1080p",
      "source": "Raw",
      "title": "Crossing Jordan",
      "video_codec": "H.264",
      "year": 2001
    }
  },
  "Delivery Run 2024 BluRay 1080p DDP 5 1 x264 hallowed": {
    "input": "Delivery Run 2024 BluRay 1080p DDP 5 1 x264 hallowed",
    "original_file": "fixture_accuracy_test.go",
    "input_line": 18,
    "original_index": 142,
    "assertion_start_line": 16,
    "assertion_end_line": 29,
    "original_assertion_block": "{\n\t\tname:  \"movie bluray with audio and group\",\n\t\ttitle: \"Delivery Run 2024 BluRay 1080p DDP 5 1 x264 hallowed\",\n\t\twant: TorrentInfo{\n\t\t\tTitle:      \"Delivery Run\",\n\t\t\tYear:       2024,\n\t\t\tResolution: \"1080p\",\n\t\t\tQuality:    \"BluRay\",\n\t\t\tCodec:      \"x264\",\n\t\t\tAudio:      \"DDP5.1\",\n\t\t\tGroup:      \"hallowed\",\n\t\t},\n\t\tfields: []string{\"title\", \"year\", \"resolution\", \"quality\", \"codec\", \"audio\", \"group\"},\n\t}",
    "original_literal_fields": {
      "Audio": "DDP5.1",
      "Codec": "x264",
      "Group": "hallowed",
      "Quality": "BluRay",
      "Resolution": "1080p",
      "Title": "Delivery Run",
      "Year": 2024
    },
    "original_asserted_fields": {
      "Audio": "DDP5.1",
      "Codec": "x264",
      "Group": "hallowed",
      "Quality": "BluRay",
      "Resolution": "1080p",
      "Title": "Delivery Run",
      "Year": 2024
    },
    "selected_fields": [
      "title",
      "year",
      "resolution",
      "quality",
      "codec",
      "audio",
      "group"
    ],
    "field_lines": {
      "Audio": 25,
      "Codec": 24,
      "Group": 26,
      "Quality": 23,
      "Resolution": 22,
      "Title": 20,
      "Year": 21
    },
    "expected": {
      "group": "hallowed",
      "screen_size": "1080p",
      "source": "Blu-ray",
      "title": "Delivery Run",
      "video_codec": "H.264",
      "year": 2024
    }
  },
  "It 2017 1080p BluRay x264": {
    "input": "It 2017 1080p BluRay x264",
    "original_file": "parser_test.go",
    "input_line": 1690,
    "original_index": 0,
    "assertion_start_line": 1688,
    "assertion_end_line": 1697,
    "original_assertion_block": "{\n\t\t\tname:     \"it title is not source\",\n\t\t\tfilename: \"It 2017 1080p BluRay x264\",\n\t\t\tcheck: func(t *testing.T, got TorrentInfo) {\n\t\t\t\tt.Helper()\n\t\t\t\tif got.Source != \"\" {\n\t\t\t\t\tt.Fatalf(\"Source = %q, want empty\", got.Source)\n\t\t\t\t}\n\t\t\t},\n\t\t}",
    "original_literal_fields": {
      "Source": ""
    },
    "original_asserted_fields": {
      "Source": ""
    },
    "selected_fields": null,
    "field_lines": {
      "Source": 1693
    },
    "expected": {}
  },
  "Kaya chan Isnt Scary S01E11 1080p CR WEB DL DUAL AAC2 0 H 264 VARYG (Kaya chan wa Kowakunai  Dual Audio  Multi Subs)": {
    "input": "Kaya chan Isnt Scary S01E11 1080p CR WEB DL DUAL AAC2 0 H 264 VARYG (Kaya chan wa Kowakunai  Dual Audio  Multi Subs)",
    "original_file": "fixture_accuracy_test.go",
    "input_line": 48,
    "original_index": 144,
    "assertion_start_line": 46,
    "assertion_end_line": 61,
    "original_assertion_block": "{\n\t\tname:  \"cr web dl source\",\n\t\ttitle: \"Kaya chan Isnt Scary S01E11 1080p CR WEB DL DUAL AAC2 0 H 264 VARYG (Kaya chan wa Kowakunai  Dual Audio  Multi Subs)\",\n\t\twant: TorrentInfo{\n\t\t\tTitle:      \"Kaya chan Isnt Scary\",\n\t\t\tSeason:     1,\n\t\t\tEpisode:    11,\n\t\t\tResolution: \"1080p\",\n\t\t\tQuality:    \"WEB-DL\",\n\t\t\tCodec:      \"H264\",\n\t\t\tAudio:      \"AAC2.0\",\n\t\t\tSource:     \"CR\",\n\t\t\tEdition:    \"Dual Audio Multi Subs\",\n\t\t},\n\t\tfields: []string{\"title\", \"season\", \"episode\", \"resolution\", \"quality\", \"codec\", \"audio\", \"source\", \"edition\"},\n\t}",
    "original_literal_fields": {
      "Audio": "AAC2.0",
      "Codec": "H264",
      "Edition": "Dual Audio Multi Subs",
      "Episode": 11,
      "Quality": "WEB-DL",
      "Resolution": "1080p",
      "Season": 1,
      "Source": "CR",
      "Title": "Kaya chan Isnt Scary"
    },
    "original_asserted_fields": {
      "Audio": "AAC2.0",
      "Codec": "H264",
      "Edition": "Dual Audio Multi Subs",
      "Episode": 11,
      "Quality": "WEB-DL",
      "Resolution": "1080p",
      "Season": 1,
      "Source": "CR",
      "Title": "Kaya chan Isnt Scary"
    },
    "selected_fields": [
      "title",
      "season",
      "episode",
      "resolution",
      "quality",
      "codec",
      "audio",
      "source",
      "edition"
    ],
    "field_lines": {
      "Audio": 56,
      "Codec": 55,
      "Edition": 58,
      "Episode": 52,
      "Quality": 54,
      "Resolution": 53,
      "Season": 51,
      "Source": 57,
      "Title": 50
    },
    "expected": {
      "episode": 11,
      "screen_size": "1080p",
      "season": 1,
      "source": "Web",
      "title": "Kaya chan Isnt Scary",
      "video_codec": "H.264"
    }
  },
  "Kaya-chan.Isnt.Scary.S01E11.1080p.CR.WEB-DL.DUAL.AAC2.0.H.264-VARYG": {
    "input": "Kaya-chan.Isnt.Scary.S01E11.1080p.CR.WEB-DL.DUAL.AAC2.0.H.264-VARYG",
    "original_file": "parser_test.go",
    "input_line": 951,
    "original_index": 86,
    "assertion_start_line": 949,
    "assertion_end_line": 964,
    "original_assertion_block": "{\n\t\t\tname:     \"cr anime web dl\",\n\t\t\tfilename: \"Kaya-chan.Isnt.Scary.S01E11.1080p.CR.WEB-DL.DUAL.AAC2.0.H.264-VARYG\",\n\t\t\twant: TorrentInfo{\n\t\t\t\tTitle:      \"Kaya-chan Isnt Scary\",\n\t\t\t\tSeason:     1,\n\t\t\t\tEpisode:    11,\n\t\t\t\tResolution: \"1080p\",\n\t\t\t\tQuality:    \"WEB-DL\",\n\t\t\t\tCodec:      \"H264\",\n\t\t\t\tAudio:      \"AAC2.0\",\n\t\t\t\tSource:     \"CR\",\n\t\t\t\tGroup:      \"VARYG\",\n\t\t\t\tEdition:    \"Dual Audio\",\n\t\t\t},\n\t\t}",
    "original_literal_fields": {
      "Audio": "AAC2.0",
      "Codec": "H264",
      "Edition": "Dual Audio",
      "Episode": 11,
      "Group": "VARYG",
      "Quality": "WEB-DL",
      "Resolution": "1080p",
      "Season": 1,
      "Source": "CR",
      "Title": "Kaya-chan Isnt Scary"
    },
    "original_asserted_fields": {
      "Audio": "AAC2.0",
      "Codec": "H264",
      "Edition": "Dual Audio",
      "Episode": 11,
      "Group": "VARYG",
      "Quality": "WEB-DL",
      "Resolution": "1080p",
      "Season": 1,
      "Source": "CR",
      "Title": "Kaya-chan Isnt Scary"
    },
    "selected_fields": null,
    "field_lines": {
      "Audio": 959,
      "Codec": 958,
      "Edition": 962,
      "Episode": 955,
      "Group": 961,
      "Quality": 957,
      "Resolution": 956,
      "Season": 954,
      "Source": 960,
      "Title": 953
    },
    "expected": {
      "episode": 11,
      "group": "VARYG",
      "screen_size": "1080p",
      "season": 1,
      "source": "Web",
      "title": "Kaya-chan Isnt Scary",
      "video_codec": "H.264"
    }
  },
  "Monarch Legacy of Monsters S02E04 2160p HDR10Plus DV WEBRip 6CH x265 HEVC-P": {
    "input": "Monarch Legacy of Monsters S02E04 2160p HDR10Plus DV WEBRip 6CH x265 HEVC-P",
    "original_file": "parser_test.go",
    "input_line": 967,
    "original_index": 87,
    "assertion_start_line": 965,
    "assertion_end_line": 979,
    "original_assertion_block": "{\n\t\t\tname:     \"multi hdr hevc\",\n\t\t\tfilename: \"Monarch Legacy of Monsters S02E04 2160p HDR10Plus DV WEBRip 6CH x265 HEVC-P\",\n\t\t\twant: TorrentInfo{\n\t\t\t\tTitle:      \"Monarch Legacy of Monsters\",\n\t\t\t\tSeason:     2,\n\t\t\t\tEpisode:    4,\n\t\t\t\tResolution: \"2160p\",\n\t\t\t\tQuality:    \"WEBRip\",\n\t\t\t\tCodec:      \"H265\",\n\t\t\t\tHDR:        \"HDR10+ DV\",\n\t\t\t\tAudio:      \"5.1\",\n\t\t\t\tGroup:      \"P\",\n\t\t\t},\n\t\t}",
    "original_literal_fields": {
      "Audio": "5.1",
      "Codec": "H265",
      "Episode": 4,
      "Group": "P",
      "HDR": "HDR10+ DV",
      "Quality": "WEBRip",
      "Resolution": "2160p",
      "Season": 2,
      "Title": "Monarch Legacy of Monsters"
    },
    "original_asserted_fields": {
      "Audio": "5.1",
      "Codec": "H265",
      "Episode": 4,
      "Group": "P",
      "HDR": "HDR10+ DV",
      "Quality": "WEBRip",
      "Resolution": "2160p",
      "Season": 2,
      "Title": "Monarch Legacy of Monsters"
    },
    "selected_fields": null,
    "field_lines": {
      "Audio": 976,
      "Codec": 974,
      "Episode": 971,
      "Group": 977,
      "HDR": 975,
      "Quality": 973,
      "Resolution": 972,
      "Season": 970,
      "Title": 969
    },
    "expected": {
      "episode": 4,
      "group": "P",
      "screen_size": "2160p",
      "season": 2,
      "source": "Web",
      "title": "Monarch Legacy of Monsters",
      "video_codec": "H.265"
    }
  },
  "Mr.Hollands.Opus.1995.1080p.BluRay.x264-GRP": {
    "input": "Mr.Hollands.Opus.1995.1080p.BluRay.x264-GRP",
    "original_file": "parser_test.go",
    "input_line": 1290,
    "original_index": 109,
    "assertion_start_line": 1288,
    "assertion_end_line": 1299,
    "original_assertion_block": "{\n\t\t\tname:     \"bare opus stays in title\",\n\t\t\tfilename: \"Mr.Hollands.Opus.1995.1080p.BluRay.x264-GRP\",\n\t\t\twant: TorrentInfo{\n\t\t\t\tTitle:      \"Mr Hollands Opus\",\n\t\t\t\tYear:       1995,\n\t\t\t\tResolution: \"1080p\",\n\t\t\t\tQuality:    \"BluRay\",\n\t\t\t\tCodec:      \"x264\",\n\t\t\t\tGroup:      \"GRP\",\n\t\t\t},\n\t\t}",
    "original_literal_fields": {
      "Codec": "x264",
      "Group": "GRP",
      "Quality": "BluRay",
      "Resolution": "1080p",
      "Title": "Mr Hollands Opus",
      "Year": 1995
    },
    "original_asserted_fields": {
      "Codec": "x264",
      "Group": "GRP",
      "Quality": "BluRay",
      "Resolution": "1080p",
      "Title": "Mr Hollands Opus",
      "Year": 1995
    },
    "selected_fields": null,
    "field_lines": {
      "Codec": 1296,
      "Group": 1297,
      "Quality": 1295,
      "Resolution": 1294,
      "Title": 1292,
      "Year": 1293
    },
    "expected": {
      "group": "GRP",
      "screen_size": "1080p",
      "source": "Blu-ray",
      "title": "Mr Hollands Opus",
      "video_codec": "H.264",
      "year": 1995
    }
  },
  "Nightmare Alley 2021 2160p UHD Blu ray Remux DV HDR HEVC TrueHD Atmos 7 1 CiNEPHiLES": {
    "input": "Nightmare Alley 2021 2160p UHD Blu ray Remux DV HDR HEVC TrueHD Atmos 7 1 CiNEPHiLES",
    "original_file": "parser_test.go",
    "input_line": 995,
    "original_index": 89,
    "assertion_start_line": 993,
    "assertion_end_line": 1006,
    "original_assertion_block": "{\n\t\t\tname:     \"uhd remux dv hdr\",\n\t\t\tfilename: \"Nightmare Alley 2021 2160p UHD Blu ray Remux DV HDR HEVC TrueHD Atmos 7 1 CiNEPHiLES\",\n\t\t\twant: TorrentInfo{\n\t\t\t\tTitle:      \"Nightmare Alley\",\n\t\t\t\tYear:       2021,\n\t\t\t\tResolution: \"2160p\",\n\t\t\t\tQuality:    \"REMUX\",\n\t\t\t\tCodec:      \"H265\",\n\t\t\t\tHDR:        \"DV HDR\",\n\t\t\t\tAudio:      \"TrueHD Atmos 7.1\",\n\t\t\t\tGroup:      \"CiNEPHiLES\",\n\t\t\t},\n\t\t}",
    "original_literal_fields": {
      "Audio": "TrueHD Atmos 7.1",
      "Codec": "H265",
      "Group": "CiNEPHiLES",
      "HDR": "DV HDR",
      "Quality": "REMUX",
      "Resolution": "2160p",
      "Title": "Nightmare Alley",
      "Year": 2021
    },
    "original_asserted_fields": {
      "Audio": "TrueHD Atmos 7.1",
      "Codec": "H265",
      "Group": "CiNEPHiLES",
      "HDR": "DV HDR",
      "Quality": "REMUX",
      "Resolution": "2160p",
      "Title": "Nightmare Alley",
      "Year": 2021
    },
    "selected_fields": null,
    "field_lines": {
      "Audio": 1003,
      "Codec": 1001,
      "Group": 1004,
      "HDR": 1002,
      "Quality": 1000,
      "Resolution": 999,
      "Title": 997,
      "Year": 998
    },
    "expected": {
      "group": "CiNEPHiLES",
      "screen_size": "2160p",
      "source": "Blu-ray",
      "title": "Nightmare Alley",
      "video_codec": "H.265",
      "year": 2021
    }
  },
  "Nightmare Alley 2021 BW Directors Cut 2160p UHD Blu ray Remux DV HDR HEVC DTS HD MA 5 1 CiNEPHiLES": {
    "input": "Nightmare Alley 2021 BW Directors Cut 2160p UHD Blu ray Remux DV HDR HEVC DTS HD MA 5 1 CiNEPHiLES",
    "original_file": "fixture_accuracy_test.go",
    "input_line": 64,
    "original_index": 145,
    "assertion_start_line": 62,
    "assertion_end_line": 77,
    "original_assertion_block": "{\n\t\tname:  \"uhd remux edition\",\n\t\ttitle: \"Nightmare Alley 2021 BW Directors Cut 2160p UHD Blu ray Remux DV HDR HEVC DTS HD MA 5 1 CiNEPHiLES\",\n\t\twant: TorrentInfo{\n\t\t\tTitle:      \"Nightmare Alley\",\n\t\t\tYear:       2021,\n\t\t\tResolution: \"2160p\",\n\t\t\tQuality:    \"REMUX\",\n\t\t\tCodec:      \"H265\",\n\t\t\tHDR:        \"DV HDR\",\n\t\t\tAudio:      \"DTS-HD MA 5.1\",\n\t\t\tGroup:      \"CiNEPHiLES\",\n\t\t\tEdition:    \"Black and White Director's Cut\",\n\t\t},\n\t\tfields: []string{\"title\", \"year\", \"resolution\", \"quality\", \"codec\", \"hdr\", \"audio\", \"group\", \"edition\"},\n\t}",
    "original_literal_fields": {
      "Audio": "DTS-HD MA 5.1",
      "Codec": "H265",
      "Edition": "Black and White Director's Cut",
      "Group": "CiNEPHiLES",
      "HDR": "DV HDR",
      "Quality": "REMUX",
      "Resolution": "2160p",
      "Title": "Nightmare Alley",
      "Year": 2021
    },
    "original_asserted_fields": {
      "Audio": "DTS-HD MA 5.1",
      "Codec": "H265",
      "Edition": "Black and White Director's Cut",
      "Group": "CiNEPHiLES",
      "HDR": "DV HDR",
      "Quality": "REMUX",
      "Resolution": "2160p",
      "Title": "Nightmare Alley",
      "Year": 2021
    },
    "selected_fields": [
      "title",
      "year",
      "resolution",
      "quality",
      "codec",
      "hdr",
      "audio",
      "group",
      "edition"
    ],
    "field_lines": {
      "Audio": 72,
      "Codec": 70,
      "Edition": 74,
      "Group": 73,
      "HDR": 71,
      "Quality": 69,
      "Resolution": 68,
      "Title": 66,
      "Year": 67
    },
    "expected": {
      "group": "CiNEPHiLES",
      "screen_size": "2160p",
      "source": "Blu-ray",
      "title": "Nightmare Alley",
      "video_codec": "H.265",
      "year": 2021
    }
  },
  "On the Brain 2018 HDRip XviD AC3-EVO": {
    "input": "On the Brain 2018 HDRip XviD AC3-EVO",
    "original_file": "fixture_accuracy_test.go",
    "input_line": 80,
    "original_index": 146,
    "assertion_start_line": 78,
    "assertion_end_line": 90,
    "original_assertion_block": "{\n\t\tname:  \"hdrip xvid ac3\",\n\t\ttitle: \"On the Brain 2018 HDRip XviD AC3-EVO\",\n\t\twant: TorrentInfo{\n\t\t\tTitle:   \"On the Brain\",\n\t\t\tYear:    2018,\n\t\t\tQuality: \"HDRip\",\n\t\t\tCodec:   \"XViD\",\n\t\t\tAudio:   \"AC3\",\n\t\t\tGroup:   \"EVO\",\n\t\t},\n\t\tfields: []string{\"title\", \"year\", \"quality\", \"codec\", \"audio\", \"group\"},\n\t}",
    "original_literal_fields": {
      "Audio": "AC3",
      "Codec": "XViD",
      "Group": "EVO",
      "Quality": "HDRip",
      "Title": "On the Brain",
      "Year": 2018
    },
    "original_asserted_fields": {
      "Audio": "AC3",
      "Codec": "XViD",
      "Group": "EVO",
      "Quality": "HDRip",
      "Title": "On the Brain",
      "Year": 2018
    },
    "selected_fields": [
      "title",
      "year",
      "quality",
      "codec",
      "audio",
      "group"
    ],
    "field_lines": {
      "Audio": 86,
      "Codec": 85,
      "Group": 87,
      "Quality": 84,
      "Title": 82,
      "Year": 83
    },
    "expected": {
      "group": "EVO",
      "source": "Raw",
      "title": "On the Brain",
      "video_codec": "Xvid",
      "year": 2018
    }
  },
  "Peaky Blinders S03E01 Episode 1 1080p NF WEB-DL DDP5 1 H 264-Kitsune": {
    "input": "Peaky Blinders S03E01 Episode 1 1080p NF WEB-DL DDP5 1 H 264-Kitsune",
    "original_file": "fixture_accuracy_test.go",
    "input_line": 141,
    "original_index": 150,
    "assertion_start_line": 139,
    "assertion_end_line": 154,
    "original_assertion_block": "{\n\t\tname:  \"nf web dl\",\n\t\ttitle: \"Peaky Blinders S03E01 Episode 1 1080p NF WEB-DL DDP5 1 H 264-Kitsune\",\n\t\twant: TorrentInfo{\n\t\t\tTitle:      \"Peaky Blinders\",\n\t\t\tSeason:     3,\n\t\t\tEpisode:    1,\n\t\t\tResolution: \"1080p\",\n\t\t\tQuality:    \"WEB-DL\",\n\t\t\tCodec:      \"H264\",\n\t\t\tAudio:      \"DDP5.1\",\n\t\t\tSource:     \"NF\",\n\t\t\tGroup:      \"Kitsune\",\n\t\t},\n\t\tfields: []string{\"title\", \"season\", \"episode\", \"resolution\", \"quality\", \"codec\", \"audio\", \"source\", \"group\"},\n\t}",
    "original_literal_fields": {
      "Audio": "DDP5.1",
      "Codec": "H264",
      "Episode": 1,
      "Group": "Kitsune",
      "Quality": "WEB-DL",
      "Resolution": "1080p",
      "Season": 3,
      "Source": "NF",
      "Title": "Peaky Blinders"
    },
    "original_asserted_fields": {
      "Audio": "DDP5.1",
      "Codec": "H264",
      "Episode": 1,
      "Group": "Kitsune",
      "Quality": "WEB-DL",
      "Resolution": "1080p",
      "Season": 3,
      "Source": "NF",
      "Title": "Peaky Blinders"
    },
    "selected_fields": [
      "title",
      "season",
      "episode",
      "resolution",
      "quality",
      "codec",
      "audio",
      "source",
      "group"
    ],
    "field_lines": {
      "Audio": 149,
      "Codec": 148,
      "Episode": 145,
      "Group": 151,
      "Quality": 147,
      "Resolution": 146,
      "Season": 144,
      "Source": 150,
      "Title": 143
    },
    "expected": {
      "episode": 1,
      "group": "Kitsune",
      "screen_size": "1080p",
      "season": 3,
      "source": "Web",
      "title": "Peaky Blinders",
      "video_codec": "H.264"
    }
  },
  "Pyramid.Builders.New.Clues.2019.720p.x265.aac.subs.mkv": {
    "input": "Pyramid.Builders.New.Clues.2019.720p.x265.aac.subs.mkv",
    "original_file": "fixture_accuracy_test.go",
    "input_line": 299,
    "original_index": 156,
    "assertion_start_line": 297,
    "assertion_end_line": 309,
    "original_assertion_block": "{\n\t\tname:  \"container and lowercase audio\",\n\t\ttitle: \"Pyramid.Builders.New.Clues.2019.720p.x265.aac.subs.mkv\",\n\t\twant: TorrentInfo{\n\t\t\tTitle:      \"Pyramid Builders New Clues\",\n\t\t\tYear:       2019,\n\t\t\tResolution: \"720p\",\n\t\t\tCodec:      \"x265\",\n\t\t\tAudio:      \"AAC\",\n\t\t\tContainer:  \"mkv\",\n\t\t},\n\t\tfields: []string{\"title\", \"year\", \"resolution\", \"codec\", \"audio\", \"container\"},\n\t}",
    "original_literal_fields": {
      "Audio": "AAC",
      "Codec": "x265",
      "Container": "mkv",
      "Resolution": "720p",
      "Title": "Pyramid Builders New Clues",
      "Year": 2019
    },
    "original_asserted_fields": {
      "Audio": "AAC",
      "Codec": "x265",
      "Container": "mkv",
      "Resolution": "720p",
      "Title": "Pyramid Builders New Clues",
      "Year": 2019
    },
    "selected_fields": [
      "title",
      "year",
      "resolution",
      "codec",
      "audio",
      "container"
    ],
    "field_lines": {
      "Audio": 305,
      "Codec": 304,
      "Container": 306,
      "Resolution": 303,
      "Title": 301,
      "Year": 302
    },
    "expected": {
      "screen_size": "720p",
      "title": "Pyramid Builders New Clues",
      "video_codec": "H.265",
      "year": 2019
    }
  },
  "Roman Empire By Train With Alice Roberts S01E06 720p HDTV H264 JFF EZTV": {
    "input": "Roman Empire By Train With Alice Roberts S01E06 720p HDTV H264 JFF EZTV",
    "original_file": "fixture_accuracy_test.go",
    "input_line": 287,
    "original_index": 21,
    "assertion_start_line": 285,
    "assertion_end_line": 296,
    "original_assertion_block": "{\n\t\tname:  \"space separated trailing group\",\n\t\ttitle: \"Roman Empire By Train With Alice Roberts S01E06 720p HDTV H264 JFF EZTV\",\n\t\twant: TorrentInfo{\n\t\t\tSeason:     1,\n\t\t\tEpisode:    6,\n\t\t\tResolution: \"720p\",\n\t\t\tQuality:    \"HDTV\",\n\t\t\tCodec:      \"H264\",\n\t\t},\n\t\tfields: []string{\"season\", \"episode\", \"resolution\", \"quality\", \"codec\", \"group\"},\n\t}",
    "original_literal_fields": {
      "Codec": "H264",
      "Episode": 6,
      "Quality": "HDTV",
      "Resolution": "720p",
      "Season": 1
    },
    "original_asserted_fields": {
      "Codec": "H264",
      "Episode": 6,
      "Group": "",
      "Quality": "HDTV",
      "Resolution": "720p",
      "Season": 1
    },
    "selected_fields": [
      "season",
      "episode",
      "resolution",
      "quality",
      "codec",
      "group"
    ],
    "field_lines": {
      "Codec": 293,
      "Episode": 290,
      "Quality": 292,
      "Resolution": 291,
      "Season": 289
    },
    "expected": {
      "episode": 6,
      "group": "",
      "screen_size": "720p",
      "season": 1,
      "source": "HDTV",
      "video_codec": "H.264"
    }
  },
  "Roman Empire By Train With Alice Roberts S01E06 720p HDTV H264-JFF": {
    "input": "Roman Empire By Train With Alice Roberts S01E06 720p HDTV H264-JFF",
    "original_file": "fixture_accuracy_test.go",
    "input_line": 127,
    "original_index": 149,
    "assertion_start_line": 125,
    "assertion_end_line": 138,
    "original_assertion_block": "{\n\t\tname:  \"hdtv h264 group\",\n\t\ttitle: \"Roman Empire By Train With Alice Roberts S01E06 720p HDTV H264-JFF\",\n\t\twant: TorrentInfo{\n\t\t\tTitle:      \"Roman Empire By Train With Alice Roberts\",\n\t\t\tSeason:     1,\n\t\t\tEpisode:    6,\n\t\t\tResolution: \"720p\",\n\t\t\tQuality:    \"HDTV\",\n\t\t\tCodec:      \"H264\",\n\t\t\tGroup:      \"JFF\",\n\t\t},\n\t\tfields: []string{\"title\", \"season\", \"episode\", \"resolution\", \"quality\", \"codec\", \"group\"},\n\t}",
    "original_literal_fields": {
      "Codec": "H264",
      "Episode": 6,
      "Group": "JFF",
      "Quality": "HDTV",
      "Resolution": "720p",
      "Season": 1,
      "Title": "Roman Empire By Train With Alice Roberts"
    },
    "original_asserted_fields": {
      "Codec": "H264",
      "Episode": 6,
      "Group": "JFF",
      "Quality": "HDTV",
      "Resolution": "720p",
      "Season": 1,
      "Title": "Roman Empire By Train With Alice Roberts"
    },
    "selected_fields": [
      "title",
      "season",
      "episode",
      "resolution",
      "quality",
      "codec",
      "group"
    ],
    "field_lines": {
      "Codec": 134,
      "Episode": 131,
      "Group": 135,
      "Quality": 133,
      "Resolution": 132,
      "Season": 130,
      "Title": 129
    },
    "expected": {
      "episode": 6,
      "group": "JFF",
      "screen_size": "720p",
      "season": 1,
      "source": "HDTV",
      "title": "Roman Empire By Train With Alice Roberts",
      "video_codec": "H.264"
    }
  },
  "SD.Gundam.World.Heroes.S01E01.1080p.WEB-DL.H265": {
    "input": "SD.Gundam.World.Heroes.S01E01.1080p.WEB-DL.H265",
    "original_file": "parser_test.go",
    "input_line": 669,
    "original_index": 60,
    "assertion_start_line": 668,
    "assertion_end_line": 678,
    "original_assertion_block": "{\n\t\t\tfilename: \"SD.Gundam.World.Heroes.S01E01.1080p.WEB-DL.H265\",\n\t\t\twant: TorrentInfo{\n\t\t\t\tTitle:      \"SD Gundam World Heroes\",\n\t\t\t\tSeason:     1,\n\t\t\t\tEpisode:    1,\n\t\t\t\tResolution: \"1080p\",\n\t\t\t\tQuality:    \"WEB-DL\",\n\t\t\t\tCodec:      \"H265\",\n\t\t\t},\n\t\t}",
    "original_literal_fields": {
      "Codec": "H265",
      "Episode": 1,
      "Quality": "WEB-DL",
      "Resolution": "1080p",
      "Season": 1,
      "Title": "SD Gundam World Heroes"
    },
    "original_asserted_fields": {
      "Codec": "H265",
      "Episode": 1,
      "Quality": "WEB-DL",
      "Resolution": "1080p",
      "Season": 1,
      "Title": "SD Gundam World Heroes"
    },
    "selected_fields": null,
    "field_lines": {
      "Codec": 676,
      "Episode": 673,
      "Quality": 675,
      "Resolution": 674,
      "Season": 672,
      "Title": 671
    },
    "expected": {
      "episode": 1,
      "screen_size": "1080p",
      "season": 1,
      "source": "Web",
      "title": "SD Gundam World Heroes",
      "video_codec": "H.265"
    }
  },
  "SD.Gundam.World.Heroes.S01E01.WEB-DL.H265": {
    "input": "SD.Gundam.World.Heroes.S01E01.WEB-DL.H265",
    "original_file": "parser_test.go",
    "input_line": 680,
    "original_index": 61,
    "assertion_start_line": 679,
    "assertion_end_line": 688,
    "original_assertion_block": "{\n\t\t\tfilename: \"SD.Gundam.World.Heroes.S01E01.WEB-DL.H265\",\n\t\t\twant: TorrentInfo{\n\t\t\t\tTitle:   \"SD Gundam World Heroes\",\n\t\t\t\tSeason:  1,\n\t\t\t\tEpisode: 1,\n\t\t\t\tQuality: \"WEB-DL\",\n\t\t\t\tCodec:   \"H265\",\n\t\t\t},\n\t\t}",
    "original_literal_fields": {
      "Codec": "H265",
      "Episode": 1,
      "Quality": "WEB-DL",
      "Season": 1,
      "Title": "SD Gundam World Heroes"
    },
    "original_asserted_fields": {
      "Codec": "H265",
      "Episode": 1,
      "Quality": "WEB-DL",
      "Season": 1,
      "Title": "SD Gundam World Heroes"
    },
    "selected_fields": null,
    "field_lines": {
      "Codec": 686,
      "Episode": 684,
      "Quality": 685,
      "Season": 683,
      "Title": 682
    },
    "expected": {
      "episode": 1,
      "season": 1,
      "source": "Web",
      "title": "SD Gundam World Heroes",
      "video_codec": "H.265"
    }
  },
  "Silkwood 1983 1080p BluRay x265 10bit EAC3 2 0 r00t QxR": {
    "input": "Silkwood 1983 1080p BluRay x265 10bit EAC3 2 0 r00t QxR",
    "original_file": "fixture_accuracy_test.go",
    "input_line": 200,
    "original_index": 152,
    "assertion_start_line": 198,
    "assertion_end_line": 212,
    "original_assertion_block": "{\n\t\tname:  \"bluray x265 bit depth and advanced group\",\n\t\ttitle: \"Silkwood 1983 1080p BluRay x265 10bit EAC3 2 0 r00t QxR\",\n\t\twant: TorrentInfo{\n\t\t\tTitle:      \"Silkwood\",\n\t\t\tYear:       1983,\n\t\t\tResolution: \"1080p\",\n\t\t\tQuality:    \"BluRay\",\n\t\t\tCodec:      \"x265\",\n\t\t\tAudio:      \"EAC3 2.0\",\n\t\t\tGroup:      \"QxR\",\n\t\t\tBitDepth:   \"10-bit\",\n\t\t},\n\t\tfields: []string{\"title\", \"year\", \"resolution\", \"quality\", \"codec\", \"audio\", \"group\", \"bit_depth\"},\n\t}",
    "original_literal_fields": {
      "Audio": "EAC3 2.0",
      "BitDepth": "10-bit",
      "Codec": "x265",
      "Group": "QxR",
      "Quality": "BluRay",
      "Resolution": "1080p",
      "Title": "Silkwood",
      "Year": 1983
    },
    "original_asserted_fields": {
      "Audio": "EAC3 2.0",
      "BitDepth": "10-bit",
      "Codec": "x265",
      "Group": "QxR",
      "Quality": "BluRay",
      "Resolution": "1080p",
      "Title": "Silkwood",
      "Year": 1983
    },
    "selected_fields": [
      "title",
      "year",
      "resolution",
      "quality",
      "codec",
      "audio",
      "group",
      "bit_depth"
    ],
    "field_lines": {
      "Audio": 207,
      "BitDepth": 209,
      "Codec": 206,
      "Group": 208,
      "Quality": 205,
      "Resolution": 204,
      "Title": 202,
      "Year": 203
    },
    "expected": {
      "group": "QxR",
      "screen_size": "1080p",
      "source": "Blu-ray",
      "title": "Silkwood",
      "video_codec": "H.265",
      "year": 1983
    }
  },
  "Tar 2022 HDRip XviD AC3 EVO[TGx]": {
    "input": "Tar 2022 HDRip XviD AC3 EVO[TGx]",
    "original_file": "fixture_accuracy_test.go",
    "input_line": 274,
    "original_index": 155,
    "assertion_start_line": 272,
    "assertion_end_line": 284,
    "original_assertion_block": "{\n\t\tname:  \"bracket group after legacy metadata\",\n\t\ttitle: \"Tar 2022 HDRip XviD AC3 EVO[TGx]\",\n\t\twant: TorrentInfo{\n\t\t\tTitle:   \"Tar\",\n\t\t\tYear:    2022,\n\t\t\tQuality: \"HDRip\",\n\t\t\tCodec:   \"XViD\",\n\t\t\tAudio:   \"AC3\",\n\t\t\tGroup:   \"TGx\",\n\t\t},\n\t\tfields: []string{\"title\", \"year\", \"quality\", \"codec\", \"audio\", \"group\"},\n\t}",
    "original_literal_fields": {
      "Audio": "AC3",
      "Codec": "XViD",
      "Group": "TGx",
      "Quality": "HDRip",
      "Title": "Tar",
      "Year": 2022
    },
    "original_asserted_fields": {
      "Audio": "AC3",
      "Codec": "XViD",
      "Group": "TGx",
      "Quality": "HDRip",
      "Title": "Tar",
      "Year": 2022
    },
    "selected_fields": [
      "title",
      "year",
      "quality",
      "codec",
      "audio",
      "group"
    ],
    "field_lines": {
      "Audio": 280,
      "Codec": 279,
      "Group": 281,
      "Quality": 278,
      "Title": 276,
      "Year": 277
    },
    "expected": {
      "group": "TGx",
      "source": "Raw",
      "title": "Tar",
      "video_codec": "Xvid",
      "year": 2022
    }
  },
  "Widow s Bay (2026) s01e04 [Mkv  1080p H264  MultiLang Aac 2 0  MultiSubs]": {
    "input": "Widow s Bay (2026) s01e04 [Mkv  1080p H264  MultiLang Aac 2 0  MultiSubs]",
    "original_file": "fixture_accuracy_test.go",
    "input_line": 178,
    "original_index": 16,
    "assertion_start_line": 176,
    "assertion_end_line": 181,
    "original_assertion_block": "{\n\t\tname:   \"metadata bracket multisubs collapsed punctuation\",\n\t\ttitle:  \"Widow s Bay (2026) s01e04 [Mkv  1080p H264  MultiLang Aac 2 0  MultiSubs]\",\n\t\twant:   TorrentInfo{},\n\t\tfields: []string{\"group\"},\n\t}",
    "original_literal_fields": {},
    "original_asserted_fields": {
      "Group": ""
    },
    "selected_fields": [
      "group"
    ],
    "field_lines": {},
    "expected": {
      "group": ""
    }
  },
  "Widow's Bay (2026) s01e04 [Mkv - 1080p H264 - MultiLang Aac 2 0 - MultiSubs]": {
    "input": "Widow's Bay (2026) s01e04 [Mkv - 1080p H264 - MultiLang Aac 2 0 - MultiSubs]",
    "original_file": "fixture_accuracy_test.go",
    "input_line": 172,
    "original_index": 15,
    "assertion_start_line": 170,
    "assertion_end_line": 175,
    "original_assertion_block": "{\n\t\tname:   \"metadata bracket multisubs apostrophe\",\n\t\ttitle:  \"Widow's Bay (2026) s01e04 [Mkv - 1080p H264 - MultiLang Aac 2 0 - MultiSubs]\",\n\t\twant:   TorrentInfo{},\n\t\tfields: []string{\"group\"},\n\t}",
    "original_literal_fields": {},
    "original_asserted_fields": {
      "Group": ""
    },
    "selected_fields": [
      "group"
    ],
    "field_lines": {},
    "expected": {
      "group": ""
    }
  },
  "WidowS Bay S01e04 [1080p Ita Eng Spa HEVC10 SubS] byMe7alh [MIRCrew]": {
    "input": "WidowS Bay S01e04 [1080p Ita Eng Spa HEVC10 SubS] byMe7alh [MIRCrew]",
    "original_file": "fixture_accuracy_test.go",
    "input_line": 184,
    "original_index": 17,
    "assertion_start_line": 182,
    "assertion_end_line": 189,
    "original_assertion_block": "{\n\t\tname:  \"real bracket group after metadata\",\n\t\ttitle: \"WidowS Bay S01e04 [1080p Ita Eng Spa HEVC10 SubS] byMe7alh [MIRCrew]\",\n\t\twant: TorrentInfo{\n\t\t\tGroup: \"MIRCrew\",\n\t\t},\n\t\tfields: []string{\"group\"},\n\t}",
    "original_literal_fields": {
      "Group": "MIRCrew"
    },
    "original_asserted_fields": {
      "Group": "MIRCrew"
    },
    "selected_fields": [
      "group"
    ],
    "field_lines": {
      "Group": 186
    },
    "expected": {
      "group": "MIRCrew"
    }
  },
  "Widows Bay S01E04 Beach Reads 2160p ATVP WEB-DL DD 5 1 Atmos H 265-playWEB": {
    "input": "Widows Bay S01E04 Beach Reads 2160p ATVP WEB-DL DD 5 1 Atmos H 265-playWEB",
    "original_file": "fixture_accuracy_test.go",
    "input_line": 192,
    "original_index": 18,
    "assertion_start_line": 190,
    "assertion_end_line": 197,
    "original_assertion_block": "{\n\t\tname:  \"real dash group after metadata\",\n\t\ttitle: \"Widows Bay S01E04 Beach Reads 2160p ATVP WEB-DL DD 5 1 Atmos H 265-playWEB\",\n\t\twant: TorrentInfo{\n\t\t\tGroup: \"playWEB\",\n\t\t},\n\t\tfields: []string{\"group\"},\n\t}",
    "original_literal_fields": {
      "Group": "playWEB"
    },
    "original_asserted_fields": {
      "Group": "playWEB"
    },
    "selected_fields": [
      "group"
    ],
    "field_lines": {
      "Group": 194
    },
    "expected": {
      "group": "playWEB"
    }
  },
  "You and I Are Polar Opposites S01E10 Class Trip Part 1 1080p CR WEB-DL DUAL AAC2.0 H 264-VARYG (Seihantai na Kimi to Boku, Dual-Audio, Multi-Subs)": {
    "input": "You and I Are Polar Opposites S01E10 Class Trip Part 1 1080p CR WEB-DL DUAL AAC2.0 H 264-VARYG (Seihantai na Kimi to Boku, Dual-Audio, Multi-Subs)",
    "original_file": "fixture_accuracy_test.go",
    "input_line": 93,
    "original_index": 147,
    "assertion_start_line": 91,
    "assertion_end_line": 108,
    "original_assertion_block": "{\n\t\tname:  \"episode part\",\n\t\ttitle: \"You and I Are Polar Opposites S01E10 Class Trip Part 1 1080p CR WEB-DL DUAL AAC2.0 H 264-VARYG (Seihantai na Kimi to Boku, Dual-Audio, Multi-Subs)\",\n\t\twant: TorrentInfo{\n\t\t\tTitle:      \"You and I Are Polar Opposites\",\n\t\t\tSeason:     1,\n\t\t\tEpisode:    10,\n\t\t\tPart:       1,\n\t\t\tResolution: \"1080p\",\n\t\t\tQuality:    \"WEB-DL\",\n\t\t\tCodec:      \"H264\",\n\t\t\tAudio:      \"AAC2.0\",\n\t\t\tSource:     \"CR\",\n\t\t\tGroup:      \"VARYG\",\n\t\t\tEdition:    \"Dual Audio Multi Subs\",\n\t\t},\n\t\tfields: []string{\"title\", \"season\", \"episode\", \"part\", \"resolution\", \"quality\", \"codec\", \"audio\", \"source\", \"group\", \"edition\"},\n\t}",
    "original_literal_fields": {
      "Audio": "AAC2.0",
      "Codec": "H264",
      "Edition": "Dual Audio Multi Subs",
      "Episode": 10,
      "Group": "VARYG",
      "Part": 1,
      "Quality": "WEB-DL",
      "Resolution": "1080p",
      "Season": 1,
      "Source": "CR",
      "Title": "You and I Are Polar Opposites"
    },
    "original_asserted_fields": {
      "Audio": "AAC2.0",
      "Codec": "H264",
      "Edition": "Dual Audio Multi Subs",
      "Episode": 10,
      "Group": "VARYG",
      "Part": 1,
      "Quality": "WEB-DL",
      "Resolution": "1080p",
      "Season": 1,
      "Source": "CR",
      "Title": "You and I Are Polar Opposites"
    },
    "selected_fields": [
      "title",
      "season",
      "episode",
      "part",
      "resolution",
      "quality",
      "codec",
      "audio",
      "source",
      "group",
      "edition"
    ],
    "field_lines": {
      "Audio": 102,
      "Codec": 101,
      "Edition": 105,
      "Episode": 97,
      "Group": 104,
      "Part": 98,
      "Quality": 100,
      "Resolution": 99,
      "Season": 96,
      "Source": 103,
      "Title": 95
    },
    "expected": {
      "episode": 10,
      "group": "VARYG",
      "screen_size": "1080p",
      "season": 1,
      "source": "Web",
      "title": "You and I Are Polar Opposites",
      "video_codec": "H.264"
    }
  },
  "You and I Are Polar Opposites S01E11 Class Trip Part 2 1080p CR WEB DL AAC2 0 H 264 VARYG (Seihantai na Kimi to Boku  Multi Subs)": {
    "input": "You and I Are Polar Opposites S01E11 Class Trip Part 2 1080p CR WEB DL AAC2 0 H 264 VARYG (Seihantai na Kimi to Boku  Multi Subs)",
    "original_file": "fixture_accuracy_test.go",
    "input_line": 246,
    "original_index": 20,
    "assertion_start_line": 244,
    "assertion_end_line": 255,
    "original_assertion_block": "{\n\t\tname:  \"second episode part\",\n\t\ttitle: \"You and I Are Polar Opposites S01E11 Class Trip Part 2 1080p CR WEB DL AAC2 0 H 264 VARYG (Seihantai na Kimi to Boku  Multi Subs)\",\n\t\twant: TorrentInfo{\n\t\t\tSeason:  1,\n\t\t\tEpisode: 11,\n\t\t\tPart:    2,\n\t\t\tSource:  \"CR\",\n\t\t\tEdition: \"Multi Subs\",\n\t\t},\n\t\tfields: []string{\"season\", \"episode\", \"part\", \"source\", \"group\", \"edition\"},\n\t}",
    "original_literal_fields": {
      "Edition": "Multi Subs",
      "Episode": 11,
      "Part": 2,
      "Season": 1,
      "Source": "CR"
    },
    "original_asserted_fields": {
      "Edition": "Multi Subs",
      "Episode": 11,
      "Group": "",
      "Part": 2,
      "Season": 1,
      "Source": "CR"
    },
    "selected_fields": [
      "season",
      "episode",
      "part",
      "source",
      "group",
      "edition"
    ],
    "field_lines": {
      "Edition": 252,
      "Episode": 249,
      "Part": 250,
      "Season": 248,
      "Source": 251
    },
    "expected": {
      "episode": 11,
      "group": "",
      "season": 1
    }
  },
  "Young.Sherlock.2026.S01.720p.ITA-ENG.MULTI.WEBRip.x265.AAC-V3SP4EV3R": {
    "input": "Young.Sherlock.2026.S01.720p.ITA-ENG.MULTI.WEBRip.x265.AAC-V3SP4EV3R",
    "original_file": "fixture_accuracy_test.go",
    "input_line": 157,
    "original_index": 151,
    "assertion_start_line": 155,
    "assertion_end_line": 169,
    "original_assertion_block": "{\n\t\tname:  \"season only multilingual\",\n\t\ttitle: \"Young.Sherlock.2026.S01.720p.ITA-ENG.MULTI.WEBRip.x265.AAC-V3SP4EV3R\",\n\t\twant: TorrentInfo{\n\t\t\tTitle:      \"Young Sherlock\",\n\t\t\tSeason:     1,\n\t\t\tYear:       2026,\n\t\t\tResolution: \"720p\",\n\t\t\tQuality:    \"WEBRip\",\n\t\t\tCodec:      \"x265\",\n\t\t\tAudio:      \"AAC\",\n\t\t\tGroup:      \"V3SP4EV3R\",\n\t\t},\n\t\tfields: []string{\"title\", \"season\", \"year\", \"resolution\", \"quality\", \"codec\", \"audio\", \"group\"},\n\t}",
    "original_literal_fields": {
      "Audio": "AAC",
      "Codec": "x265",
      "Group": "V3SP4EV3R",
      "Quality": "WEBRip",
      "Resolution": "720p",
      "Season": 1,
      "Title": "Young Sherlock",
      "Year": 2026
    },
    "original_asserted_fields": {
      "Audio": "AAC",
      "Codec": "x265",
      "Group": "V3SP4EV3R",
      "Quality": "WEBRip",
      "Resolution": "720p",
      "Season": 1,
      "Title": "Young Sherlock",
      "Year": 2026
    },
    "selected_fields": [
      "title",
      "season",
      "year",
      "resolution",
      "quality",
      "codec",
      "audio",
      "group"
    ],
    "field_lines": {
      "Audio": 165,
      "Codec": 164,
      "Group": 166,
      "Quality": 163,
      "Resolution": 162,
      "Season": 160,
      "Title": 159,
      "Year": 161
    },
    "expected": {
      "group": "V3SP4EV3R",
      "screen_size": "720p",
      "season": 1,
      "source": "Web",
      "title": "Young Sherlock",
      "video_codec": "H.265",
      "year": 2026
    }
  },
  "Zootopia 2 2025 1080p Blu ray Remux AVC TrueHD Atmos 7 1 CiNEPHiL": {
    "input": "Zootopia 2 2025 1080p Blu ray Remux AVC TrueHD Atmos 7 1 CiNEPHiL",
    "original_file": "parser_test.go",
    "input_line": 982,
    "original_index": 88,
    "assertion_start_line": 980,
    "assertion_end_line": 992,
    "original_assertion_block": "{\n\t\t\tname:     \"bluray remux avc truehd\",\n\t\t\tfilename: \"Zootopia 2 2025 1080p Blu ray Remux AVC TrueHD Atmos 7 1 CiNEPHiL\",\n\t\t\twant: TorrentInfo{\n\t\t\t\tTitle:      \"Zootopia 2\",\n\t\t\t\tYear:       2025,\n\t\t\t\tResolution: \"1080p\",\n\t\t\t\tQuality:    \"REMUX\",\n\t\t\t\tCodec:      \"H264\",\n\t\t\t\tAudio:      \"TrueHD Atmos 7.1\",\n\t\t\t\tGroup:      \"CiNEPHiL\",\n\t\t\t},\n\t\t}",
    "original_literal_fields": {
      "Audio": "TrueHD Atmos 7.1",
      "Codec": "H264",
      "Group": "CiNEPHiL",
      "Quality": "REMUX",
      "Resolution": "1080p",
      "Title": "Zootopia 2",
      "Year": 2025
    },
    "original_asserted_fields": {
      "Audio": "TrueHD Atmos 7.1",
      "Codec": "H264",
      "Group": "CiNEPHiL",
      "Quality": "REMUX",
      "Resolution": "1080p",
      "Title": "Zootopia 2",
      "Year": 2025
    },
    "selected_fields": null,
    "field_lines": {
      "Audio": 989,
      "Codec": 988,
      "Group": 990,
      "Quality": 987,
      "Resolution": 986,
      "Title": 984,
      "Year": 985
    },
    "expected": {
      "group": "CiNEPHiL",
      "screen_size": "1080p",
      "source": "Blu-ray",
      "title": "Zootopia 2",
      "video_codec": "H.264",
      "year": 2025
    }
  },
  "[Anime Time] Sentenced To Be A Hero (Yuusha-kei ni Shosu: Choubatsu Yuusha 9004 Tai Keimu Kiroku) - S01E05 [1080p][HEVC 10bit x265][AAC][Dual-Audio] [Multi Sub] [Weekly]": {
    "input": "[Anime Time] Sentenced To Be A Hero (Yuusha-kei ni Shosu: Choubatsu Yuusha 9004 Tai Keimu Kiroku) - S01E05 [1080p][HEVC 10bit x265][AAC][Dual-Audio] [Multi Sub] [Weekly]",
    "original_file": "fixture_accuracy_test.go",
    "input_line": 32,
    "original_index": 143,
    "assertion_start_line": 30,
    "assertion_end_line": 45,
    "original_assertion_block": "{\n\t\tname:  \"anime dual audio multisub\",\n\t\ttitle: \"[Anime Time] Sentenced To Be A Hero (Yuusha-kei ni Shosu: Choubatsu Yuusha 9004 Tai Keimu Kiroku) - S01E05 [1080p][HEVC 10bit x265][AAC][Dual-Audio] [Multi Sub] [Weekly]\",\n\t\twant: TorrentInfo{\n\t\t\tTitle:      \"Sentenced To Be A Hero\",\n\t\t\tSeason:     1,\n\t\t\tEpisode:    5,\n\t\t\tResolution: \"1080p\",\n\t\t\tCodec:      \"H265\",\n\t\t\tAudio:      \"AAC\",\n\t\t\tWebsite:    \"Anime Time\",\n\t\t\tBitDepth:   \"10-bit\",\n\t\t\tEdition:    \"Dual Audio Multi Subs\",\n\t\t},\n\t\tfields: []string{\"title\", \"season\", \"episode\", \"resolution\", \"codec\", \"audio\", \"website\", \"bit_depth\", \"edition\"},\n\t}",
    "original_literal_fields": {
      "Audio": "AAC",
      "BitDepth": "10-bit",
      "Codec": "H265",
      "Edition": "Dual Audio Multi Subs",
      "Episode": 5,
      "Resolution": "1080p",
      "Season": 1,
      "Title": "Sentenced To Be A Hero",
      "Website": "Anime Time"
    },
    "original_asserted_fields": {
      "Audio": "AAC",
      "BitDepth": "10-bit",
      "Codec": "H265",
      "Edition": "Dual Audio Multi Subs",
      "Episode": 5,
      "Resolution": "1080p",
      "Season": 1,
      "Title": "Sentenced To Be A Hero",
      "Website": "Anime Time"
    },
    "selected_fields": [
      "title",
      "season",
      "episode",
      "resolution",
      "codec",
      "audio",
      "website",
      "bit_depth",
      "edition"
    ],
    "field_lines": {
      "Audio": 39,
      "BitDepth": 41,
      "Codec": 38,
      "Edition": 42,
      "Episode": 36,
      "Resolution": 37,
      "Season": 35,
      "Title": 34,
      "Website": 40
    },
    "expected": {
      "episode": 5,
      "screen_size": "1080p",
      "season": 1,
      "title": "Sentenced To Be A Hero",
      "video_codec": "H.265"
    }
  },
  "[ToonsHub] In the Clear Moonlit Dusk S01E09 1080p CR WEB DL DUAL AAC2 0 H 264 (Uruwashi no Yoi no Tsuki  Dual Audio  Multi Subs)": {
    "input": "[ToonsHub] In the Clear Moonlit Dusk S01E09 1080p CR WEB DL DUAL AAC2 0 H 264 (Uruwashi no Yoi no Tsuki  Dual Audio  Multi Subs)",
    "original_file": "fixture_accuracy_test.go",
    "input_line": 229,
    "original_index": 153,
    "assertion_start_line": 227,
    "assertion_end_line": 243,
    "original_assertion_block": "{\n\t\tname:  \"website dual audio source without group\",\n\t\ttitle: \"[ToonsHub] In the Clear Moonlit Dusk S01E09 1080p CR WEB DL DUAL AAC2 0 H 264 (Uruwashi no Yoi no Tsuki  Dual Audio  Multi Subs)\",\n\t\twant: TorrentInfo{\n\t\t\tTitle:      \"In the Clear Moonlit Dusk\",\n\t\t\tSeason:     1,\n\t\t\tEpisode:    9,\n\t\t\tResolution: \"1080p\",\n\t\t\tQuality:    \"WEB-DL\",\n\t\t\tCodec:      \"H264\",\n\t\t\tAudio:      \"AAC2.0\",\n\t\t\tSource:     \"CR\",\n\t\t\tWebsite:    \"ToonsHub\",\n\t\t\tEdition:    \"Dual Audio Multi Subs\",\n\t\t},\n\t\tfields: []string{\"title\", \"season\", \"episode\", \"resolution\", \"quality\", \"codec\", \"audio\", \"source\", \"group\", \"website\", \"edition\"},\n\t}",
    "original_literal_fields": {
      "Audio": "AAC2.0",
      "Codec": "H264",
      "Edition": "Dual Audio Multi Subs",
      "Episode": 9,
      "Quality": "WEB-DL",
      "Resolution": "1080p",
      "Season": 1,
      "Source": "CR",
      "Title": "In the Clear Moonlit Dusk",
      "Website": "ToonsHub"
    },
    "original_asserted_fields": {
      "Audio": "AAC2.0",
      "Codec": "H264",
      "Edition": "Dual Audio Multi Subs",
      "Episode": 9,
      "Group": "",
      "Quality": "WEB-DL",
      "Resolution": "1080p",
      "Season": 1,
      "Source": "CR",
      "Title": "In the Clear Moonlit Dusk",
      "Website": "ToonsHub"
    },
    "selected_fields": [
      "title",
      "season",
      "episode",
      "resolution",
      "quality",
      "codec",
      "audio",
      "source",
      "group",
      "website",
      "edition"
    ],
    "field_lines": {
      "Audio": 237,
      "Codec": 236,
      "Edition": 240,
      "Episode": 233,
      "Quality": 235,
      "Resolution": 234,
      "Season": 232,
      "Source": 238,
      "Title": 231,
      "Website": 239
    },
    "expected": {
      "episode": 9,
      "group": "",
      "screen_size": "1080p",
      "season": 1,
      "source": "Web",
      "title": "In the Clear Moonlit Dusk",
      "video_codec": "H.264"
    }
  },
  "[hchcsen] Gurren Lagann The Movie  Childhood  039 s End (2008) v2 (BD Remux 1080p x264 8 bit TrueHD Atmos) [Dual Audio]": {
    "input": "[hchcsen] Gurren Lagann The Movie  Childhood  039 s End (2008) v2 (BD Remux 1080p x264 8 bit TrueHD Atmos) [Dual Audio]",
    "original_file": "fixture_accuracy_test.go",
    "input_line": 258,
    "original_index": 154,
    "assertion_start_line": 256,
    "assertion_end_line": 271,
    "original_assertion_block": "{\n\t\tname:  \"malformed apostrophe remux\",\n\t\ttitle: \"[hchcsen] Gurren Lagann The Movie  Childhood  039 s End (2008) v2 (BD Remux 1080p x264 8 bit TrueHD Atmos) [Dual Audio]\",\n\t\twant: TorrentInfo{\n\t\t\tTitle:      \"Gurren Lagann The Movie Childhood's End\",\n\t\t\tYear:       2008,\n\t\t\tResolution: \"1080p\",\n\t\t\tQuality:    \"REMUX\",\n\t\t\tCodec:      \"x264\",\n\t\t\tAudio:      \"TrueHD Atmos\",\n\t\t\tWebsite:    \"hchcsen\",\n\t\t\tBitDepth:   \"8-bit\",\n\t\t\tEdition:    \"Dual Audio\",\n\t\t},\n\t\tfields: []string{\"title\", \"year\", \"resolution\", \"quality\", \"codec\", \"audio\", \"website\", \"bit_depth\", \"edition\"},\n\t}",
    "original_literal_fields": {
      "Audio": "TrueHD Atmos",
      "BitDepth": "8-bit",
      "Codec": "x264",
      "Edition": "Dual Audio",
      "Quality": "REMUX",
      "Resolution": "1080p",
      "Title": "Gurren Lagann The Movie Childhood's End",
      "Website": "hchcsen",
      "Year": 2008
    },
    "original_asserted_fields": {
      "Audio": "TrueHD Atmos",
      "BitDepth": "8-bit",
      "Codec": "x264",
      "Edition": "Dual Audio",
      "Quality": "REMUX",
      "Resolution": "1080p",
      "Title": "Gurren Lagann The Movie Childhood's End",
      "Website": "hchcsen",
      "Year": 2008
    },
    "selected_fields": [
      "title",
      "year",
      "resolution",
      "quality",
      "codec",
      "audio",
      "website",
      "bit_depth",
      "edition"
    ],
    "field_lines": {
      "Audio": 265,
      "BitDepth": 267,
      "Codec": 264,
      "Edition": 268,
      "Quality": 263,
      "Resolution": 262,
      "Title": 260,
      "Website": 266,
      "Year": 261
    },
    "expected": {
      "screen_size": "1080p",
      "source": "Blu-ray",
      "title": "Gurren Lagann The Movie Childhood's End",
      "video_codec": "H.264",
      "year": 2008
    }
  },
  "[hchcsen] Gurren Lagann the Movie: The Lights in the Sky Are Stars (2009) v2 (BD Remux 1080p x264 8-bit TrueHD Atmos) [Dual Audio] | Tengen Toppa Gurren Lagann: Lagann-hen": {
    "input": "[hchcsen] Gurren Lagann the Movie: The Lights in the Sky Are Stars (2009) v2 (BD Remux 1080p x264 8-bit TrueHD Atmos) [Dual Audio] | Tengen Toppa Gurren Lagann: Lagann-hen",
    "original_file": "fixture_accuracy_test.go",
    "input_line": 111,
    "original_index": 148,
    "assertion_start_line": 109,
    "assertion_end_line": 124,
    "original_assertion_block": "{\n\t\tname:  \"remux truehd atmos\",\n\t\ttitle: \"[hchcsen] Gurren Lagann the Movie: The Lights in the Sky Are Stars (2009) v2 (BD Remux 1080p x264 8-bit TrueHD Atmos) [Dual Audio] | Tengen Toppa Gurren Lagann: Lagann-hen\",\n\t\twant: TorrentInfo{\n\t\t\tTitle:      \"Gurren Lagann the Movie: The Lights in the Sky Are Stars\",\n\t\t\tYear:       2009,\n\t\t\tResolution: \"1080p\",\n\t\t\tQuality:    \"REMUX\",\n\t\t\tCodec:      \"x264\",\n\t\t\tAudio:      \"TrueHD Atmos\",\n\t\t\tWebsite:    \"hchcsen\",\n\t\t\tBitDepth:   \"8-bit\",\n\t\t\tEdition:    \"Dual Audio\",\n\t\t},\n\t\tfields: []string{\"title\", \"year\", \"resolution\", \"quality\", \"codec\", \"audio\", \"website\", \"bit_depth\", \"edition\"},\n\t}",
    "original_literal_fields": {
      "Audio": "TrueHD Atmos",
      "BitDepth": "8-bit",
      "Codec": "x264",
      "Edition": "Dual Audio",
      "Quality": "REMUX",
      "Resolution": "1080p",
      "Title": "Gurren Lagann the Movie: The Lights in the Sky Are Stars",
      "Website": "hchcsen",
      "Year": 2009
    },
    "original_asserted_fields": {
      "Audio": "TrueHD Atmos",
      "BitDepth": "8-bit",
      "Codec": "x264",
      "Edition": "Dual Audio",
      "Quality": "REMUX",
      "Resolution": "1080p",
      "Title": "Gurren Lagann the Movie: The Lights in the Sky Are Stars",
      "Website": "hchcsen",
      "Year": 2009
    },
    "selected_fields": [
      "title",
      "year",
      "resolution",
      "quality",
      "codec",
      "audio",
      "website",
      "bit_depth",
      "edition"
    ],
    "field_lines": {
      "Audio": 118,
      "BitDepth": 120,
      "Codec": 117,
      "Edition": 121,
      "Quality": 116,
      "Resolution": 115,
      "Title": 113,
      "Website": 119,
      "Year": 114
    },
    "expected": {
      "screen_size": "1080p",
      "source": "Blu-ray",
      "title": "Gurren Lagann the Movie: The Lights in the Sky Are Stars",
      "video_codec": "H.264",
      "year": 2009
    }
  }
}
`
const chillFrozenContracts = `{
  "schema": "pulse.release-review.v1",
  "reviewed_entries": 33,
  "required_entries": 1000,
  "formal_golden_acceptance": false,
  "review_method": "Independent manual input reading before any execution of PulseMedia. No upstream parser executed. Tuples explicitly specify all 12 expected fields; seven quality values are resolution/source/codec/hdr/audio/edition/group. Expansion only changes representation. is_anime describes an unbound anime numbering interpretation, not guessed genre. Audio/edition are withheld unsupported measurement facts. No tracker occurrence proof required.",
  "entries": [
    {
      "id": "chill-p-35",
      "source": "chill-parser-original",
      "input": "Childhood.039.s.End.2008.1080p.BluRay.x264-GRP",
      "review_note": "Known 2008 Gurren Lagann film subtitle, realistic dot/entity-corrupted filename handwritten as an individual assertion. Common literal 039 apostrophe marker; not a fabricated work or generated permutation. Keep the original title assertion and explicit year; BluRay/x264 and delimited GRP only.",
      "expected": {
        "title": "Childhood's End",
        "year": 2008,
        "media_type": "movie",
        "season": 0,
        "episodes": [],
        "unit_keys": [
          "movie"
        ],
        "absolute_episode": 0,
        "is_anime": false,
        "confidence": 0.9,
        "reason": "clean_movie_with_year",
        "requires_review": false,
        "quality": {
          "resolution": "1080p",
          "source": "Bluray",
          "codec": "H264",
          "hdr": "",
          "audio": "",
          "edition": "",
          "group": "GRP"
        }
      }
    },
    {
      "id": "chill-p-669",
      "source": "chill-parser-original",
      "input": "SD.Gundam.World.Heroes.S01E01.1080p.WEB-DL.H265",
      "review_note": "Known series, independently handwritten full S01E01 input; leading SD belongs to the exact original title, not resolution. Explicit 1080p/WEB-DL/H265; no absolute or genre inference.",
      "expected": {
        "title": "SD Gundam World Heroes",
        "year": 0,
        "media_type": "tv",
        "season": 1,
        "episodes": [
          1
        ],
        "unit_keys": [
          "S01E01"
        ],
        "absolute_episode": 0,
        "is_anime": false,
        "confidence": 0.95,
        "reason": "standard_season_episode",
        "requires_review": false,
        "quality": {
          "resolution": "1080p",
          "source": "WEB-DL",
          "codec": "HEVC",
          "hdr": "",
          "audio": "",
          "edition": "",
          "group": ""
        }
      }
    },
    {
      "id": "chill-p-680",
      "source": "chill-parser-original",
      "input": "SD.Gundam.World.Heroes.S01E01.WEB-DL.H265",
      "review_note": "Second individually handwritten realistic S01E01 assertion for the same known series is a different exact filename, not generated table enumeration. No measured resolution: title SD must not manufacture one.",
      "expected": {
        "title": "SD Gundam World Heroes",
        "year": 0,
        "media_type": "tv",
        "season": 1,
        "episodes": [
          1
        ],
        "unit_keys": [
          "S01E01"
        ],
        "absolute_episode": 0,
        "is_anime": false,
        "confidence": 0.95,
        "reason": "standard_season_episode",
        "requires_review": false,
        "quality": {
          "resolution": "",
          "source": "WEB-DL",
          "codec": "HEVC",
          "hdr": "",
          "audio": "",
          "edition": "",
          "group": ""
        }
      }
    },
    {
      "id": "chill-p-951",
      "source": "chill-parser-original",
      "input": "Kaya-chan.Isnt.Scary.S01E11.1080p.CR.WEB-DL.DUAL.AAC2.0.H.264-VARYG",
      "review_note": "Known TV series; preserve exact hyphenated title and single episode 11. CR/DUAL/AAC are retained raw source labels, not supported measured quality fields. Delimited publisher VARYG is explicit.",
      "expected": {
        "title": "Kaya-chan Isnt Scary",
        "year": 0,
        "media_type": "tv",
        "season": 1,
        "episodes": [
          11
        ],
        "unit_keys": [
          "S01E11"
        ],
        "absolute_episode": 0,
        "is_anime": false,
        "confidence": 0.95,
        "reason": "standard_season_episode",
        "requires_review": false,
        "quality": {
          "resolution": "1080p",
          "source": "WEB-DL",
          "codec": "H264",
          "hdr": "",
          "audio": "",
          "edition": "",
          "group": "VARYG"
        }
      }
    },
    {
      "id": "chill-p-967",
      "source": "chill-parser-original",
      "input": "Monarch Legacy of Monsters S02E04 2160p HDR10Plus DV WEBRip 6CH x265 HEVC-P",
      "review_note": "Known series, season 2 episode 4 only. Explicit WEBRip and x265/HEVC agree. DV and HDR10Plus remain raw; the single supported HDR representative is DV. 6CH alone is not a measured audio profile; -P is an explicit group label.",
      "expected": {
        "title": "Monarch Legacy of Monsters",
        "year": 0,
        "media_type": "tv",
        "season": 2,
        "episodes": [
          4
        ],
        "unit_keys": [
          "S02E04"
        ],
        "absolute_episode": 0,
        "is_anime": false,
        "confidence": 0.95,
        "reason": "standard_season_episode",
        "requires_review": false,
        "quality": {
          "resolution": "2160p",
          "source": "WEBRip",
          "codec": "HEVC",
          "hdr": "DV",
          "audio": "",
          "edition": "",
          "group": "P"
        }
      }
    },
    {
      "id": "chill-p-982",
      "source": "chill-parser-original",
      "input": "Zootopia 2 2025 1080p Blu ray Remux AVC TrueHD Atmos 7 1 CiNEPHiL",
      "review_note": "Known movie, sequel number 2 stays in title and 2025 is year. Blu ray Remux/AVC explicitly support Remux/H264. TrueHD Atmos and unbound CiNEPHiL remain source evidence, not promoted audio or group facts.",
      "expected": {
        "title": "Zootopia 2",
        "year": 2025,
        "media_type": "movie",
        "season": 0,
        "episodes": [],
        "unit_keys": [
          "movie"
        ],
        "absolute_episode": 0,
        "is_anime": false,
        "confidence": 0.9,
        "reason": "clean_movie_with_year",
        "requires_review": false,
        "quality": {
          "resolution": "1080p",
          "source": "Remux",
          "codec": "H264",
          "hdr": "",
          "audio": "",
          "edition": "",
          "group": ""
        }
      }
    },
    {
      "id": "chill-p-995",
      "source": "chill-parser-original",
      "input": "Nightmare Alley 2021 2160p UHD Blu ray Remux DV HDR HEVC TrueHD Atmos 7 1 CiNEPHiLES",
      "review_note": "Known movie; one explicit year. REMUX/HEVC/DV are supported facts. Preserve HDR and TrueHD Atmos source labels without expanding the seven-field vocabulary; undelimited CiNEPHiLES not assigned a publisher identity.",
      "expected": {
        "title": "Nightmare Alley",
        "year": 2021,
        "media_type": "movie",
        "season": 0,
        "episodes": [],
        "unit_keys": [
          "movie"
        ],
        "absolute_episode": 0,
        "is_anime": false,
        "confidence": 0.9,
        "reason": "clean_movie_with_year",
        "requires_review": false,
        "quality": {
          "resolution": "2160p",
          "source": "Remux",
          "codec": "HEVC",
          "hdr": "DV",
          "audio": "",
          "edition": "",
          "group": ""
        }
      }
    },
    {
      "id": "chill-p-1009",
      "source": "chill-parser-original",
      "input": "Crossing Jordan 2001 Complete Series Seasons 1 to 6 1080p WEB x264 [i_c]",
      "review_note": "Known TV series, complete seasons 1 through 6 state no full episode list and cannot authorize any unit. Season 0 means no single season claim; explicit 2001 retained. Generic WEB does not establish WEB-DL or WEBRip. Bracket i_c is the original publisher assertion.",
      "expected": {
        "title": "Crossing Jordan",
        "year": 2001,
        "media_type": "tv",
        "season": 0,
        "episodes": [],
        "unit_keys": [],
        "absolute_episode": 0,
        "is_anime": false,
        "confidence": 0,
        "reason": "multi_unit_collection",
        "requires_review": true,
        "quality": {
          "resolution": "1080p",
          "source": "",
          "codec": "H264",
          "hdr": "",
          "audio": "",
          "edition": "",
          "group": "i_c"
        }
      }
    },
    {
      "id": "chill-p-1290",
      "source": "chill-parser-original",
      "input": "Mr.Hollands.Opus.1995.1080p.BluRay.x264-GRP",
      "review_note": "Known 1995 film, individually handwritten realistic title boundary assertion. Opus belongs to title, not audio. Keep original punctuation-less title; BluRay/x264 and explicit -GRP support the three quality fields.",
      "expected": {
        "title": "Mr Hollands Opus",
        "year": 1995,
        "media_type": "movie",
        "season": 0,
        "episodes": [],
        "unit_keys": [
          "movie"
        ],
        "absolute_episode": 0,
        "is_anime": false,
        "confidence": 0.9,
        "reason": "clean_movie_with_year",
        "requires_review": false,
        "quality": {
          "resolution": "1080p",
          "source": "Bluray",
          "codec": "H264",
          "hdr": "",
          "audio": "",
          "edition": "",
          "group": "GRP"
        }
      }
    },
    {
      "id": "chill-f-18",
      "source": "chill-parser-original",
      "input": "Delivery Run 2024 BluRay 1080p DDP 5 1 x264 hallowed",
      "review_note": "Known 2024 film, exact fixture literal and explicit year. BluRay/x264 support source and codec. DDP5.1 and undelimited hallowed remain original assertions only; no audio or publisher measurement is inferred.",
      "expected": {
        "title": "Delivery Run",
        "year": 2024,
        "media_type": "movie",
        "season": 0,
        "episodes": [],
        "unit_keys": [
          "movie"
        ],
        "absolute_episode": 0,
        "is_anime": false,
        "confidence": 0.9,
        "reason": "clean_movie_with_year",
        "requires_review": false,
        "quality": {
          "resolution": "1080p",
          "source": "Bluray",
          "codec": "H264",
          "hdr": "",
          "audio": "",
          "edition": "",
          "group": ""
        }
      }
    },
    {
      "id": "chill-f-32",
      "source": "chill-parser-original",
      "input": "[Anime Time] Sentenced To Be A Hero (Yuusha-kei ni Shosu: Choubatsu Yuusha 9004 Tai Keimu Kiroku) - S01E05 [1080p][HEVC 10bit x265][AAC][Dual-Audio] [Multi Sub] [Weekly]",
      "review_note": "Known series, full S01E05 coverage; the long parenthesized Japanese alternate title, including 9004, is title metadata, not a year or absolute episode. HEVC/x265 explicit; Anime Time is website evidence only; no source format is stated.",
      "expected": {
        "title": "Sentenced To Be A Hero",
        "year": 0,
        "media_type": "tv",
        "season": 1,
        "episodes": [
          5
        ],
        "unit_keys": [
          "S01E05"
        ],
        "absolute_episode": 0,
        "is_anime": false,
        "confidence": 0.95,
        "reason": "standard_season_episode",
        "requires_review": false,
        "quality": {
          "resolution": "1080p",
          "source": "",
          "codec": "HEVC",
          "hdr": "",
          "audio": "",
          "edition": "",
          "group": ""
        }
      }
    },
    {
      "id": "chill-f-48",
      "source": "chill-parser-original",
      "input": "Kaya chan Isnt Scary S01E11 1080p CR WEB DL DUAL AAC2 0 H 264 VARYG (Kaya chan wa Kowakunai  Dual Audio  Multi Subs)",
      "review_note": "Known series, retain original space-separated title and full episode 11. WEB DL/H 264 are explicit token-separated technical labels. Parenthesized alternate title and unbound VARYG do not change coverage or publisher authority.",
      "expected": {
        "title": "Kaya chan Isnt Scary",
        "year": 0,
        "media_type": "tv",
        "season": 1,
        "episodes": [
          11
        ],
        "unit_keys": [
          "S01E11"
        ],
        "absolute_episode": 0,
        "is_anime": false,
        "confidence": 0.95,
        "reason": "standard_season_episode",
        "requires_review": false,
        "quality": {
          "resolution": "1080p",
          "source": "WEB-DL",
          "codec": "H264",
          "hdr": "",
          "audio": "",
          "edition": "",
          "group": ""
        }
      }
    },
    {
      "id": "chill-f-64",
      "source": "chill-parser-original",
      "input": "Nightmare Alley 2021 BW Directors Cut 2160p UHD Blu ray Remux DV HDR HEVC DTS HD MA 5 1 CiNEPHiLES",
      "review_note": "Known movie, BW Directors Cut is edition evidence and cannot become title or measured edition fact. 2021, remux, HEVC, DV explicit. Original DTS-HD MA, edition and CiNEPHiLES labels retained without promotion.",
      "expected": {
        "title": "Nightmare Alley",
        "year": 2021,
        "media_type": "movie",
        "season": 0,
        "episodes": [],
        "unit_keys": [
          "movie"
        ],
        "absolute_episode": 0,
        "is_anime": false,
        "confidence": 0.9,
        "reason": "clean_movie_with_year",
        "requires_review": false,
        "quality": {
          "resolution": "2160p",
          "source": "Remux",
          "codec": "HEVC",
          "hdr": "DV",
          "audio": "",
          "edition": "",
          "group": ""
        }
      }
    },
    {
      "id": "chill-f-80",
      "source": "chill-parser-original",
      "input": "On the Brain 2018 HDRip XviD AC3-EVO",
      "review_note": "Known film with exact asserted 2018 in this filename; do not replace source year with external release metadata. HDRip is not HDR nor any supported source; XviD is unsupported, AC3 not measured audio. Explicit -EVO alone is group evidence.",
      "expected": {
        "title": "On the Brain",
        "year": 2018,
        "media_type": "movie",
        "season": 0,
        "episodes": [],
        "unit_keys": [
          "movie"
        ],
        "absolute_episode": 0,
        "is_anime": false,
        "confidence": 0.9,
        "reason": "clean_movie_with_year",
        "requires_review": false,
        "quality": {
          "resolution": "",
          "source": "",
          "codec": "",
          "hdr": "",
          "audio": "",
          "edition": "",
          "group": "EVO"
        }
      }
    },
    {
      "id": "chill-f-93",
      "source": "chill-parser-original",
      "input": "You and I Are Polar Opposites S01E10 Class Trip Part 1 1080p CR WEB-DL DUAL AAC2.0 H 264-VARYG (Seihantai na Kimi to Boku, Dual-Audio, Multi-Subs)",
      "review_note": "Known episode 10; Class Trip Part 1 is an episode subtitle and source Part scalar, not evidence that the file is half of episode 10. Keep complete single-episode assertion and -VARYG; no invented absolute mapping or source-platform quality.",
      "expected": {
        "title": "You and I Are Polar Opposites",
        "year": 0,
        "media_type": "tv",
        "season": 1,
        "episodes": [
          10
        ],
        "unit_keys": [
          "S01E10"
        ],
        "absolute_episode": 0,
        "is_anime": false,
        "confidence": 0.95,
        "reason": "standard_season_episode",
        "requires_review": false,
        "quality": {
          "resolution": "1080p",
          "source": "WEB-DL",
          "codec": "H264",
          "hdr": "",
          "audio": "",
          "edition": "",
          "group": "VARYG"
        }
      }
    },
    {
      "id": "chill-f-111",
      "source": "chill-parser-original",
      "input": "[hchcsen] Gurren Lagann the Movie: The Lights in the Sky Are Stars (2009) v2 (BD Remux 1080p x264 8-bit TrueHD Atmos) [Dual Audio] | Tengen Toppa Gurren Lagann: Lagann-hen",
      "review_note": "Known 2009 anime movie, exact original English title includes colon. Alternate title after | and v2 are metadata, not additional coverage. BD Remux/x264 explicit; hchcsen website, 8-bit, Dual Audio and TrueHD Atmos remain raw only.",
      "expected": {
        "title": "Gurren Lagann the Movie: The Lights in the Sky Are Stars",
        "year": 2009,
        "media_type": "movie",
        "season": 0,
        "episodes": [],
        "unit_keys": [
          "movie"
        ],
        "absolute_episode": 0,
        "is_anime": false,
        "confidence": 0.9,
        "reason": "clean_movie_with_year",
        "requires_review": false,
        "quality": {
          "resolution": "1080p",
          "source": "Remux",
          "codec": "H264",
          "hdr": "",
          "audio": "",
          "edition": "",
          "group": ""
        }
      }
    },
    {
      "id": "chill-f-127",
      "source": "chill-parser-original",
      "input": "Roman Empire By Train With Alice Roberts S01E06 720p HDTV H264-JFF",
      "review_note": "Known documentary series, full S01E06 only; no inferred year. Explicit 720p HDTV H264 and delimited -JFF.",
      "expected": {
        "title": "Roman Empire By Train With Alice Roberts",
        "year": 0,
        "media_type": "tv",
        "season": 1,
        "episodes": [
          6
        ],
        "unit_keys": [
          "S01E06"
        ],
        "absolute_episode": 0,
        "is_anime": false,
        "confidence": 0.95,
        "reason": "standard_season_episode",
        "requires_review": false,
        "quality": {
          "resolution": "720p",
          "source": "HDTV",
          "codec": "H264",
          "hdr": "",
          "audio": "",
          "edition": "",
          "group": "JFF"
        }
      }
    },
    {
      "id": "chill-f-141",
      "source": "chill-parser-original",
      "input": "Peaky Blinders S03E01 Episode 1 1080p NF WEB-DL DDP5 1 H 264-Kitsune",
      "review_note": "Known series, Episode 1 repeats the same episode as S03E01 rather than adding a unit. NF is platform, DDP5.1 audio evidence retained only; -Kitsune is explicit publisher.",
      "expected": {
        "title": "Peaky Blinders",
        "year": 0,
        "media_type": "tv",
        "season": 3,
        "episodes": [
          1
        ],
        "unit_keys": [
          "S03E01"
        ],
        "absolute_episode": 0,
        "is_anime": false,
        "confidence": 0.95,
        "reason": "standard_season_episode",
        "requires_review": false,
        "quality": {
          "resolution": "1080p",
          "source": "WEB-DL",
          "codec": "H264",
          "hdr": "",
          "audio": "",
          "edition": "",
          "group": "Kitsune"
        }
      }
    },
    {
      "id": "chill-f-157",
      "source": "chill-parser-original",
      "input": "Young.Sherlock.2026.S01.720p.ITA-ENG.MULTI.WEBRip.x265.AAC-V3SP4EV3R",
      "review_note": "Known series, explicit season 1 and year 2026 but no episode enumeration. Zero coverage and confidence with review; no complete-season invention. WEBRip/x265 and -V3SP4EV3R explicit; languages and AAC not promoted.",
      "expected": {
        "title": "Young Sherlock",
        "year": 2026,
        "media_type": "tv",
        "season": 1,
        "episodes": [],
        "unit_keys": [],
        "absolute_episode": 0,
        "is_anime": false,
        "confidence": 0,
        "reason": "episode_coverage_unresolved",
        "requires_review": true,
        "quality": {
          "resolution": "720p",
          "source": "WEBRip",
          "codec": "HEVC",
          "hdr": "",
          "audio": "",
          "edition": "",
          "group": "V3SP4EV3R"
        }
      }
    },
    {
      "id": "chill-f-200",
      "source": "chill-parser-original",
      "input": "Silkwood 1983 1080p BluRay x265 10bit EAC3 2 0 r00t QxR",
      "review_note": "Known 1983 movie; BluRay/x265 explicit. 10bit and EAC3 2.0 retained raw, not HDR/audio quality. r00t/QxR have no delimiter establishing a measured publisher identity.",
      "expected": {
        "title": "Silkwood",
        "year": 1983,
        "media_type": "movie",
        "season": 0,
        "episodes": [],
        "unit_keys": [
          "movie"
        ],
        "absolute_episode": 0,
        "is_anime": false,
        "confidence": 0.9,
        "reason": "clean_movie_with_year",
        "requires_review": false,
        "quality": {
          "resolution": "1080p",
          "source": "Bluray",
          "codec": "HEVC",
          "hdr": "",
          "audio": "",
          "edition": "",
          "group": ""
        }
      }
    },
    {
      "id": "chill-f-229",
      "source": "chill-parser-original",
      "input": "[ToonsHub] In the Clear Moonlit Dusk S01E09 1080p CR WEB DL DUAL AAC2 0 H 264 (Uruwashi no Yoi no Tsuki  Dual Audio  Multi Subs)",
      "review_note": "Known series, S01E09 complete. ToonsHub is explicitly a Website assertion and original selected Group is empty. CR is platform; WEB DL/H 264 support two quality fields without promoting website or dual-audio edition.",
      "expected": {
        "title": "In the Clear Moonlit Dusk",
        "year": 0,
        "media_type": "tv",
        "season": 1,
        "episodes": [
          9
        ],
        "unit_keys": [
          "S01E09"
        ],
        "absolute_episode": 0,
        "is_anime": false,
        "confidence": 0.95,
        "reason": "standard_season_episode",
        "requires_review": false,
        "quality": {
          "resolution": "1080p",
          "source": "WEB-DL",
          "codec": "H264",
          "hdr": "",
          "audio": "",
          "edition": "",
          "group": ""
        }
      }
    },
    {
      "id": "chill-f-258",
      "source": "chill-parser-original",
      "input": "[hchcsen] Gurren Lagann The Movie  Childhood  039 s End (2008) v2 (BD Remux 1080p x264 8 bit TrueHD Atmos) [Dual Audio]",
      "review_note": "Known 2008 movie, original assertion explicitly decodes tracker 039 apostrophe. Preserve original asserted casing and title. v2 is revision; BD Remux/x264 explicit; website, TrueHD Atmos, bit depth and Dual Audio remain raw.",
      "expected": {
        "title": "Gurren Lagann The Movie Childhood's End",
        "year": 2008,
        "media_type": "movie",
        "season": 0,
        "episodes": [],
        "unit_keys": [
          "movie"
        ],
        "absolute_episode": 0,
        "is_anime": false,
        "confidence": 0.9,
        "reason": "clean_movie_with_year",
        "requires_review": false,
        "quality": {
          "resolution": "1080p",
          "source": "Remux",
          "codec": "H264",
          "hdr": "",
          "audio": "",
          "edition": "",
          "group": ""
        }
      }
    },
    {
      "id": "chill-f-274",
      "source": "chill-parser-original",
      "input": "Tar 2022 HDRip XviD AC3 EVO[TGx]",
      "review_note": "Known 2022 movie under the original ASCII title. HDRip/XviD are unsupported source/codec, not HDR. Original TGx group assertion is a relay tag, not publisher identity; EVO without a group delimiter does not establish it either.",
      "expected": {
        "title": "Tar",
        "year": 2022,
        "media_type": "movie",
        "season": 0,
        "episodes": [],
        "unit_keys": [
          "movie"
        ],
        "absolute_episode": 0,
        "is_anime": false,
        "confidence": 0.9,
        "reason": "clean_movie_with_year",
        "requires_review": false,
        "quality": {
          "resolution": "",
          "source": "",
          "codec": "",
          "hdr": "",
          "audio": "",
          "edition": "",
          "group": ""
        }
      }
    },
    {
      "id": "chill-f-299",
      "source": "chill-parser-original",
      "input": "Pyramid.Builders.New.Clues.2019.720p.x265.aac.subs.mkv",
      "review_note": "Known documentary title under original exact title/year assertion; no episode syntax. Explicit 720p/x265, no source token. aac/subs/mkv are audio/subtitle/container evidence only, not publisher or source.",
      "expected": {
        "title": "Pyramid Builders New Clues",
        "year": 2019,
        "media_type": "movie",
        "season": 0,
        "episodes": [],
        "unit_keys": [
          "movie"
        ],
        "absolute_episode": 0,
        "is_anime": false,
        "confidence": 0.9,
        "reason": "clean_movie_with_year",
        "requires_review": false,
        "quality": {
          "resolution": "720p",
          "source": "",
          "codec": "HEVC",
          "hdr": "",
          "audio": "",
          "edition": "",
          "group": ""
        }
      }
    },
    {
      "id": "chill-p-1690",
      "source": "chill-parser-original",
      "input": "It 2017 1080p BluRay x264",
      "review_note": "Previously excluded only because its original assertion is negative platform Source empty. Known 2017 movie with explicit BluRay/x264; It must remain title, not an Italian platform or language-derived source.",
      "expected": {
        "title": "It",
        "year": 2017,
        "media_type": "movie",
        "season": 0,
        "episodes": [],
        "unit_keys": [
          "movie"
        ],
        "absolute_episode": 0,
        "is_anime": false,
        "confidence": 0.9,
        "reason": "clean_movie_with_year",
        "requires_review": false,
        "quality": {
          "resolution": "1080p",
          "source": "Bluray",
          "codec": "H264",
          "hdr": "",
          "audio": "",
          "edition": "",
          "group": ""
        }
      }
    },
    {
      "id": "chill-p-1700",
      "source": "chill-parser-original",
      "input": "Critical Role S01E01 1080p WEB x264",
      "review_note": "Previously excluded title-unasserted but known TV/web series, explicit S01E01. Preserve negative Source empty; Critical must not manufacture CR platform. Generic WEB leaves source unknown.",
      "expected": {
        "title": "Critical Role",
        "year": 0,
        "media_type": "tv",
        "season": 1,
        "episodes": [
          1
        ],
        "unit_keys": [
          "S01E01"
        ],
        "absolute_episode": 0,
        "is_anime": false,
        "confidence": 0.95,
        "reason": "standard_season_episode",
        "requires_review": false,
        "quality": {
          "resolution": "1080p",
          "source": "",
          "codec": "H264",
          "hdr": "",
          "audio": "",
          "edition": "",
          "group": ""
        }
      }
    },
    {
      "id": "chill-f-172",
      "source": "chill-parser-original",
      "input": "Widow's Bay (2026) s01e04 [Mkv - 1080p H264 - MultiLang Aac 2 0 - MultiSubs]",
      "review_note": "Known series, exact possessive spelling, year 2026 and S01E04 explicit. Original only selected Group empty; bracket is technical metadata, not group. Mkv/MultiLang/Aac/MultiSubs do not establish source or audio measurement.",
      "expected": {
        "title": "Widow's Bay",
        "year": 2026,
        "media_type": "tv",
        "season": 1,
        "episodes": [
          4
        ],
        "unit_keys": [
          "S01E04"
        ],
        "absolute_episode": 0,
        "is_anime": false,
        "confidence": 0.95,
        "reason": "standard_season_episode",
        "requires_review": false,
        "quality": {
          "resolution": "1080p",
          "source": "",
          "codec": "H264",
          "hdr": "",
          "audio": "",
          "edition": "",
          "group": ""
        }
      }
    },
    {
      "id": "chill-f-178",
      "source": "chill-parser-original",
      "input": "Widow s Bay (2026) s01e04 [Mkv  1080p H264  MultiLang Aac 2 0  MultiSubs]",
      "review_note": "Distinct exact handwritten filename for known series. Retain literal collapsed punctuation, no speculative apostrophe restoration. Same explicit year and S01E04; negative original Group empty must survive technical bracket.",
      "expected": {
        "title": "Widow s Bay",
        "year": 2026,
        "media_type": "tv",
        "season": 1,
        "episodes": [
          4
        ],
        "unit_keys": [
          "S01E04"
        ],
        "absolute_episode": 0,
        "is_anime": false,
        "confidence": 0.95,
        "reason": "standard_season_episode",
        "requires_review": false,
        "quality": {
          "resolution": "1080p",
          "source": "",
          "codec": "H264",
          "hdr": "",
          "audio": "",
          "edition": "",
          "group": ""
        }
      }
    },
    {
      "id": "chill-f-184",
      "source": "chill-parser-original",
      "input": "WidowS Bay S01e04 [1080p Ita Eng Spa HEVC10 SubS] byMe7alh [MIRCrew]",
      "review_note": "Distinct known-series filename; literal WidowS spelling, no year in input. Full S01E04 explicit; HEVC10 states HEVC plus bit depth, not HDR. Original bracket MIRCrew publisher is positively asserted; byMe7alh not a second group.",
      "expected": {
        "title": "WidowS Bay",
        "year": 0,
        "media_type": "tv",
        "season": 1,
        "episodes": [
          4
        ],
        "unit_keys": [
          "S01E04"
        ],
        "absolute_episode": 0,
        "is_anime": false,
        "confidence": 0.95,
        "reason": "standard_season_episode",
        "requires_review": false,
        "quality": {
          "resolution": "1080p",
          "source": "",
          "codec": "HEVC",
          "hdr": "",
          "audio": "",
          "edition": "",
          "group": "MIRCrew"
        }
      }
    },
    {
      "id": "chill-f-192",
      "source": "chill-parser-original",
      "input": "Widows Bay S01E04 Beach Reads 2160p ATVP WEB-DL DD 5 1 Atmos H 265-playWEB",
      "review_note": "Known series, Beach Reads is episode subtitle following unambiguous S01E04. No inferred year. WEB-DL/H 265 and -playWEB explicit; ATVP platform and DD Atmos source labels do not become measured audio or HDR.",
      "expected": {
        "title": "Widows Bay",
        "year": 0,
        "media_type": "tv",
        "season": 1,
        "episodes": [
          4
        ],
        "unit_keys": [
          "S01E04"
        ],
        "absolute_episode": 0,
        "is_anime": false,
        "confidence": 0.95,
        "reason": "standard_season_episode",
        "requires_review": false,
        "quality": {
          "resolution": "2160p",
          "source": "WEB-DL",
          "codec": "HEVC",
          "hdr": "",
          "audio": "",
          "edition": "",
          "group": "playWEB"
        }
      }
    },
    {
      "id": "chill-f-215",
      "source": "chill-parser-original",
      "input": "56 дней / 56 Days / S1E1-8 of 8 (Шена Стейн, Алетеа Джонс) [2026, США, триллер, драма, мелодрама, криминал, детектив, HEVC, HDR10+, WEB-DL 2160p] 3 x MVO (Дубляжная, LE-Production, Dragon Money Studio) + Original + Sub (Eng)",
      "review_note": "Known series; bilingual title is before the explicit S1E1-8 of 8 declaration. Original scalar Season=1, Episode=1, EpisodeEnd=8 is a full interval, never just first episode. Manually enumerate all eight episodes and keys. Explicit metadata year 2026, HEVC/HDR10+/WEB-DL/2160p; slash title must not erase coverage. No audio/group promotion.",
      "expected": {
        "title": "56 дней / 56 Days",
        "year": 2026,
        "media_type": "tv",
        "season": 1,
        "episodes": [
          1,
          2,
          3,
          4,
          5,
          6,
          7,
          8
        ],
        "unit_keys": [
          "S01E01",
          "S01E02",
          "S01E03",
          "S01E04",
          "S01E05",
          "S01E06",
          "S01E07",
          "S01E08"
        ],
        "absolute_episode": 0,
        "is_anime": false,
        "confidence": 0.95,
        "reason": "multi_episode_range",
        "requires_review": false,
        "quality": {
          "resolution": "2160p",
          "source": "WEB-DL",
          "codec": "HEVC",
          "hdr": "HDR10+",
          "audio": "",
          "edition": "",
          "group": ""
        }
      }
    },
    {
      "id": "chill-f-246",
      "source": "chill-parser-original",
      "input": "You and I Are Polar Opposites S01E11 Class Trip Part 2 1080p CR WEB DL AAC2 0 H 264 VARYG (Seihantai na Kimi to Boku  Multi Subs)",
      "review_note": "Known series, original scalar Episode=11 and Part=2. Class Trip Part 2 is episode subtitle, not permission to invent half-unit or infer absolute numbering. WEB DL/H 264 explicit in input; CR/Edition retained raw; undelimited VARYG not promoted.",
      "expected": {
        "title": "You and I Are Polar Opposites",
        "year": 0,
        "media_type": "tv",
        "season": 1,
        "episodes": [
          11
        ],
        "unit_keys": [
          "S01E11"
        ],
        "absolute_episode": 0,
        "is_anime": false,
        "confidence": 0.95,
        "reason": "standard_season_episode",
        "requires_review": false,
        "quality": {
          "resolution": "1080p",
          "source": "WEB-DL",
          "codec": "H264",
          "hdr": "",
          "audio": "",
          "edition": "",
          "group": ""
        }
      }
    },
    {
      "id": "chill-f-287",
      "source": "chill-parser-original",
      "input": "Roman Empire By Train With Alice Roberts S01E06 720p HDTV H264 JFF EZTV",
      "review_note": "Known documentary episode, full scalar original S01E06. Space-separated JFF EZTV is intentionally original selected Group empty; do not promote either token. 720p HDTV H264 explicit; no inferred year.",
      "expected": {
        "title": "Roman Empire By Train With Alice Roberts",
        "year": 0,
        "media_type": "tv",
        "season": 1,
        "episodes": [
          6
        ],
        "unit_keys": [
          "S01E06"
        ],
        "absolute_episode": 0,
        "is_anime": false,
        "confidence": 0.95,
        "reason": "standard_season_episode",
        "requires_review": false,
        "quality": {
          "resolution": "720p",
          "source": "HDTV",
          "codec": "H264",
          "hdr": "",
          "audio": "",
          "edition": "",
          "group": ""
        }
      }
    }
  ]
}
`
