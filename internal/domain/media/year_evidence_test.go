package media_test

import (
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"testing"
)

func TestDistinctYearsHoldAndExplicitIDDoesNotBypassReview(t *testing.T) {
	for _, raw := range []string{"Fixture.1984.2020.1080p.WEB-DL.mkv", "Fixture_2019_(1983)_REMASTERED.mkv", "Fixture 2012 (2009) {tmdb-550}.mkv", "Fixture.2049.2017.S01E02.mkv"} {
		r := media.RecognizeRelease(raw)
		if !r.RequiresReview || r.Confidence != 0 || r.Year != 0 || len(r.UnitKeys) != 0 || r.Reason != "ambiguous_media_year" {
			t.Fatal("ambiguous year authorized coverage", raw, r)
		}
		if media.MatchesAutomaticMovie(raw, "Fixture", 2020, map[string]string{"tmdb.movie": "550"}) {
			t.Fatal("explicit identity bypassed review", raw)
		}
	}
}
func TestExternalIdentityNumberIsNotCalendarEvidence(t *testing.T) {
	r := media.RecognizeRelease("Fixture_2020_{tmdb-2005}.mkv")
	if r.RequiresReview || r.Year != 2020 || r.Reason != "has_external_id" {
		t.Fatal("identity UUID/date confusion", r)
	}
}

func TestUnderscoreAndRepeatedUniqueYearRemainEvidence(t *testing.T) {
	r := media.RecognizeRelease("Fixture_Show_2005.8x12.720p_hdtv_x264.mkv")
	if r.RequiresReview || r.Year != 2005 || len(r.UnitKeys) != 1 || r.UnitKeys[0] != "S08E12" {
		t.Fatal(r)
	}
	r = media.RecognizeRelease("Fixture_2020_2020_1080p.mkv")
	if r.RequiresReview || r.Year != 2020 {
		t.Fatal("duplicate same evidence is not conflict", r)
	}
}
