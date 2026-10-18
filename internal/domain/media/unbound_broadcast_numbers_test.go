package media_test

import (
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"testing"
)

func TestUnboundBroadcastNumbersAndLocalizedIntervalNeverAuthorize(t *testing.T) {
	originals := []string{
		"[모에-Raws] Kakegurui #05 (BS11 1280x720 x264 AAC).mp4",
		"[Anime Land] Kakegurui 11 (BS11 720p Hi10P AAC) RAW [FF183D95].mp4",
		"[Anime Land] Made in Abyss 12 (BS11 720p Hi10P AAC) RAW [F5B00E92].mp4",
		"[WZF] Toriko - Episodio 93~94[HD][X264-AAC][1280x720][Sub Esp]",
	}
	for _, original := range originals {
		for _, input := range []string{original, original + " 2020 {tmdb-550}", original + " S01E01", original + "/Movie.2020.{tmdb-550}.mkv", original + "/Alias.S01E01.1080p.mkv", original + "/[SubsPlease] Alias - 01 [1080p].mkv"} {
			t.Run(input, func(t *testing.T) {
				got := media.RecognizeRelease(input)
				if !got.RequiresReview || got.Confidence != 0 || len(got.UnitKeys) != 0 || len(got.Episodes) != 0 || got.Season != 0 || got.AbsoluteEpisode != 0 {
					t.Fatalf("unbound source retained coverage: %+v", got)
				}
				legacy := media.Recognize(input)
				if !legacy.RequiresReview || legacy.Identity != nil || legacy.Confidence != 0 {
					t.Fatalf("legacy retained identity: %+v", legacy)
				}
			})
		}
	}
}

func TestUnboundBroadcastRefusalKeepsYearsTitlesAndBoundAbsolute(t *testing.T) {
	for _, input := range []string{"[Anime Land] Movie 2020 (BS11 720p x264 AAC).mkv", "[Anime Land] Apollo 13 (BD 1080p x264 AAC).mkv", "[SubsPlease] Chihayafuru 3 - 21 [1080p].mkv", "[Anime Land] Kakegurui - 11 [720p].mkv", "Movie Episodio 93 (2020).mkv"} {
		got := media.RecognizeRelease(input)
		if got.Reason == "episode_coverage_unresolved" || got.Reason == "conflicting_episode_coverage" {
			t.Fatalf("ordinary title, year or bound absolute refused: %+v", got)
		}
	}
}
