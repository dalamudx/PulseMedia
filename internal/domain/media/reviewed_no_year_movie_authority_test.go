package media_test

import (
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
)

// These two literal originals are independently labelled in the pinned title table.
// The explicit-year positive control is a synthetic rule probe, not a gold input.
func TestReviewedNoYearQualityDoesNotAuthorizeAutomaticMovie(t *testing.T) {
	for _, tc := range []struct {
		raw, title   string
		year         int
		knownQuality bool
	}{
		{"Get.Him.To.The.Greek.UNRATED.FRENCH.720p.BluRay.x264-NERDHD", "Get Him To The Greek", 2010, true},
		{"No.Country.for.Old.Men.DVDRip.XviD-DiAMOND", "No Country for Old Men", 2007, false},
	} {
		t.Run(tc.title, func(t *testing.T) {
			got := media.RecognizeRelease(tc.raw)
			if got.Title != tc.title || got.Year != 0 || !got.RequiresReview || len(got.UnitKeys) != 0 {
				t.Fatalf("missing work-year evidence acquired identity: %+v", got)
			}
			if quality.KnownProfile(got.Quality) != tc.knownQuality {
				t.Fatalf("identity and quality evidence were conflated: %+v", got)
			}
			if media.MatchesAutomaticMovie(tc.raw, tc.title, tc.year, nil) {
				t.Fatal("quality/title supplied the missing movie year or coverage")
			}
		})
	}
	if !media.MatchesAutomaticMovie("Get.Him.To.The.Greek.2010.720p.BluRay.x264-NERDHD", "Get Him To The Greek", 2010, nil) {
		t.Fatal("control with actual title/year evidence unexpectedly refused")
	}
}
