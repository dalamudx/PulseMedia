package media_test

import (
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
)

func TestLocalizedRangeAndUnboundCRCRefusePartialCoverage(t *testing.T) {
	for _, input := range []string{
		"Heidi Audio Latino DVDRip [cap. 3 Al 18]",
		"Heidi.S01E03.[cap. 3 Al 18].1080p.mkv",
		"Heidi [cap. 3 Al 18]/Movie.2020.1080p.mkv",
		"[DB]_Bleach_264_[012073FE].avi",
		"[DB]_Bleach_264_[012073FE]_S01E264.mkv",
		"[DB]_Bleach_264_[012073FE]/Movie.2020.1080p.mkv",
		"Heidi [cap. 3 Al 18]/Heidi.S01E03.1080p.mkv",
		"[DB]_Bleach_264_[012073FE]/Bleach.S01E264.1080p.mkv",
		"Heidi [cap. 3 Al 18]/[DB] Heidi - 3 [012073FE].mkv",
		"[DB]_Bleach_264_[012073FE]/[DB] Bleach - 264 [012073FE].mkv",
	} {
		t.Run(input, func(t *testing.T) {
			got := media.RecognizeRelease(input)
			if got.RawName != input || got.MediaType != media.TypeTV || !got.RequiresReview || got.Confidence != 0 || len(got.Episodes) != 0 || len(got.UnitKeys) != 0 || got.AbsoluteEpisode != 0 || got.IsAnime {
				t.Fatalf("unresolved coverage gained identity or partial units: %+v", got)
			}
		})
	}
}

func TestLocalizedRangeAndCRCHintsPreserveMovieAndBoundAbsoluteContracts(t *testing.T) {
	for _, input := range []string{
		"Cape.Fear.1991.1080p.BluRay.x264-GROUP.mkv",
		"The.Cap.2020.1080p.mkv",
		"[DB]_Movie_2020_[012073FE].mkv",
		"[DB]_300_2006_[012073FE].mkv",
	} {
		t.Run(input, func(t *testing.T) {
			got := media.RecognizeRelease(input)
			if got.MediaType != media.TypeMovie || got.RequiresReview || len(got.UnitKeys) != 1 || got.UnitKeys[0] != "movie" {
				t.Fatalf("ordinary movie incorrectly refused: %+v", got)
			}
		})
	}
	input := "[DB] Bleach - 264 [012073FE].mkv"
	got := media.RecognizeRelease(input)
	if got.RawName != input || got.AbsoluteEpisode != 264 || !got.IsAnime || !got.RequiresReview || len(got.UnitKeys) != 0 || got.Reason != "absolute_mapping_required" {
		t.Fatalf("existing bound absolute contract changed: %+v", got)
	}
}
