package quality_test

import (
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/quality"
)

func TestCustomWordRulesEvaluation(t *testing.T) {
	rules := []quality.CustomWordRule{
		{Word: "Atmos", Score: 100},
		{Word: "DV", Score: 200},
		{Word: "Dubbed", Score: -50},
		{Word: "CAM", Excluded: true},
		{Word: "1080p", Required: true},
	}

	// 1. Matches required 1080p, Atmos (+100) and DV (+200) -> Passed with +300 bonus
	res1 := quality.EvaluateCustomWords("Movie.2024.1080p.Atmos.DV", rules)
	if !res1.Passed || res1.BonusScore != 300 {
		t.Fatalf("expected passed with 300 bonus, got: %+v", res1)
	}

	// 2. Hits excluded word CAM -> Rejected
	res2 := quality.EvaluateCustomWords("Movie.2024.1080p.CAM", rules)
	if res2.Passed || res2.RejectReason != "命中必须排除词: CAM" {
		t.Fatalf("expected rejected for CAM, got: %+v", res2)
	}

	// 3. Missing required word 1080p -> Rejected
	res3 := quality.EvaluateCustomWords("Movie.2024.720p.Atmos", rules)
	if res3.Passed || res3.RejectReason != "缺少必须包含词: 1080p" {
		t.Fatalf("expected rejected for missing 1080p, got: %+v", res3)
	}
}
