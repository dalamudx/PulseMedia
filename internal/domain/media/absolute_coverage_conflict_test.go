package media

import "testing"

func TestAbsoluteCoverageRetainsAllNumberEvidence(t *testing.T) {
	for _, name := range []string{"[Team] Example - 25 [1080p] - 27 [720p].mkv", "[Team] Example - 25 [1080p] + 27.mkv", "[Team] Example E26 - 25 [1080p].mkv"} {
		got := RecognizeRelease(name)
		if got.Reason != "conflicting_episode_coverage" || !got.RequiresReview || got.AbsoluteEpisode != 0 || len(got.UnitKeys) > 0 {
			t.Fatalf("additional coverage discarded: %+v", got)
		}
	}
}
