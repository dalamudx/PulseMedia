package media_test

import (
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
)

func TestExternalIdentityCannotMaskReleaseGroupDeny(t *testing.T) {
	rules := quality.PolicyRules{ResolutionOrder: map[quality.Resolution]int{quality.Res1080p: 300}, SourceOrder: map[quality.Source]int{quality.SourceHDTV: 20}, RejectGroups: []string{"ASAP"}}
	for _, tag := range []string{"{tmdb-550}", "[imdb-tt0137523]", "(tt0137523)", "imdb-tt0137523", "tt0137523"} {
		for _, tail := range []string{"ASAP." + tag, "ASAP " + tag, "ASAP[ettv]." + tag, "ASAP[unreviewed]." + tag} {
			raw := "Fixture.2020.1080p.HDTV.x264-" + tail + ".mkv"
			parsed := media.RecognizeRelease(raw)
			if d := quality.EvaluateCandidate(rules, raw, parsed.Quality); d.Passed {
				t.Errorf("identity decoration masked denied group %s: %+v", tail, parsed.Quality)
			}
			// An old incorrect or absent group cannot override raw negative evidence.
			for _, oldGroup := range []string{"", "550", "tt0137523"} {
				old := parsed.Quality
				old.Group = oldGroup
				if d := quality.EvaluateCandidate(rules, raw, old); d.Passed {
					t.Errorf("historical group %q bypassed raw deny: %s", oldGroup, tail)
				}
				if old.Group != oldGroup {
					t.Fatal("raw deny re-signed historical group")
				}
			}
		}
		p := media.RecognizeRelease("Fixture.2020.1080p.HDTV.x264-ASAP." + tag + ".mkv")
		if p.Quality.Group != "ASAP" {
			t.Errorf("external identity replaced release identity %s: %q", tag, p.Quality.Group)
		}
		p = media.RecognizeRelease("Fixture.2020.1080p.HDTV.x264." + tag + ".mkv")
		if p.Quality.Group != "" {
			t.Errorf("external identity invented a group %s: %q", tag, p.Quality.Group)
		}
	}
	for _, tail := range []string{"ASAP2.{tmdb-550}", "OTHER.{tmdb-550}", "550"} {
		p := media.RecognizeRelease("Fixture.2020.1080p.HDTV.x264-" + tail + ".mkv")
		if !quality.EvaluateCandidate(rules, p.RawName, p.Quality).Passed {
			t.Errorf("unrelated group denied: %s", tail)
		}
	}
}
