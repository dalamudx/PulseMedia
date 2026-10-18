package media_test

import (
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
)

func TestCoffretCollectionCannotHidePartialCoverage(t *testing.T) {
	for _, input := range []string{
		"Series.2020.COMPLETE.BluRay.Coffret.1080p.mkv",
		"Series.COMPLETE.BluRay.Coffret.{tmdb-123}.1080p.mkv",
		"Series.S01E01.COMPLETE.BluRay.Coffret.1080p.mkv",
		"Series.1x01.COMPLETE.BluRay.Coffret.1080p.mkv",
		"[SG] Series - 01 COMPLETE.BluRay.Coffret.1080p.mkv",
		"Series.COMPLETE.BluRay.Coffret/Film.2020.1080p.mkv",
		"Series.COMPLETE.BluRay.Coffret/Show.S01E01.1080p.mkv",
		"Series.COMPLETE.BluRay.Coffret/Show.1x01.1080p.mkv",
		"Series.COMPLETE.BluRay.Coffret/[SG] Show - 01 [1080p].mkv",
	} {
		t.Run(input, func(t *testing.T) {
			got := media.RecognizeRelease(input)
			if got.RawName != input || !got.RequiresReview || got.Confidence != 0 || len(got.UnitKeys) != 0 || len(got.Episodes) != 0 || got.AbsoluteEpisode != 0 || got.Season != 0 {
				t.Fatalf("collection retained partial coverage: %+v", got)
			}
			legacy := media.Recognize(input)
			if !legacy.RequiresReview || legacy.Confidence != 0 || legacy.Identity != nil {
				t.Fatalf("legacy movie parser retained collection authority: %+v", legacy)
			}
		})
	}
	got := media.Recognize("Series.2020.COMPLETE.BluRay.Coffret.{tmdb-123}.mkv")
	if !got.RequiresReview || got.Confidence != 0 || got.Identity != nil {
		t.Fatalf("legacy movie parser retained collection authority: %+v", got)
	}
}

func TestCoffretAndCompleteDiscTitlesRemainOrdinary(t *testing.T) {
	for _, input := range []string{
		"Coffret.2020.1080p.BluRay.mkv",
		"Complete.Coffret.2020.1080p.BluRay.mkv",
		"CoffretX.2020.COMPLETE.BLURAY-TAG",
		"Apprentice.2016.COMPLETE.BLURAY-UNRELiABLE",
		"Indiana.Jones.and.the.Temple.of.Doom.1984.Complete.UHD.Bluray-JONES",
	} {
		t.Run(input, func(t *testing.T) {
			got := media.RecognizeRelease(input)
			if got.RequiresReview || got.Confidence != 0.9 || len(got.UnitKeys) != 1 || got.UnitKeys[0] != "movie" {
				t.Fatalf("ordinary film/disc rejected: %+v", got)
			}
		})
	}
}

func TestCompleteSeriesExtrasTitlePreservesUnknownContinuation(t *testing.T) {
	input := "Series Complete Series + OVA Story 1080p BluRay"
	got := media.RecognizeRelease(input)
	if !strings.Contains(got.Title, "Story") || got.RawName != input || !got.RequiresReview || len(got.UnitKeys) != 0 {
		t.Fatalf("unknown title continuation removed: %+v", got)
	}
}

func TestSingleOVADoesNotDeclareCompleteCollection(t *testing.T) {
	input := "[FansubAnimesForever] Highschool of the Dead OVA [BDRip-1080p-8bit].mkv"
	got := media.RecognizeRelease(input)
	if got.RawName != input || got.Reason != "episode_coverage_unresolved" || got.Confidence != 0 || !got.RequiresReview || len(got.UnitKeys) != 0 || len(got.Episodes) != 0 || got.AbsoluteEpisode != 0 {
		t.Fatalf("single OVA title confused with complete collection: %+v", got)
	}
}
