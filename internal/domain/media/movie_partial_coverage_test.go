package media_test

import (
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"testing"
)

func TestMoviePartialSuffixCannotObtainCompleteCoverage(t *testing.T) {
	for _, raw := range []string{
		"Fixture.1999.1080p.WEB-DL.sample.x264.{tmdb-550}",
		"Fixture.1999.1080p.WEB-DL.first-half.x264.{tmdb-550}",
		"Fixture.1999.1080p.WEB-DL.trailer.x264.{tmdb-550}",
	} {
		r := media.RecognizeRelease(raw)
		if !r.RequiresReview || r.Confidence != 0 || len(r.UnitKeys) != 0 || r.Reason != "partial_movie_content" {
			t.Errorf("partial movie retained complete coverage: %+v", r)
		}
		legacy := media.Recognize(raw)
		if !legacy.RequiresReview || legacy.Confidence != 0 || legacy.Identity != nil {
			t.Errorf("legacy recognizer retained partial identity: %+v", legacy)
		}
		if media.MatchesAutomaticMovie(raw, "Fixture", 1999, map[string]string{"tmdb.movie": "550"}) {
			t.Error("movieID hid partial suffix")
		}
	}
	for _, raw := range []string{"Sample.1999.1080p.BluRay.x264", "Trailer.Park.1999.1080p.BluRay.x264"} {
		r := media.RecognizeRelease(raw)
		if r.RequiresReview || len(r.UnitKeys) != 1 || r.UnitKeys[0] != "movie" {
			t.Errorf("work title was mistaken for partial suffix: %+v", r)
		}
	}
}
