package media_test

import (
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
)

func TestPairedTechnicalBracketsDoNotSupplyReleaseGroup(t *testing.T) {
	for _, raw := range []string{
		"Yes Day (2021) [h265 WEBDL-1080p] [tt8521876]",
		"Fixture.2020.[1080p-WEB-DL].mkv",
		"Fixture.2020.(1080p-WEB-DL).mkv",
		"Fixture.2020.[x264-[WEB-DL-1080p]].mkv",
	} {
		if p := media.RecognizeRelease(raw); p.Quality.Group != "" {
			t.Errorf("technical bracket became group: %q %+v", raw, p.Quality)
		}
	}
}

func TestHyphenatedUnknownTagsPreserveRawGroupAndCannotHideDenial(t *testing.T) {
	rules := quality.PolicyRules{ResolutionOrder: map[quality.Resolution]int{quality.Res1080p: 300}, SourceOrder: map[quality.Source]int{quality.SourceHDTV: 20}, RejectGroups: []string{"ASAP"}}
	legacy := quality.Profile{Resolution: quality.Res1080p, ResolutionParserVersion: quality.ResolutionTokenParserVersion, Source: quality.SourceHDTV, SourceParserVersion: quality.SourceTokenParserVersion}
	for _, suffix := range []string{"ASAP[unreviewed-other]", "ASAP[nested[unreviewed-other]]", "ASAP[unreviewed-other][ettv]"} {
		raw := "Fixture.2020.1080p.HDTV.x264-" + suffix + ".mkv"
		p := media.RecognizeRelease(raw)
		want := suffix
		if suffix == "ASAP[unreviewed-other][ettv]" {
			want = "ASAP[unreviewed-other]"
		}
		if p.Quality.Group != want {
			t.Errorf("raw unknown tag lost: %q got %q want %q", raw, p.Quality.Group, want)
		}
		if d := quality.EvaluateCandidate(rules, raw, legacy); d.Passed || d.RejectedReason != "命中黑名单发布组: ASAP" {
			t.Errorf("missing historical group bypassed deny: %q %+v", raw, d)
		}
		if legacy.Group != "" {
			t.Fatal("negative comparison re-signed historical group")
		}
	}
	for _, suffix := range []string{"ASAP2[unreviewed-other]", "OTHER[unreviewed-other]"} {
		raw := "Fixture.2020.1080p.HDTV.x264-" + suffix + ".mkv"
		if d := quality.EvaluateCandidate(rules, raw, legacy); !d.Passed {
			t.Errorf("unrelated group denied: %q %+v", raw, d)
		}
	}
}
