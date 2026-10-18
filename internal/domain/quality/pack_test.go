package quality_test

import (
	"errors"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/quality"
)

func TestPackPolicyCompletenessValidation(t *testing.T) {
	reqPolicy := quality.PackPolicy{
		Mode: quality.PackModeRequirePack,
	}

	completePack := quality.PackCandidate{
		ID:               "pack-complete",
		Title:            "Breaking.Bad.S01.1080p.BluRay",
		Season:           1,
		StartEp:          1,
		EndEp:            7,
		TotalSeasonEps:   7,
		ExpectedEpisodes: []int{1, 2, 3, 4, 5, 6, 7}, CoveredEpisodes: []int{1, 2, 3, 4, 5, 6, 7}, SeasonMetadataKnown: true, CoverageKnown: true,
	}

	incompletePack := quality.PackCandidate{
		ID:             "pack-partial",
		Title:          "Breaking.Bad.S01.Part1.1080p",
		Season:         1,
		StartEp:        1,
		EndEp:          4,
		TotalSeasonEps: 7, // missing episodes 5-7!
	}

	// 1. Complete pack passes require_pack
	if err := quality.EvaluatePackCompleteness(reqPolicy, completePack); err != nil {
		t.Fatalf("complete pack should pass: %v", err)
	}

	// 2. Incomplete pack MUST be rejected under require_pack! (不完整 require_pack 拒绝)
	err := quality.EvaluatePackCompleteness(reqPolicy, incompletePack)
	if !errors.Is(err, quality.ErrIncompletePack) {
		t.Fatalf("expected ErrIncompletePack, got: %v", err)
	}

	// 3. Under prefer_pack, incomplete pack is allowed
	preferPolicy := quality.PackPolicy{
		Mode: quality.PackModePreferPack,
	}
	if err := quality.EvaluatePackCompleteness(preferPolicy, incompletePack); err != nil {
		t.Fatalf("incomplete pack should not be rejected under prefer_pack: %v", err)
	}
}
