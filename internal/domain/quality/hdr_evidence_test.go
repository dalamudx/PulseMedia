package quality_test

import (
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"testing"
)

func TestLegacyHDRCannotAddRankOrSatisfyCutoff(t *testing.T) {
	rules := quality.PolicyRules{PreferHDR: true, CutoffScore: 25, ResolutionOrder: map[quality.Resolution]int{quality.Res1080p: 10}, SourceOrder: map[quality.Source]int{quality.SourceWEBDL: 5}}
	for _, version := range []string{"", "old", "future"} {
		p := quality.Profile{Resolution: quality.Res1080p, ResolutionParserVersion: quality.ResolutionTokenParserVersion, Source: quality.SourceWEBDL, SourceParserVersion: quality.SourceTokenParserVersion, HDR: quality.HDRDolbyVision, HDRParserVersion: version}
		raw, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		var decoded quality.Profile
		if err = json.Unmarshal(raw, &decoded); err != nil || decoded.HDR != "" || decoded.Resolution != p.Resolution || decoded.Source != p.Source {
			t.Fatal("legacy evidence not unknown or lost base facts", decoded, err)
		}
		for _, prof := range []quality.Profile{p, decoded} {
			evaluation := quality.EvaluateCandidate(rules, "Adventureland.2009.1080p.WEB-DL.x264", prof)
			if !evaluation.Passed || evaluation.Score != 15 || evaluation.MeetsCutoff {
				t.Fatal("old HDR raised rank/cutoff", evaluation)
			}
		}
	}
	p := quality.Profile{Resolution: quality.Res1080p, ResolutionParserVersion: quality.ResolutionTokenParserVersion, Source: quality.SourceWEBDL, SourceParserVersion: quality.SourceTokenParserVersion, HDR: quality.HDRDolbyVision, HDRParserVersion: quality.HDRTokenParserVersion}
	raw, _ := json.Marshal(p)
	var decoded quality.Profile
	if err := json.Unmarshal(raw, &decoded); err != nil || decoded.HDR != p.HDR {
		t.Fatal(decoded, err)
	}
	if result := quality.EvaluateCandidate(rules, "Film.2020.DV", decoded); result.Score != 30 || !result.MeetsCutoff {
		t.Fatal("new explicit HDR lost", result)
	}
}
