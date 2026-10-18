package media_test

import (
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"testing"
)

func TestExplicitCollectionTailCannotAcquireOrUseMovieID(t *testing.T) {
	for _, raw := range []string{
		"Fixture.2020.[Complete].1080p.BluRay.x264.{tmdb-550}.mkv",
		"Fixture.2020.Collection [DB 1080p].{tmdb-550}.mkv",
		"Fixture.Complete.13.Movie.Collection.2000-2020.1080p.H264.{tmdb-550}.mkv",
		"Fixture.S01E01.[Complete].1080p.x264.mkv",
		"[Unknown] Fixture [v2] [R2J] [VFR] [Dual Audio] [Complete] [x264]",
	} {
		got, old := media.RecognizeRelease(raw), media.Recognize(raw)
		if got.RawName != raw || got.Confidence != 0 || !got.RequiresReview || len(got.UnitKeys) != 0 || len(got.Episodes) != 0 || !old.RequiresReview || old.Identity != nil || old.Confidence != 0 {
			t.Errorf("explicit collection gained authority or lost raw evidence: %q: %+v / %+v", raw, got, old)
		}
		if media.MatchesAutomaticMovie(raw, "Fixture", 2020, map[string]string{"tmdb.movie": "550"}) {
			t.Errorf("ID bypassed package refusal: %s", raw)
		}
	}
}

func TestCollectionTailKeepsOrdinaryTitlesAndUnknownMetadata(t *testing.T) {
	for _, raw := range []string{
		"The.Collection.2012.1080p.BluRay.x264.mkv",
		"Collection.2012.1080p.BluRay.x264.mkv",
		"Complete.2012.1080p.BluRay.x264.mkv",
		"The.Complete.2020.1080p.BluRay.x264.mkv",
		"Stamp.Collection.2020.1080p.BluRay.x264.mkv",
		"Fixture.2020.[Incomplete].1080p.BluRay.x264.mkv",
		"Fixture.2020.Collection [Unknown].1080p.BluRay.x264.mkv",
		"Fixture.[Complete].of.History.2020.1080p.BluRay.x264.mkv",
		"Fixture [Complete] Edition.2020.1080p.BluRay.x264.mkv",
	} {
		got := media.RecognizeRelease(raw)
		if got.RequiresReview || got.Confidence != 0.9 || len(got.UnitKeys) != 1 || got.UnitKeys[0] != "movie" {
			t.Errorf("ordinary title became package: %s: %+v", raw, got)
		}
	}
	for _, raw := range []string{"[Complete] Fixture.2020.1080p.x264.mkv", "The.[Complete].2020.1080p.x264.mkv"} {
		if got := media.RecognizeRelease(raw); got.Reason == "multi_unit_collection" {
			t.Errorf("marker without work prefix became package: %+v", got)
		}
	}
	got := media.RecognizeRelease("[Unknown] Fixture [v2] [R2J] [VFR] [Dual Audio] [Complete] [x264]")
	if got.Title != "Unknown] Fixture" || got.Quality.Group != "" {
		t.Errorf("unknown publisher removed or certified: %+v", got)
	}
}
