package media_test

import (
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
)

func TestFrenchSeasonHintsRefusePartialCoverage(t *testing.T) {
	for _, input := range []string{
		"One Piece - Saison 17 - French HD 720p x264 - JMK",
		"[Fansub Resistance] Naruto Shippuden Saison 18 [H264- 720p HD] VOSTFR HQ",
		"Show.Saisons.1-8.1080p.mkv",
		"Show.Saison.17.2024.1080p.mkv",
		"Show.S01E01.Saison.17.1080p.mkv",
		"Show.Saison.17.1x01.1080p.mkv",
		"[G] Show Saison 17 - 03 [720p].mkv",
		"Show Saison 17/Movie.2024.1080p.mkv",
		"Show Saison 17/Show.S01E01.1080p.mkv",
		"Show Saison 17/[G] Show - 03 [720p].mkv",
		"Show.S01E01/Movie.Saison.17.2024.1080p.mkv",
	} {
		t.Run(input, func(t *testing.T) {
			got := media.RecognizeRelease(input)
			if got.RawName != input || got.MediaType != media.TypeTV || !got.RequiresReview || got.Confidence != 0 || len(got.UnitKeys) != 0 || len(got.Episodes) != 0 || got.AbsoluteEpisode != 0 || got.IsAnime {
				t.Fatalf("French season evidence gained canonical coverage: %+v", got)
			}
		})
	}
}

func TestFrenchSeasonHintsPreserveOrdinaryTitles(t *testing.T) {
	for _, input := range []string{
		"La.Saison.Des.Feuilles.2024.1080p.mkv",
		"Les.Saisons.2024.1080p.mkv",
		"Saisonnier.17.2024.1080p.mkv",
		"Show.S01E01.1080p.mkv",
		"[G] Show - 03 [720p].mkv",
	} {
		t.Run(input, func(t *testing.T) {
			got := media.RecognizeRelease(input)
			if got.RawName != input || got.Reason == "episode_coverage_unresolved" || got.Reason == "conflicting_episode_coverage" || (len(got.UnitKeys) == 0 && got.AbsoluteEpisode == 0) {
				t.Fatalf("ordinary title or supported numbering refused: %+v", got)
			}
		})
	}
}
