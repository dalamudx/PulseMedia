package media_test

import (
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
)

// Hand-authored adversarial variants are safety probes, not independent goldens.
func TestLongNumberedCollectionsCannotAcquireMovieAuthority(t *testing.T) {
	for _, marker := range []string{"Heptalogy", "Octalogy", "Octology", "Ennealogy", "Nonalogy", "Decalogy"} {
		for _, tail := range []string{
			"2020.1080p.BluRay.x264.{tmdb-550}.mkv",
			"SWESUB.2020.1080p.BluRay.x264.{tmdb-550}.mkv",
			"MULTi.FRENCH.1080p.BluRay.x264.{tmdb-550}.mkv",
			"{tmdb-550}.1080p.BluRay.x264.mkv",
		} {
			raw := "Fixture." + marker + "." + tail
			got, legacy := media.RecognizeRelease(raw), media.Recognize(raw)
			if !got.RequiresReview || got.Confidence != 0 || len(got.UnitKeys) != 0 || !legacy.RequiresReview || legacy.Confidence != 0 || legacy.Identity != nil {
				t.Errorf("multi-work name gained authority %s: %+v / %+v", raw, got, legacy)
			}
			if media.MatchesAutomaticMovie(raw, "Fixture", 2020, map[string]string{"tmdb.movie": "550"}) {
				t.Errorf("movie ID masked collection: %s", raw)
			}
		}
	}
}

func TestOriginalHeptalogyAndExistingLanguageSuffixRequireReview(t *testing.T) {
	// Exact pinned MIT PTT original has only collection/title assertions.
	for _, raw := range []string{
		"American.Pie.Heptalogy.SWESUB.DVDRip.XviD-BaZZe",
		"Fixture.Trilogy.SWESUB.2020.1080p.BluRay.x264.{tmdb-550}.mkv",
		"Fixture.Quadrilogy.MULTi.FRENCH.2020.1080p.BluRay.x264.{tmdb-550}.mkv",
	} {
		got := media.RecognizeRelease(raw)
		if !got.RequiresReview || got.Confidence != 0 || len(got.UnitKeys) != 0 || got.Reason != "multi_unit_collection" {
			t.Errorf("collection suffix was swallowed: %s / %+v", raw, got)
		}
	}
}

func TestLongCollectionWordsDoNotReclassifyOrdinaryTitles(t *testing.T) {
	for _, raw := range []string{
		"Heptalogy.2020.1080p.BluRay.x264.mkv",
		"The.Octalogy.2020.1080p.BluRay.x264.mkv",
		"A.Decalogy.2020.1080p.BluRay.x264.mkv",
		"Heptalogy.of.Terror.2020.1080p.BluRay.x264.mkv",
		"American.Trilogy.of.Terror.2020.1080p.BluRay.x264.mkv",
	} {
		got := media.RecognizeRelease(raw)
		if got.RequiresReview || len(got.UnitKeys) != 1 || got.UnitKeys[0] != "movie" {
			t.Errorf("ordinary title lost original contract: %s / %+v", raw, got)
		}
	}
}
