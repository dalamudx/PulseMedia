package media_test

import (
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"testing"
)

func TestExplicitTextSidecarsNeverGrantUnits(t *testing.T) {
	for _, raw := range []string{"Movie.2024.txt", "Movie.2024.{tmdb-123}.nfo", "Show.S01E03.1080p.txt", "[PUNCH] Show - 03 [1080p].nfo", "Movie.2024.TXT", "Show.S01E03.2024.txt", "Whiplash (2014) (1080p 10bit BluRay AAC5.1 HEVC-Vyndros).txt"} {
		t.Run(raw, func(t *testing.T) {
			got := media.RecognizeRelease(raw)
			if got.RawName != raw || got.Confidence != 0 || !got.RequiresReview || got.Reason != "non_video_file" || got.Season != 0 || len(got.Episodes) != 0 || len(got.UnitKeys) != 0 || got.AbsoluteEpisode != 0 || got.IsAnime {
				t.Fatalf("text sidecar acquired units: %+v", got)
			}
			legacy := media.Recognize(raw)
			if legacy.RawName != raw || legacy.Confidence != 0 || !legacy.RequiresReview || legacy.Identity != nil || media.MatchesAutomaticMovie(raw, "Movie", 2024, map[string]string{"tmdb.movie": "123"}) {
				t.Fatalf("text sidecar acquired identity: %+v", legacy)
			}
		})
	}
}

func TestTextSidecarSuffixDoesNotConsumeOrdinaryTitles(t *testing.T) {
	for _, raw := range []string{"TXT.2024.mkv", "NFO.2024.mkv", "Movie.2024.txtx", "Movie.2024.nfox"} {
		got := media.RecognizeRelease(raw)
		if got.Reason == "non_video_file" {
			t.Fatal("ordinary title or unknown suffix rejected", raw, got)
		}
	}
}
