package media_test

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
)

// This view preserves the reviewed labels before any persisted-profile evidence
// filtering. The shared full-corpus test remains the closed acceptance schema.
type moistariReviewedQuality quality.Profile

type moistariReviewedContract struct {
	Title          string                  `json:"title"`
	MediaType      media.Type              `json:"media_type"`
	Season         int                     `json:"season"`
	Episodes       []int                   `json:"episodes"`
	UnitKeys       []string                `json:"unit_keys"`
	Confidence     float64                 `json:"confidence"`
	RequiresReview bool                    `json:"requires_review"`
	Reason         string                  `json:"reason"`
	Quality        moistariReviewedQuality `json:"quality"`
}

func validateMoistariSourceRebuttals(original *corpusEntry, raw json.RawMessage) error {
	var want moistariReviewedContract
	if err := json.Unmarshal(raw, &want); err != nil {
		return err
	}
	return validateMoistariContract(original, want)
}

// Mapping retains every original assertion. Only exact, independently asserted
// equivalents participate in shared source rebuttals; no range is expanded.
func moistariExpectedLabels(labels map[string]json.RawMessage) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for k, v := range labels {
		out["moistari_"+k] = v
	}
	for _, k := range []string{"title", "year", "type", "group"} {
		if v, ok := labels[k]; ok {
			out[k] = v
		}
	}
	if raw := string(labels["type"]); raw == `"episode"` || raw == `"series"` {
		out["type"] = json.RawMessage(`"episode"`)
	}
	for from, to := range map[string]string{"resolution": "screen_size", "series": "season", "episode": "episode"} {
		if v, ok := labels[from]; ok {
			out[to] = v
		}
	}
	if _, ok := labels["seriesEpisodes"]; ok {
		if keys, err := moistariDeclaredKeys(&corpusEntry{Expected: out}); err == nil {
			seasons, episodes := []int{}, []int{}
			seenSeason, seenEpisode := map[int]bool{}, map[int]bool{}
			for _, key := range keys {
				var season, episode int
				_, _ = fmt.Sscanf(key, "S%dE%d", &season, &episode)
				if !seenSeason[season] {
					seasons = append(seasons, season)
					seenSeason[season] = true
				}
				if !seenEpisode[episode] {
					episodes = append(episodes, episode)
					seenEpisode[episode] = true
				}
			}
			sort.Ints(seasons)
			sort.Ints(episodes)
			out["season"], _ = json.Marshal(seasons)
			out["episode"], _ = json.Marshal(episodes)
		}
	}
	var source, codec string
	_ = json.Unmarshal(labels["source"], &source)
	_ = json.Unmarshal(labels["codec"], &codec)
	switch source {
	case "BluRay", "UHD.BluRay", "BDRiP", "MBluRay":
		out["source"] = json.RawMessage(`"Blu-ray"`)
	case "WEB-DL", "UHD.WEB-DL", "WEBRiP", "WEB":
		out["source"] = json.RawMessage(`"Web"`)
	case "HDTV":
		out["source"] = json.RawMessage(`"HDTV"`)
	case "DVD", "DVDRiP":
		out["source"] = json.RawMessage(`"DVD"`)
	case "TS":
		out["source"] = json.RawMessage(`"Telesync"`)
	case "CAMRiP":
		out["source"] = json.RawMessage(`"Camera"`)
	}
	switch codec {
	case "x264", "x264.HQ", "x264 dxva", "H.264", "AVC":
		out["video_codec"] = json.RawMessage(`"H.264"`)
	case "x265", "H.265", "HEVC", "HEVC x265", "x265 HEVC":
		out["video_codec"] = json.RawMessage(`"H.265"`)
	case "XViD":
		out["video_codec"] = json.RawMessage(`"Xvid"`)
	}
	return out
}

func moistariOriginalString(original *corpusEntry, field string) string {
	var value string
	_ = json.Unmarshal(original.Expected["moistari_"+field], &value)
	return value
}

func moistariDeclaredKeys(original *corpusEntry) ([]string, error) {
	if full := moistariOriginalString(original, "seriesEpisodes"); full != "" {
		token := regexp.MustCompile(`^S([0-9]{1,3})E([0-9]{1,4})$`)
		seen := map[string]bool{}
		keys := []string{}
		for _, raw := range strings.Fields(full) {
			m := token.FindStringSubmatch(raw)
			if m == nil {
				return nil, fmt.Errorf("unsupported original unit token %q", raw)
			}
			season, _ := strconv.Atoi(m[1])
			episode, _ := strconv.Atoi(m[2])
			if episode <= 0 {
				return nil, fmt.Errorf("nonpositive original episode")
			}
			key := fmt.Sprintf("S%02dE%02d", season, episode)
			if seen[key] {
				return nil, fmt.Errorf("duplicate original unit")
			}
			seen[key] = true
			keys = append(keys, key)
		}
		sort.Strings(keys)
		return keys, nil
	}
	var season, episode int
	if raw, ok := original.Expected["moistari_episode"]; !ok || json.Unmarshal(raw, &episode) != nil || episode <= 0 {
		return nil, fmt.Errorf("no independently declared episode")
	}
	raw, ok := original.Expected["moistari_series"]
	if ok {
		if json.Unmarshal(raw, &season) != nil || season < 0 {
			return nil, fmt.Errorf("invalid original season")
		}
	} else {
		// Zero is omitted by the upstream YAML writer. It may be recovered only
		// from explicit S00Exx syntax, never from a default or an absolute number.
		explicit := regexp.MustCompile(fmt.Sprintf(`(?i)(?:^|[^a-z0-9])S00E0*%d(?:[^0-9]|$)`, episode))
		if !explicit.MatchString(original.Name) {
			return nil, fmt.Errorf("no independently bound season")
		}
	}
	return []string{fmt.Sprintf("S%02dE%02d", season, episode)}, nil
}

func validateMoistariContract(original *corpusEntry, w moistariReviewedContract) error {
	kind := moistariOriginalString(original, "type")
	switch kind {
	case "movie":
		if w.MediaType != media.TypeMovie {
			return fmt.Errorf("original movie type changed")
		}
	case "episode", "series":
		if w.MediaType != media.TypeTV {
			return fmt.Errorf("original series or episode fell back to movie")
		}
	default:
		return fmt.Errorf("non-video original type cannot enter this source's video review")
	}
	if w.Quality.Audio != "" || w.Quality.Edition != "" {
		return fmt.Errorf("unsupported original audio or edition became a fact")
	}
	if len(w.UnitKeys) > 0 && w.MediaType == media.TypeTV {
		want, err := moistariDeclaredKeys(original)
		if err != nil {
			return err
		}
		got := append([]string(nil), w.UnitKeys...)
		sort.Strings(got)
		if strings.Join(want, " ") != strings.Join(got, " ") {
			return fmt.Errorf("complete original unit set changed or truncated")
		}
	}
	if len(w.UnitKeys) > 0 && kind == "series" {
		return fmt.Errorf("season-only source gained canonical unit coverage")
	}
	if full := moistariOriginalString(original, "seriesEpisodes"); full != "" && len(w.UnitKeys) == 0 && (w.Confidence != 0 || !w.RequiresReview || len(w.Episodes) != 0) {
		return fmt.Errorf("unresolved full unit list retained partial authority")
	}
	var group string
	if raw, ok := original.Expected["moistari_group"]; ok {
		if json.Unmarshal(raw, &group) != nil || w.Quality.Group != group {
			return fmt.Errorf("original release group changed")
		}
	}
	if source := moistariOriginalString(original, "source"); source != "" && w.Quality.Source != "" {
		valid := false
		switch source {
		case "BluRay", "UHD.BluRay", "MBluRay":
			valid = w.Quality.Source == quality.SourceBluray || w.Quality.Source == quality.SourceRemux
		case "BDRiP":
			valid = w.Quality.Source == quality.SourceBluray
		case "WEB-DL", "UHD.WEB-DL":
			valid = w.Quality.Source == quality.SourceWEBDL
		case "WEBRiP":
			valid = w.Quality.Source == quality.SourceWEBRip
		case "HDTV":
			valid = w.Quality.Source == quality.SourceHDTV
		}
		if !valid {
			return fmt.Errorf("original source does not support asserted source fact")
		}
	}
	if codec := moistariOriginalString(original, "codec"); codec != "" && w.Quality.Codec != "" {
		valid := false
		switch codec {
		case "x264", "x264.HQ", "x264 dxva", "H.264", "AVC":
			valid = w.Quality.Codec == quality.CodecH264
		case "x265", "H.265", "HEVC", "HEVC x265", "x265 HEVC":
			valid = w.Quality.Codec == quality.CodecHEVC
		}
		if !valid {
			return fmt.Errorf("unsupported or contradictory original codec")
		}
	}
	if hdr := moistariOriginalString(original, "hdr"); hdr != "" && w.Quality.HDR != "" {
		valid := false
		switch hdr {
		case "DV", "DV HDR", "DV HDR10+":
			valid = w.Quality.HDR == quality.HDRDolbyVision
		case "HDR", "HDR10":
			valid = w.Quality.HDR == quality.HDR10
		case "HDR10+":
			valid = w.Quality.HDR == quality.HDR10Plus
		}
		if !valid {
			return fmt.Errorf("unsupported or contradictory original HDR")
		}
	}
	return nil
}

func TestMoistariOriginalCoverageAndQualityRebuttals(t *testing.T) {
	labels := map[string]json.RawMessage{"title": json.RawMessage(`"Independent Scope"`), "type": json.RawMessage(`"episode"`), "series": json.RawMessage(`1`), "episode": json.RawMessage(`1`), "seriesEpisodes": json.RawMessage(`"S01E01 S01E03"`), "source": json.RawMessage(`"WEB-DL"`), "codec": json.RawMessage(`"H.264"`), "hdr": json.RawMessage(`"DV"`), "group": json.RawMessage(`"LAZY"`)}
	original := &corpusEntry{Name: "Independent.Scope.S01E01E03.WEB-DL.H.264.DV-LAZY", Expected: moistariExpectedLabels(labels)}
	base := moistariReviewedContract{Title: "Independent Scope", MediaType: media.TypeTV, Season: 1, Episodes: []int{1, 3}, UnitKeys: []string{"S01E01", "S01E03"}, Confidence: 1, Reason: "standard_season_episode"}
	base.Quality.Source = quality.SourceWEBDL
	base.Quality.Codec = quality.CodecH264
	base.Quality.HDR = quality.HDRDolbyVision
	base.Quality.Group = "LAZY"
	if err := validateMoistariContract(original, base); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		mutate func(*moistariReviewedContract)
	}{
		{"first endpoint", func(w *moistariReviewedContract) { w.UnitKeys = []string{"S01E01"}; w.Episodes = []int{1} }},
		{"last endpoint", func(w *moistariReviewedContract) { w.UnitKeys = []string{"S01E03"}; w.Episodes = []int{3} }},
		{"invented middle", func(w *moistariReviewedContract) {
			w.UnitKeys = []string{"S01E01", "S01E02", "S01E03"}
			w.Episodes = []int{1, 2, 3}
		}},
		{"different season", func(w *moistariReviewedContract) { w.UnitKeys = []string{"S02E01", "S02E03"}; w.Season = 2 }},
		{"movie fallback", func(w *moistariReviewedContract) { w.MediaType = media.TypeMovie; w.UnitKeys = []string{"movie"} }},
		{"unresolved but confident", func(w *moistariReviewedContract) { w.UnitKeys = nil; w.Episodes = nil; w.RequiresReview = true }},
		{"wrong codec", func(w *moistariReviewedContract) { w.Quality.Codec = quality.CodecHEVC }},
		{"wrong source", func(w *moistariReviewedContract) { w.Quality.Source = quality.SourceWEBRip }},
		{"wrong HDR", func(w *moistariReviewedContract) { w.Quality.HDR = quality.HDR10 }},
		{"wrong group", func(w *moistariReviewedContract) { w.Quality.Group = "Other" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := base
			c.mutate(&w)
			if validateMoistariContract(original, w) == nil {
				t.Fatal("changed independent source evidence admitted")
			}
		})
	}
	t.Run("zero confidence full refusal", func(t *testing.T) {
		w := base
		w.UnitKeys = nil
		w.Episodes = nil
		w.Confidence = 0
		w.RequiresReview = true
		if err := validateMoistariContract(original, w); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("full cross season list", func(t *testing.T) {
		labels["seriesEpisodes"] = json.RawMessage(`"S01E01 S02E03"`)
		o := &corpusEntry{Name: original.Name, Expected: moistariExpectedLabels(labels)}
		w := base
		w.UnitKeys = []string{"S01E01", "S02E03"}
		if err := validateMoistariContract(o, w); err != nil {
			t.Fatal(err)
		}
		w.UnitKeys = []string{"S01E01"}
		if validateMoistariContract(o, w) == nil {
			t.Fatal("cross-season truncation admitted")
		}
	})
	t.Run("no inferred default season", func(t *testing.T) {
		delete(labels, "seriesEpisodes")
		delete(labels, "series")
		o := &corpusEntry{Name: "Independent Scope - 01", Expected: moistariExpectedLabels(labels)}
		if _, err := moistariDeclaredKeys(o); err == nil {
			t.Fatal("default season inferred")
		}
		o.Name = "Independent.Scope.S00E01"
		if keys, err := moistariDeclaredKeys(o); err != nil || len(keys) != 1 || keys[0] != "S00E01" {
			t.Fatalf("explicit zero-season evidence lost: %v %v", keys, err)
		}
	})
}
