package quality_test

import (
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"testing"
)

func TestMovieDiscoveryAllowed(t *testing.T) {
	rules := quality.PolicyRules{CutoffScore: 340, ResolutionOrder: map[quality.Resolution]int{quality.Res1080p: 300}, SourceOrder: map[quality.Source]int{quality.SourceBluray: 40}}
	current := quality.Profile{Resolution: quality.Res1080p, Source: quality.SourceBluray, ResolutionParserVersion: quality.ResolutionTokenParserVersion, SourceParserVersion: quality.SourceTokenParserVersion}
	for _, tc := range []struct {
		name    string
		mode    quality.UpgradeMode
		cutoff  int
		unknown bool
		want    bool
	}{
		{"below_cutoff", quality.UpgradeStopAtCutoff, 341, false, true},
		{"at_cutoff", quality.UpgradeStopAtCutoff, 340, false, false},
		{"above_cutoff", quality.UpgradeStopAtCutoff, 339, false, false},
		{"replace_after_cutoff", quality.UpgradeReplaceIfBetter, 339, false, true},
		{"disabled", quality.UpgradeDisabled, 1000, false, false},
		{"unknown_current", quality.UpgradeReplaceIfBetter, 1000, true, false},
		{"no_cutoff", quality.UpgradeStopAtCutoff, 0, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := rules
			r.CutoffScore = tc.cutoff
			p := current
			if tc.unknown {
				p.ResolutionParserVersion = ""
			}
			if got := quality.MovieDiscoveryAllowed(r, p, tc.mode); got != tc.want {
				t.Fatalf("allowed=%v want %v", got, tc.want)
			}
		})
	}
}
