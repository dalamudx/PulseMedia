package media_test

import (
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"testing"
)

func TestAudioCountsAreNotEpisodesAndAbsoluteQualityIsBounded(t *testing.T) {
	for _, input := range []string{"Film.2000.1080p.BluRay.x265.AC3.5.1x2+2.0x3.mkv", "Show.2020.S01E01.1080p.AAC.5.1x2+2.0x3.mkv", "Show.2020.1x02.1080p.AC3.5.1x2+2.0x3.mkv"} {
		r := media.RecognizeRelease(input)
		if r.RequiresReview || len(r.UnitKeys) != 1 {
			t.Fatal("known audio counts contaminated coverage", r)
		}
		if input[:4] == "Film" && (r.MediaType != media.TypeMovie || r.UnitKeys[0] != "movie") {
			t.Fatal("audio count acquired a season", r)
		}
	}
	good := media.RecognizeRelease("[Team] Example - 25 (1080p) [HASH].mkv")
	if good.Reason != "absolute_mapping_required" || good.AbsoluteEpisode != 25 || good.Title != "Example" || good.IsAnime != true || len(good.UnitKeys) != 0 || !good.RequiresReview || good.Confidence != 0 {
		t.Fatal("clear absolute number not explained safely", good)
	}
	for _, input := range []string{"[Team] Example.2020 - 25 (480p E26) [HASH].mkv", "[Team] Example - 25 (1080p) [HASH] S01E01.mkv", "[Team] Example - 25 (sample) [HASH].mkv", "[Team] Example - 25 (1080p) [HASH] - 27.mkv"} {
		r := media.RecognizeRelease(input)
		if !r.RequiresReview || r.Confidence != 0 || len(r.UnitKeys) != 0 || r.AbsoluteEpisode != 0 {
			t.Fatal("parenthetical/noisy coverage bypassed mapping safety", r)
		}
	}
}
