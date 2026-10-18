package media_test

import (
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"testing"
)

func TestVirenFullRawScope(t *testing.T) {
	for _, raw := range []string{
		"[Erai-raws] Unseen Comet 3rd Season - 02 [1080p].mkv",
		"Unseen Season 3 - 02/Feature.2024.{tmdb-123}.mkv",
		"Feature.2024.{tmdb-123}.mkv/Unseen Season 3 - 02",
		"Unseen Season 3 - 02/Show.S01E01.1080p.mkv",
		"Unseen Season 3 - 02/Show.S01E01E02.1080p.mkv",
		"Show.S01E01.2024.1080p/Unseen Season 3 - 02",
		"Unseen Season 3 - 02/Feature.2024.03.15.mkv",
	} {
		r, m := media.RecognizeRelease(raw), media.Recognize(raw)
		if !r.RequiresReview || r.Confidence != 0 || r.Year != 0 || r.Season != 0 || len(r.Episodes) != 0 || len(r.UnitKeys) != 0 || r.AbsoluteEpisode != 0 || m.Identity != nil || !m.RequiresReview || m.Confidence != 0 {
			t.Fatalf("raw scope authority leaked %q: %+v / %+v", raw, r, m)
		}
	}
}
func TestVirenUnknownAndNonVideo(t *testing.T) {
	for _, raw := range []string{
		"[Unknown] Unseen Comet - Season 3 46.mkv",
		"[Unknown] Unseen op.Comet (WEB 1080p x264 AAC) [Dual-Audio]",
		"[Unknown] Unseen Comet [Movie][BDRip][1080P]",
		"未知 Unseen.Comet.2024.1080p.WEB-DL.mkv",
	} {
		r := media.RecognizeRelease(raw)
		if r.Title == "Unseen Comet" || r.Title == "Unseen op.Comet" {
			t.Fatalf("unknown prefix stripped %q: %+v", raw, r)
		}
	}
	for _, raw := range []string{"Unseen Comet S3 OVA - 07.srt", "Unseen.Season.3-02.2024.srt", "Unseen.2024.SUBPACK.1080p.BluRay.x264-GRP.srt"} {
		r := media.RecognizeRelease(raw)
		if r.Reason != "non_video_file" || len(r.UnitKeys) != 0 || r.AbsoluteEpisode != 0 {
			t.Fatalf("non-video return overwritten %+v", r)
		}
	}
}
func TestVirenUnsupportedStaticVocabulary(t *testing.T) {
	for _, raw := range []string{"Film.2024.TeleCine.1080p.mkv", "Film.2024.DVD.1080p.mkv", "Film.2024.WEB-DLRip.720p.mkv"} {
		if media.RecognizeRelease(raw).Quality.Source != "" || media.Recognize(raw).Quality.Source != "" {
			t.Fatalf("unsupported source promoted: %s", raw)
		}
	}
	if validatePTTMainQualityRebuttals(map[string]json.RawMessage{"ptt_main_quality": json.RawMessage(`"WEB-DLRip"`)}, quality.Profile{}) == nil {
		t.Fatal("unaudited WEB-DLRip accepted")
	}

	for _, label := range []string{"TeleCine", "DVD"} {
		labels := map[string]json.RawMessage{"ptt_main_quality": json.RawMessage(`"` + label + `"`)}
		if e := validatePTTMainQualityRebuttals(labels, quality.Profile{}); e != nil {
			t.Fatal(e)
		}
		for _, source := range []quality.Source{quality.SourceBluray, quality.SourceRemux, quality.SourceWEBDL, quality.SourceWEBRip, quality.SourceHDTV} {
			if validatePTTMainQualityRebuttals(labels, quality.Profile{Source: source}) == nil {
				t.Fatalf("promoted %s to %s", label, source)
			}
		}
	}
}
