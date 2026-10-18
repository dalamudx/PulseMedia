package media_test

import (
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"testing"
)

func TestExplicitAllSeasonIntervalsCannotBecomeSingleUnits(t *testing.T) {
	for _, input := range []string{
		"House MD All Seasons (1-8) 720p Ultra-Compressed",
		"Breaking Bad Complete (All Seasons 1 to 5) -nelly-",
		"The Golden Girls, Seasons 1 thru 7 Complete, X264",
		"House MD All Seasons (1-8) 2020 {tmdb-123}/Movie.2020.mkv",
		"House MD All Seasons (1-8)/Show.S01E01.720p.mkv",
		"House MD All Seasons (1-8)/[Group] Show - 01 [720p].mkv",
	} {
		got := media.RecognizeRelease(input)
		if !got.RequiresReview || got.Confidence != 0 || got.Season != 0 || got.AbsoluteEpisode != 0 || len(got.Episodes) != 0 || len(got.UnitKeys) != 0 {
			t.Errorf("partial collection scope: %q => %+v", input, got)
		}
		old := media.Recognize(input)
		if !old.RequiresReview || old.Confidence != 0 || old.Identity != nil {
			t.Errorf("legacy collection identity: %q => %+v", input, old)
		}
	}
	for _, input := range []string{"All Seasons Hotel.2020.1080p.mkv", "Season of the Witch.2011.1080p.mkv"} {
		got := media.RecognizeRelease(input)
		if got.RequiresReview || got.Confidence != 0.9 || len(got.UnitKeys) != 1 {
			t.Errorf("ordinary title changed: %s => %+v", input, got)
		}
	}
}

func TestAllSeasonTitleAndCompressionBoundariesStayBounded(t *testing.T) {
	got := media.RecognizeRelease("All Seasons.2020.1080p.mkv")
	if got.Reason != "episode_coverage_unresolved" || got.Confidence != 0 || len(got.UnitKeys) != 0 {
		t.Fatal("existing unsupported season-year boundary changed", got)
	}
	for _, item := range []struct{ input, group string }{
		{"Movie.2020.1080p-Ultra-Compressed.mkv", ""},
		{"Movie.2020.1080p-Compressed.mkv", "Compressed"},
	} {
		if got := media.RecognizeRelease(item.input); got.Quality.Group != item.group {
			t.Fatalf("whole technical phrase/group boundary: %+v", got)
		}
	}
}
