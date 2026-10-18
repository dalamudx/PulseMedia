package quality_test

import (
	"encoding/json"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/quality"
)

func TestUpgradeUnknownAndDisallowedQualityNeverBecomesLowRank(t *testing.T) {
	rules := quality.PolicyRules{ResolutionOrder: map[quality.Resolution]int{quality.Res1080p: 300, quality.Res2160p: 400}, SourceOrder: map[quality.Source]int{quality.SourceBluray: 40, quality.SourceWEBDL: 30}}
	current := quality.Profile{Resolution: quality.Res1080p, ResolutionParserVersion: quality.ResolutionTokenParserVersion, Source: quality.SourceWEBDL, SourceParserVersion: quality.SourceTokenParserVersion}
	candidate := quality.Profile{Resolution: quality.Res2160p, ResolutionParserVersion: quality.ResolutionTokenParserVersion, Source: quality.SourceBluray, SourceParserVersion: quality.SourceTokenParserVersion}
	var legacy quality.Profile
	if err := json.Unmarshal([]byte(`{"resolution":"720p","source":"HDTV"}`), &legacy); err != nil {
		t.Fatal(err)
	}
	unversioned := current
	unversioned.SourceParserVersion = ""
	missingResolutionVersion := current
	missingResolutionVersion.ResolutionParserVersion = ""
	disallowed := candidate
	disallowed.Source = quality.SourceRemux
	rejectedGroup := candidate
	rejectedGroup.Group = "blocked"
	rules.RejectGroups = []string{"blocked"}
	for _, tc := range []struct {
		name     string
		old, new quality.Profile
		reason   string
	}{
		{"empty old", quality.Profile{}, candidate, "current_quality_unknown"},
		{"legacy old", legacy, candidate, "current_quality_unknown"},
		{"unsigned source", unversioned, candidate, "current_quality_unknown"},
		{"unsigned resolution", missingResolutionVersion, candidate, "current_quality_unknown"},
		{"unknown new", current, quality.Profile{}, "candidate_quality_unknown"},
		{"disallowed new", current, disallowed, "candidate_policy_rejected"},
		{"disallowed old", disallowed, candidate, "current_policy_incompatible"},
		{"blocked group", current, rejectedGroup, "candidate_policy_rejected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := quality.EvaluateUpgrade(rules, tc.old, tc.new)
			if d.ShouldUpgrade || d.ScoresKnown || d.Reason != tc.reason {
				t.Fatalf("unknown/rejected fact became upgrade permission: %+v", d)
			}
		})
	}
	if d := quality.EvaluateUpgrade(rules, current, candidate); !d.ShouldUpgrade || !d.ScoresKnown {
		t.Fatalf("current allowed evidence should remain eligible: %+v", d)
	}
	invalid := rules
	invalid.ResolutionOrder = map[quality.Resolution]int{quality.Res1080p: -1}
	if d := quality.EvaluateUpgrade(invalid, current, candidate); d.ShouldUpgrade || d.ScoresKnown || d.Reason != "invalid_policy" {
		t.Fatalf("invalid policy produced a comparable upgrade: %+v", d)
	}
	zero := rules
	zero.ResolutionOrder = map[quality.Resolution]int{quality.Res1080p: 0, quality.Res2160p: 1}
	zero.SourceOrder = map[quality.Source]int{quality.SourceWEBDL: 0, quality.SourceBluray: 0}
	if d := quality.EvaluateUpgrade(zero, current, candidate); !d.ShouldUpgrade || !d.ScoresKnown || d.CurrentScore != 0 || d.NewScore != 1 {
		t.Fatalf("explicit allowed zero rank was confused with unknown: %+v", d)
	}
	preferHDR := rules
	preferHDR.PreferHDR = true
	hdrSameTier := current
	hdrSameTier.HDR = quality.HDRDolbyVision
	hdrSameTier.HDRParserVersion = quality.HDRTokenParserVersion
	if d := quality.EvaluateUpgrade(preferHDR, current, hdrSameTier); d.ShouldUpgrade || !d.ScoresKnown || d.Reason != "same_tier_replacement_disabled" {
		t.Fatalf("same-tier HDR does not authorize replacement: %+v", d)
	}
}
