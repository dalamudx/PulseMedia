package quality_test

import (
	"errors"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"testing"
)

func TestPackCompletenessRequiresWholeExplicitEvidence(t *testing.T) {
	policy := quality.PackPolicy{Mode: quality.PackModeRequirePack}
	base := quality.PackCandidate{Season: 1, StartEp: 1, EndEp: 3, TotalSeasonEps: 3, ExpectedEpisodes: []int{1, 2, 3}, CoveredEpisodes: []int{1, 2, 3}, SeasonMetadataKnown: true, CoverageKnown: true}
	if !base.IsCompleteSeason() {
		t.Fatal("explicit full set denied")
	}
	for _, name := range []string{"endpoints-only", "missing-middle", "extra", "duplicate-coverage", "duplicate-expected", "unknown-files", "unknown-season", "unknown-coverage", "unknown-count", "wrong-count", "special-season", "invalid-number"} {
		t.Run(name, func(t *testing.T) {
			p := base
			switch name {
			case "endpoints-only":
				p.ExpectedEpisodes = nil
				p.CoveredEpisodes = nil
			case "missing-middle":
				p.CoveredEpisodes = []int{1, 3}
			case "extra":
				p.CoveredEpisodes = []int{1, 2, 3, 4}
			case "duplicate-coverage":
				p.CoveredEpisodes = []int{1, 2, 2}
			case "duplicate-expected":
				p.ExpectedEpisodes = []int{1, 2, 2}
			case "unknown-files":
				p.HasUnknownFiles = true
			case "unknown-season":
				p.SeasonMetadataKnown = false
			case "unknown-coverage":
				p.CoverageKnown = false
			case "unknown-count":
				p.TotalSeasonEps = 0
			case "wrong-count":
				p.TotalSeasonEps = 2
			case "special-season":
				p.Season = 0
			case "invalid-number":
				p.ExpectedEpisodes = []int{1, 2, 0}
				p.CoveredEpisodes = []int{1, 2, 0}
			}
			if p.IsCompleteSeason() || !errors.Is(quality.EvaluatePackCompleteness(policy, p), quality.ErrIncompletePack) {
				t.Fatal("unknown/incomplete pack was accepted", p)
			}
		})
	}
	p := base
	p.TotalSeasonEps = 2
	p.ExpectedEpisodes = []int{1, 3}
	p.CoveredEpisodes = []int{3, 1}
	if !p.IsCompleteSeason() {
		t.Fatal("explicit non-contiguous canonical season incorrectly filled")
	}
}
