package media

import "testing"

func TestScoutRepairCompactSeasonCompleteMixedRefusal(t *testing.T) {
	for _, raw := range []string{
		"Orchard.Season1.COMPLETE.S02E02.720p.BRrip.NewCrew.mkv",
		"Unseen.Garden.S02E02.Season1.COMPLETE.720p.HDTV.x264-NewCrew.mkv",
		"Unseen.Garden.Season1.COMPLETE.2x03.720p.HDTV.x264-NewCrew.mkv",
		"Unseen.Garden.Season1.COMPLETE/Other.S02E02.720p.HDTV.x264-NewCrew.mkv",
	} {
		t.Run(raw, func(t *testing.T) {
			r, l := RecognizeRelease(raw), Recognize(raw)
			if r.RawName != raw || r.MediaType != TypeTV || r.Season != 0 || r.AbsoluteEpisode != 0 || len(r.Episodes) != 0 || len(r.UnitKeys) != 0 || r.IsAnime || r.Confidence != 0 || !r.RequiresReview || r.Reason != "conflicting_episode_coverage" {
				t.Fatalf("mixed numbering acquired coverage: %+v", r)
			}
			if l.RawName != raw || l.Confidence != 0 || !l.RequiresReview || l.Identity != nil {
				t.Fatalf("legacy mixed numbering acquired authority: %+v", l)
			}
		})
	}
}
