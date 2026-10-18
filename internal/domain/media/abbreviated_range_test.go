package media_test

import (
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"testing"
)

// Synthetic refusal controls are excluded from licensed golden input counts.
func TestAbbreviatedEpisodeRangesNeverCertifySingleUnits(t *testing.T) {
	for _, raw := range []string{
		"Show (Ep. 1-43) Sub 480p",
		"Show Eps 01~43 1080p",
		"Show EP1-EP43.720p",
		"Show.S01E01.(Ep. 1-43).1080p",
		"[Group] Show (Ep. 1-43) - 43 [1080p]",
		"Show (Ep. 1-43) / Alias.Movie.2024.1080p",
	} {
		got := media.RecognizeRelease(raw)
		if got.MediaType != media.TypeTV || !got.RequiresReview || got.Confidence != 0 || len(got.UnitKeys) != 0 || len(got.Episodes) != 0 || got.AbsoluteEpisode != 0 || got.RawName != raw {
			t.Fatalf("abbreviated range produced partial identity/coverage: %+v", got)
		}
	}
	for _, raw := range []string{
		"Story.EP.1.2024.1080p.mkv",
		"Star.Wars.Episode.1.The.Phantom.Menace.1999.1080p.mkv",
	} {
		got := media.RecognizeRelease(raw)
		if got.MediaType != media.TypeMovie || got.RequiresReview || len(got.UnitKeys) != 1 || got.UnitKeys[0] != "movie" {
			t.Fatalf("non-range movie word blocked: %+v", got)
		}
	}
}
