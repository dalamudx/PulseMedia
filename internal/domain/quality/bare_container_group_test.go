package quality_test

import (
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"strings"
	"testing"
)

func TestBareContainerTailCannotHideRejectedOriginalGroup(t *testing.T) {
	rules := quality.PolicyRules{ResolutionOrder: map[quality.Resolution]int{quality.Res1080p: 1}, SourceOrder: map[quality.Source]int{quality.SourceBluray: 1}, RejectGroups: []string{"EDGE2020", "ASAP"}}
	profile := quality.Profile{Resolution: quality.Res1080p, ResolutionParserVersion: quality.ResolutionTokenParserVersion, Source: quality.SourceBluray, SourceParserVersion: quality.SourceTokenParserVersion, Codec: quality.CodecHEVC, CodecParserVersion: quality.CodecTokenParserVersion}
	original := "The Shawshank Redemption 1994 1080p BluRay DDP 5 1 x265-EDGE2020 mkv"
	for _, raw := range []string{original, "Fixture.2020.1080p.Bluray.x265-ASAP mp4", "Fixture.2020.1080p.Bluray.x265-ASAP MKV[ETTV]"} {
		before := profile
		got := quality.EvaluateCandidate(rules, raw, profile)
		if got.Passed || !strings.HasPrefix(got.RejectedReason, "命中黑名单发布组:") {
			t.Errorf("bare container bypassed actual group denial: %s %+v", raw, got)
		}
		if profile != before {
			t.Fatal("historical profile was rewritten")
		}
	}
	for _, tail := range []string{"ASAP mkv-extra", "ASAP mp4x", "ASAP unrelated", "Other mkv", "ASAP mkv extra"} {
		if quality.RejectedReleaseGroup(tail, "ASAP") {
			t.Fatal("unbounded container stripping", tail)
		}
	}
}
