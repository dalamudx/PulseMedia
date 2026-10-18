package media_test

import (
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
)

func TestUnboundCRCResolutionHintsRefusePartialCoverage(t *testing.T) {
	for _, input := range []string{
		"[SG] Hitman Reborn 187 [480p] [8A5256BE].mkv",
		"[SG] Hitman Reborn 187 [480p][8a5256be].2020.mkv",
		"[SG] Hitman Reborn 187 [480p] [8A5256BE]_S01E187.mkv",
		"[SG] Hitman Reborn 187 [480p] [8A5256BE]_1x187.mkv",
		"[SG] Hitman Reborn 187 [480p] [8A5256BE]/Movie.2020.1080p.mkv",
		"[SG] Hitman Reborn 187 [480p] [8A5256BE]/Hitman.S01E187.1080p.mkv",
		"[SG] Hitman Reborn 187 [480p] [8A5256BE]/[SG] Hitman Reborn - 187 [8A5256BE].mkv",
	} {
		t.Run(input, func(t *testing.T) {
			got := media.RecognizeRelease(input)
			if got.RawName != input || got.MediaType != media.TypeTV || !got.RequiresReview || got.Confidence != 0 || got.Year != 0 || got.Season != 0 || len(got.Episodes) != 0 || len(got.UnitKeys) != 0 || got.AbsoluteEpisode != 0 || got.IsAnime {
				t.Fatalf("unbound numbered source acquired identity or partial coverage: %+v", got)
			}
		})
	}
}

func TestUnboundCRCResolutionHintsKeepMovieYearsAndBoundAbsolute(t *testing.T) {
	for _, input := range []string{
		"[SG] Movie 2020 [480p] [8A5256BE].mkv",
		"[SG] 300 2006 [480p] [8A5256BE].mkv",
	} {
		got := media.RecognizeRelease(input)
		if got.MediaType != media.TypeMovie || got.RequiresReview || len(got.UnitKeys) != 1 || got.UnitKeys[0] != "movie" {
			t.Fatalf("movie year mistaken for episode: %+v", got)
		}
	}
	input := "[SG] Hitman Reborn - 187 [480p][8A5256BE].mkv"
	got := media.RecognizeRelease(input)
	if got.RawName != input || !got.IsAnime || got.AbsoluteEpisode != 187 || !got.RequiresReview || got.Confidence != 0 || len(got.UnitKeys) != 0 || got.Reason != "absolute_mapping_required" {
		t.Fatalf("bound absolute contract changed: %+v", got)
	}
}
