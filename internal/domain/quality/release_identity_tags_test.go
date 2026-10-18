package quality_test

import (
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/quality"
)

func TestReleaseIdentityLexicalViewKeepsUnknownOrMalformedTags(t *testing.T) {
	for _, raw := range []string{"ASAP[unreviewed]", "ASAP{tmdb-550]", "ASAP[tmdb-550}", "ASAP(tmdb-550]", "ASAP{tmdb-other}", "ASAP[imdb-tt123]", "ASAP-", "550"} {
		if got := quality.WithoutReleaseIdentityTags(raw); got != raw {
			t.Errorf("unknown evidence normalized: %q -> %q", raw, got)
		}
	}
	for _, raw := range []string{"ASAP.{tmdb-550}", "ASAP.[imdb-tt0137523]", "ASAP (tt0137523)", "ASAP.{TMDB-550}.{tmdb-42}"} {
		if got := quality.WithoutReleaseIdentityTags(raw); got != "ASAP" {
			t.Errorf("paired identity masked group: %q -> %q", raw, got)
		}
	}
}
func TestBareIdentityTrailerKeepsUnknownAndMalformedNames(t *testing.T) {
	for _, raw := range []string{"ASAP.tt123", "ASAP.tt013752312345", "ASAP-imdb-unknown", "ASAPtt0137523", "ASAP{imdb-tt0137523]"} {
		if got := quality.WithoutReleaseIdentityTags(raw); got != raw {
			t.Errorf("invalid bare identity silently normalized: %q -> %q", raw, got)
		}
	}
	for _, tc := range []struct{ raw, want string }{
		{"ASAP.imdb-tt0137523", "ASAP"},
		{"ASAP.tt0137523", "ASAP"},
		{"ASAP_tt0137523", "ASAP"},
		{"ASAP-imdb-tt0137523", "ASAP"},
		{"ASAP TT0137523", "ASAP"},
		{"ASAP.imdb-tt0137523[ettv]", "ASAP[ettv]"},
		{"ASAP.tt0137523[unreviewed]", "ASAP[unreviewed]"},
		{"ASAP.{tmdb-550}[ettv]", "ASAP[ettv]"},
		{"ASAP.[imdb-tt0137523][unreviewed]", "ASAP[unreviewed]"},
	} {
		if got := quality.WithoutReleaseIdentityTags(tc.raw); got != tc.want {
			t.Errorf("known identity masked group: %q -> %q want %q", tc.raw, got, tc.want)
		}
	}
}

func TestBareAndComposedIdentityCannotHideRawDeniedGroup(t *testing.T) {
	rules := quality.PolicyRules{ResolutionOrder: map[quality.Resolution]int{quality.Res1080p: 300}, SourceOrder: map[quality.Source]int{quality.SourceHDTV: 20}, RejectGroups: []string{"ASAP"}}
	for _, savedGroup := range []string{"", "550", "tt0137523"} {
		p := quality.Profile{Resolution: quality.Res1080p, ResolutionParserVersion: quality.ResolutionTokenParserVersion, Source: quality.SourceHDTV, SourceParserVersion: quality.SourceTokenParserVersion, Group: savedGroup}
		for _, tail := range []string{"ASAP.imdb-tt0137523", "ASAP.tt0137523", "ASAP.{tmdb-550}[ettv]", "ASAP.[imdb-tt0137523][unreviewed]"} {
			if d := quality.EvaluateCandidate(rules, "Example.2020.1080p.HDTV.x264-"+tail+".mkv", p); d.Passed {
				t.Errorf("identity bypassed exact raw-group deny: %q savedGroup=%q decision=%+v", tail, savedGroup, d)
			}
			if p.Group != savedGroup {
				t.Fatal("negative check rewrote historical group")
			}
		}
	}
}
