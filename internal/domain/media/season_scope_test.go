package media_test

import (
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"testing"
)

// Original MIT season assertions are partial safety evidence, not episode lists.
func TestSeasonScopeCannotCertifyOneMovie(t *testing.T) {
	for _, raw := range []string{
		"Mr.Robot.S01.PROPER.VOSTFR.720p.WEB-DL.DD5.1.H264-ARK01",
		"Bosch S05 2160p AMZN WEBRip DD+5 1 x264-AJP69",
		"The Boys S01 2160p WEB-DL DDP5 1 HDR HEVC-NEOLUTiON",
		"Fixture.2020.S01.1080p.BluRay.x264.{tmdb-550}.mkv",
		"Fixture.2020.Season.1.1080p.BluRay.x264.{tmdb-550}.mkv",
	} {
		got, old := media.RecognizeRelease(raw), media.Recognize(raw)
		if !got.RequiresReview || got.MediaType != media.TypeTV || got.Confidence != 0 || len(got.UnitKeys) != 0 || len(got.Episodes) != 0 || got.AbsoluteEpisode != 0 {
			t.Errorf("season gained movie or inferred episode authority %q: %+v", raw, got)
		}
		if !old.RequiresReview || old.Confidence != 0 || old.Identity != nil {
			t.Errorf("legacy season acquired identity %q: %+v", raw, old)
		}
		if media.MatchesAutomaticMovie(raw, "Fixture", 2020, map[string]string{"tmdb.movie": "550"}) {
			t.Errorf("season gained automatic movie authority %q", raw)
		}
	}
}

func TestSeasonScopeDoesNotRemoveCanonicalEpisodeOrOrdinaryMovie(t *testing.T) {
	for _, raw := range []string{"Fixture.S01E01.1080p.BluRay.x264.mkv", "Fixture.S00E01.1080p.BluRay.x264.mkv", "Season.of.the.Witch.2011.1080p.BluRay.x264.mkv", "Seasoning.2020.1080p.BluRay.x264.mkv"} {
		got := media.RecognizeRelease(raw)
		if got.RequiresReview || len(got.UnitKeys) != 1 {
			t.Errorf("valid literal became unresolved %q: %+v", raw, got)
		}
	}
}
