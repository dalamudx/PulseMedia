package quality_test

import (
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/quality"
)

func TestEvaluateUpgradeRules(t *testing.T) {
	rules := quality.PolicyRules{
		CutoffScore: 400, // 2160p cutoff
		ResolutionOrder: map[quality.Resolution]int{
			quality.Res2160p: 400,
			quality.Res1080p: 300,
			quality.Res720p:  200,
		},
		SourceOrder: map[quality.Source]int{
			quality.SourceRemux:  50,
			quality.SourceBluray: 40,
			quality.SourceWEBDL:  30,
		},
		PreferHDR: true,
	}

	profile1080p := quality.Profile{
		Resolution:              quality.Res1080p,
		Source:                  quality.SourceWEBDL,
		SourceParserVersion:     quality.SourceTokenParserVersion,
		ResolutionParserVersion: quality.ResolutionTokenParserVersion,
	}

	profile4K := quality.Profile{
		Resolution:              quality.Res2160p,
		Source:                  quality.SourceBluray,
		SourceParserVersion:     quality.SourceTokenParserVersion,
		ResolutionParserVersion: quality.ResolutionTokenParserVersion,
	}

	// 1. 1080p WEB-DL (score 330) upgraded to 4K Bluray (score 440) -> Allowed!
	dec1 := quality.EvaluateUpgrade(rules, profile1080p, profile4K)
	if !dec1.ShouldUpgrade {
		t.Fatalf("expected 1080p -> 4K upgrade allowed, got: %+v", dec1)
	}

	// 2. Current 4K Bluray (score 440) has ALREADY reached Cutoff (400) -> Upgrade Rejected!
	dec2 := quality.EvaluateUpgrade(rules, profile4K, quality.Profile{Resolution: quality.Res2160p, Source: quality.SourceRemux, ResolutionParserVersion: quality.ResolutionTokenParserVersion, SourceParserVersion: quality.SourceTokenParserVersion})
	if dec2.ShouldUpgrade {
		t.Fatalf("expected upgrade rejected when cutoff reached: %+v", dec2)
	}

	// 3. Same tier (1080p WEB-DL to another 1080p WEB-DL) -> Disabled by default!
	dec3 := quality.EvaluateUpgrade(rules, profile1080p, profile1080p)
	if dec3.ShouldUpgrade || dec3.Reason != "new_score_330_not_better_than_current_330" {
		t.Fatalf("same tier replacement should be rejected: %+v", dec3)
	}
}

func TestUpgradeModeComparison(t *testing.T) {
	rules := quality.PolicyRules{CutoffScore: 300, ResolutionOrder: map[quality.Resolution]int{quality.Res1080p: 300, quality.Res2160p: 400}, SourceOrder: map[quality.Source]int{quality.SourceBluray: 40}, PreferHDR: true}
	current := quality.Profile{Resolution: quality.Res1080p, Source: quality.SourceBluray, ResolutionParserVersion: quality.ResolutionTokenParserVersion, SourceParserVersion: quality.SourceTokenParserVersion}
	higher := current
	higher.Resolution = quality.Res2160p
	if d := quality.EvaluateUpgrade(rules, current, higher); d.ShouldUpgrade || !d.ScoresKnown {
		t.Fatal("legacy cutoff contract changed", d)
	}
	for _, tc := range []struct {
		name               string
		mode               quality.UpgradeMode
		current, candidate quality.Profile
		want               bool
	}{
		{"cutoff_stops", quality.UpgradeStopAtCutoff, current, higher, false},
		{"replacement_continues", quality.UpgradeReplaceIfBetter, current, higher, true},
		{"equal_rejected", quality.UpgradeReplaceIfBetter, current, current, false},
		{"downgrade_rejected", quality.UpgradeReplaceIfBetter, higher, current, false},
		{"unknown_current", quality.UpgradeReplaceIfBetter, quality.Profile{}, higher, false},
		{"unknown_candidate", quality.UpgradeReplaceIfBetter, current, quality.Profile{}, false},
		{"disabled", quality.UpgradeDisabled, current, higher, false},
		{"unknown_mode", "future_mode", current, higher, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := quality.EvaluateUpgradeForMode(rules, tc.current, tc.candidate, tc.mode)
			if d.ShouldUpgrade != tc.want {
				t.Fatal(d)
			}
		})
	}
	hdr := current
	hdr.HDR = quality.HDR10
	hdr.HDRParserVersion = quality.HDRTokenParserVersion
	if d := quality.EvaluateUpgradeForMode(rules, current, hdr, quality.UpgradeReplaceIfBetter); d.ShouldUpgrade || !d.ScoresKnown || d.Reason != "same_tier_replacement_disabled" {
		t.Fatal("same-tier optimization enabled", d)
	}
	rules.CutoffScore = 0
	if d := quality.EvaluateUpgradeForMode(rules, current, higher, quality.UpgradeReplaceIfBetter); !d.ShouldUpgrade {
		t.Fatal("replacement requires no stopping threshold", d)
	}
}
