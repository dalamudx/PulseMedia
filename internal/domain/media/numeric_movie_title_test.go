package media_test

import (
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"testing"
)

// Synthetic controls complement fixed MIT title2012; never counted as gold.
func TestNumericMovieTitleCannotManufactureReleaseYear(t *testing.T) {
	for _, raw := range []string{"2012.AC3.720p.BluRay.x264-XXL", "1984.1080p.BluRay.x264.mkv", "[2012].1080p.WEB-DL.mkv"} {
		got := media.RecognizeRelease(raw)
		if got.Year != 0 || !got.RequiresReview || len(got.UnitKeys) != 0 || got.Reason != "title_only_without_year" {
			t.Fatalf("numeric title became work year: %+v", got)
		}
	}
	got := media.RecognizeRelease("2012.AC3.720p.BluRay.x264-XXL")
	if got.Title != "2012" || got.Confidence != 0.6 {
		t.Fatalf("numeric title lost: %+v", got)
	}
	for _, raw := range []string{"2012.2009.1080p.BluRay.mkv", "1984.2010.WEB-DL.mkv", "2012.S01.COMPLETE.1080p.mkv", "2012.2024-01-02.1080p.mkv"} {
		got := media.RecognizeRelease(raw)
		if !got.RequiresReview || len(got.UnitKeys) != 0 {
			t.Fatalf("numeric title bypassed ambiguity or scope: %+v", got)
		}
	}
	got = media.RecognizeRelease("2008.The.Incredible.Hulk.Feature.Film.1080p.mkv")
	if got.Year != 2008 || got.Title != "The Incredible Hulk Feature Film" || got.RequiresReview {
		t.Fatalf("real leading work year lost: %+v", got)
	}
	got = media.RecognizeRelease("1923.S01E01.1080p.WEB-DL.mkv")
	if got.Year != 0 || got.Title != "1923" || got.Season != 1 || len(got.Episodes) != 1 || got.Episodes[0] != 1 {
		t.Fatalf("canonical numeric series changed: %+v", got)
	}
}
