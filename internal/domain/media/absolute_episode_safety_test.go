package media

import "testing"

func TestAbsoluteEpisodePartialAndMixedEvidence(t *testing.T) {
	for _, c := range []struct{ name, reason string }{{"[Team] Example - 25 [1080p] sample.mkv", "partial_episode_content"}, {"[Team] Example - 25 [1080p] first-half.mkv", "partial_episode_content"}, {"[Team] Example - 25 [1080p] trailer.mkv", "partial_episode_content"}, {"[Team] Example - 25 [1080p] S02E05.mkv", "conflicting_episode_coverage"}, {"[Team] Example S02E05 - 25 [1080p].mkv", "conflicting_episode_coverage"}, {"[Team] Example - 25 [1080p] E26.mkv", "conflicting_episode_coverage"}} {
		t.Run(c.name, func(t *testing.T) {
			got := RecognizeRelease(c.name)
			if !got.RequiresReview || got.Confidence != 0 || len(got.UnitKeys) != 0 || got.AbsoluteEpisode != 0 || got.Reason != c.reason {
				t.Fatalf("unsafe mapping input: %+v", got)
			}
		})
	}
	good := RecognizeRelease("[Team] Example - 25 [1080p].mkv")
	if good.Reason != "absolute_mapping_required" || good.AbsoluteEpisode != 25 || len(good.UnitKeys) != 0 || !good.RequiresReview {
		t.Fatalf("clean absolute evidence changed: %+v", good)
	}
}
