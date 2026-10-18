package media_test

import (
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"testing"
)

// Synthetic boundaries supplement fixed independent dictionary index16;
// these probes are excluded from the independent golden count.
func TestUniqueLeadingWorkYearSeparatesTitle(t *testing.T) {
	for _, raw := range []string{"2008 The Incredible Hulk Feature Film.mp4", "(2008) The Incredible Hulk Feature Film.mp4", "[2008] The Incredible Hulk Feature Film.mp4", "2008.The.Incredible.Hulk.Feature.Film.1080p.mkv"} {
		got := media.RecognizeRelease(raw)
		if got.Title != "The Incredible Hulk Feature Film" || got.Year != 2008 || got.MediaType != media.TypeMovie || got.RequiresReview || len(got.UnitKeys) != 1 {
			t.Fatalf("leading unique year: %+v", got)
		}
	}
}
func TestLeadingYearCannotResolveAmbiguityOrSeriesNumbers(t *testing.T) {
	for _, raw := range []string{"2008 Film 2009 1080p.mkv", "2001 A Space Odyssey 1968 1080p.mkv", "2008 Film.S01.COMPLETE.1080p.mkv", "2008 Film.2024-01-02.1080p.mkv"} {
		got := media.RecognizeRelease(raw)
		if !got.RequiresReview || len(got.UnitKeys) != 0 {
			t.Fatalf("leading year supplied unsafe authority: %+v", got)
		}
	}
	got := media.RecognizeRelease("1923.S01E01.1080p.WEB-DL.mkv")
	if got.Title != "1923" || got.Year != 0 || got.Season != 1 || len(got.Episodes) != 1 || got.Episodes[0] != 1 {
		t.Fatalf("numeric series identity lost: %+v", got)
	}
}
