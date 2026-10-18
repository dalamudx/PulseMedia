package media_test

import (
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"reflect"
	"slices"
	"testing"
)

func TestChillFrozenProbe(t *testing.T) {
	var corpus struct {
		Entries []struct {
			ID, Input string
			Expected  json.RawMessage
		}
	}
	if err := json.Unmarshal([]byte(chillFrozenContracts), &corpus); err != nil {
		t.Fatal(err)
	}
	for _, row := range corpus.Entries {
		r := media.RecognizeRelease(row.Input)
		l := media.Recognize(row.Input)
		q := r.Quality
		actual := map[string]any{"title": r.Title, "year": r.Year, "media_type": r.MediaType, "season": r.Season, "episodes": r.Episodes, "unit_keys": r.UnitKeys, "absolute_episode": r.AbsoluteEpisode, "is_anime": r.IsAnime, "confidence": r.Confidence, "requires_review": r.RequiresReview, "reason": r.Reason, "quality": map[string]any{"resolution": q.Resolution, "source": q.Source, "codec": q.Codec, "hdr": q.HDR, "audio": q.Audio, "edition": q.Edition, "group": q.Group}}
		b, _ := json.Marshal(actual)
		var a, w map[string]any
		_ = json.Unmarshal(b, &a)
		_ = json.Unmarshal(row.Expected, &w)
		for _, k := range []string{"episodes", "unit_keys"} {
			if a[k] == nil {
				a[k] = []any{}
			}
		}
		diff := []string{}
		for k, v := range w {
			if !reflect.DeepEqual(a[k], v) {
				diff = append(diff, k)
			}
		}
		slices.Sort(diff)
		oracle := validateChillComplete(row.Input, row.Expected)
		receipt := map[string]any{"id": row.ID, "diff": diff, "original_admitted": oracle == nil, "actual": a, "legacy": l}
		if oracle != nil {
			receipt["source_pending"] = oracle.Error()
		}
		b, _ = json.Marshal(receipt)
		t.Log("FROZEN " + string(b))
		if r.RawName != row.Input || l.RawName != row.Input || r.ParserVersion != media.EpisodeParserVersion {
			t.Fatal("raw/version changed", row.ID)
		}
	}
}

func TestChillUnseenLexicalAndScopeGuards(t *testing.T) {
	for _, raw := range []string{"Harbor Atlas S03E02 Episode 2 1080p WEB-DL H264-GRP", "Quiet Orchard S12E04 Episode 04 720p HDTV x264-Q", "Harbor Atlas S03E02 Episode 2 2024-01-02 1080p WEB-DL H264-GRP"} {
		r, l := media.RecognizeRelease(raw), media.Recognize(raw)
		if r.RequiresReview || r.Confidence != .95 || len(r.Episodes) != 1 || len(r.UnitKeys) != 1 || r.RawName != raw || l.RawName != raw {
			t.Fatalf("repeated scalar positive: %s %+v", raw, r)
		}
	}
	raw := "Harbor.039.s.Edge.2024.1080p.BluRay.x264-GRP"
	if r, l := media.RecognizeRelease(raw), media.Recognize(raw); r.Title != "Harbor's Edge" || l.Title != "Harbor's Edge" || r.Year != 2024 || r.RequiresReview || l.RequiresReview {
		t.Fatalf("apostrophe: %+v %+v", r, l)
	}
	for _, raw := range []string{"Harbor Atlas 2024 S02 1080p WEBRip x265-GRP", "Harbor Atlas 2024 Complete Series Seasons 1 to 4 1080p WEB x264 [a_b]"} {
		r, l := media.RecognizeRelease(raw), media.Recognize(raw)
		if !r.RequiresReview || r.Confidence != 0 || len(r.Episodes) != 0 || len(r.UnitKeys) != 0 || r.AbsoluteEpisode != 0 || r.RawName != raw || l.RawName != raw || !l.RequiresReview || l.Identity != nil || l.Confidence != 0 {
			t.Fatalf("scope acquired authority: %s %+v %+v", raw, r, l)
		}
	}
	for _, raw := range []string{
		"Harbor Atlas S03E02 Episode 3 1080p WEB-DL H264-GRP",
		"Harbor Atlas S03E02 Episode 2.5 1080p WEB-DL H264-GRP",
		"Harbor Atlas S03E02 Episode 2 of 8 1080p WEB-DL H264-GRP",
		"Harbor Atlas S03E02 Episode 2-3 1080p WEB-DL H264-GRP",
		"Harbor Atlas S03E02 Episode 2/3 1080p WEB-DL H264-GRP",
		"Harbor Atlas S03E02 Episode 2 E03 2024 {tmdb-123} 1080p WEB-DL H264-GRP",
		"Harbor Atlas 2024 2025 S02 1080p WEB-DL H264-GRP",
		"Harbor Atlas 2024 S02 {tmdb-123} 1080p WEB-DL H264-GRP",
		"Harbor Atlas S03E02 Episode 2 S04E03 1080p WEB-DL H264-GRP",
		"Harbor Atlas S03E02 Episode 2 E03 1080p WEB-DL H264-GRP",
		"Harbor Atlas S03E02 Episode 2 1x03 1080p WEB-DL H264-GRP",
		"Harbor Atlas S1E1-8 of 8 / Alias 2024 1080p WEB-DL H264 {tmdb-123}",
		"Harbor Atlas Season2 Complete / Alias S02E01 1080p WEB-DL H264 {tmdb-123}",
		"Harbor Atlas 2024-01-02 1080p WEB-DL H264",
	} {
		r, l := media.RecognizeRelease(raw), media.Recognize(raw)
		if !r.RequiresReview || r.Confidence != 0 || len(r.Episodes) != 0 || len(r.UnitKeys) != 0 || r.AbsoluteEpisode != 0 || r.RawName != raw || l.RawName != raw || !l.RequiresReview || l.Identity != nil || l.Confidence != 0 {
			t.Errorf("mixed/date/slash acquired authority: %s release=%+v legacy=%+v", raw, r, l)
		}
	}
	for _, raw := range []string{"Harbor Atlas 2024 1080p WEB-DL H264-GRP", "Harbor Atlas S02E03 1080p WEB-DL H264-GRP"} {
		r, l := media.RecognizeRelease(raw), media.Recognize(raw)
		if r.RequiresReview || r.RawName != raw || l.RawName != raw || r.Quality.Codec != "H264" || r.Quality.Source != "WEB-DL" {
			t.Fatalf("normal: %s %+v %+v", raw, r, l)
		}
	}
}
