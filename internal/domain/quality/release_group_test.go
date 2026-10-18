package quality_test

import (
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"testing"
)

func TestRawSuffixCannotBypassGroupDenyWhenOldGroupIsMissing(t *testing.T) {
	rules := quality.PolicyRules{ResolutionOrder: map[quality.Resolution]int{quality.Res1080p: 300}, SourceOrder: map[quality.Source]int{quality.SourceHDTV: 20}, RejectGroups: []string{"S.V"}}
	p := quality.Profile{Resolution: quality.Res1080p, ResolutionParserVersion: quality.ResolutionTokenParserVersion, Source: quality.SourceHDTV, SourceParserVersion: quality.SourceTokenParserVersion}
	for _, tail := range []string{"S.V", "S.V.mkv", "s.v[unreviewed].mkv", "S.V[ettv]"} {
		if d := quality.EvaluateCandidate(rules, "Example.2020.1080p.HDTV.x264-"+tail, p); d.Passed {
			t.Fatalf("missing historical group bypassed exact suffix deny: %q %+v", tail, d)
		}
		if p.Group != "" {
			t.Fatal("raw negative re-signed saved group")
		}
	}
	for _, tail := range []string{"S.V2", "NOTS.V", "S..V", "OTHER"} {
		if d := quality.EvaluateCandidate(rules, "Example.2020.1080p.HDTV.x264-"+tail, p); !d.Passed {
			t.Fatalf("unrelated suffix denied: %q %+v", tail, d)
		}
	}
}

func TestRelayTagCannotDisguiseRejectedReleaseGroup(t *testing.T) {
	rules := quality.PolicyRules{ResolutionOrder: map[quality.Resolution]int{quality.Res1080p: 300}, SourceOrder: map[quality.Source]int{quality.SourceHDTV: 20}, RejectGroups: []string{"ASAP"}}
	for _, group := range []string{"ASAP", "ASAP[ettv]", "asap [rartv]", "ASAP[unreviewed-tag]"} {
		p := quality.Profile{Resolution: quality.Res1080p, ResolutionParserVersion: quality.ResolutionTokenParserVersion, Source: quality.SourceHDTV, SourceParserVersion: quality.SourceTokenParserVersion, Group: group}
		if d := quality.EvaluateCandidate(rules, "Episode.S01E01", p); d.Passed {
			t.Fatalf("decorated old group bypassed negative policy: %q %+v", group, d)
		}
		if p.Group != group {
			t.Fatal("negative comparison mutated historical observation")
		}
	}
	for _, group := range []string{"ASAP2[ettv]", "NOT-ASAP[ettv]", ""} {
		if quality.RejectedReleaseGroup(group, "ASAP") {
			t.Fatalf("unrelated group wrongly rejected: %q", group)
		}
	}
}
