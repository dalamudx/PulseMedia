package quality_test

import (
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/quality"
)

func TestEvaluateCandidateWithRejectTermsAndCutoff(t *testing.T) {
	rules := quality.PolicyRules{
		CutoffScore: 300,
		PreferHDR:   true,
		ResolutionOrder: map[quality.Resolution]int{
			quality.Res2160p: 400,
			quality.Res1080p: 300,
		},
		SourceOrder: map[quality.Source]int{
			quality.SourceRemux:  50,
			quality.SourceBluray: 40,
		},
		RejectTerms:  []string{"cam", "ts", "telesync"},
		RejectGroups: []string{"BADGROUP"},
	}

	// 1. Valid 1080p Bluray candidate meets cutoff
	prof := quality.Profile{
		Resolution:          quality.Res1080p,
		Source:              quality.SourceBluray,
		SourceParserVersion: quality.SourceTokenParserVersion,
	}
	res := quality.EvaluateCandidate(rules, "Fight.Club.1999.1080p.BluRay", prof)
	if !res.Passed || !res.MeetsCutoff || res.Score != 340 {
		t.Fatalf("expected passed with score 340, got: %+v", res)
	}

	// 2. Candidate containing reject term (cam) is rejected
	resCAM := quality.EvaluateCandidate(rules, "Fight.Club.1999.CAM-RELEASE", prof)
	if resCAM.Passed || resCAM.RejectedReason != "命中拒绝词: cam" {
		t.Fatalf("expected rejected for CAM, got: %+v", resCAM)
	}

	// 3. Candidate from blacklisted group is rejected
	profBadGroup := prof
	profBadGroup.Group = "BADGROUP"
	resGroup := quality.EvaluateCandidate(rules, "Fight.Club.1999.1080p.BluRay-BADGROUP", profBadGroup)
	if resGroup.Passed || resGroup.RejectedReason != "命中黑名单发布组: BADGROUP" {
		t.Fatalf("expected rejected for blacklisted group, got: %+v", resGroup)
	}
}
