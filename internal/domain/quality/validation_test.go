package quality_test

import (
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"testing"
)

func TestPolicyRulesValidationAndAllowedQuality(t *testing.T) {
	p := quality.DefaultPolicy()
	r := quality.PolicyRules{ResolutionOrder: p.ResolutionOrder, SourceOrder: p.SourceOrder, CutoffScore: 300}
	if e := quality.ValidateRules(r); e != nil {
		t.Fatal(e)
	}
	r.ResolutionOrder = map[quality.Resolution]int{quality.Res2160p: 200}
	allowed := quality.EvaluateCandidate(r, "Movie.1080p.Bluray", quality.Profile{Resolution: quality.Res1080p, Source: quality.SourceBluray, SourceParserVersion: quality.SourceTokenParserVersion})
	if allowed.Passed || allowed.RejectedReason != "分辨率不在允许列表" {
		t.Fatal(allowed)
	}
	ownership := quality.AssessOwnership(r, []quality.FileEvidence{{Verified: true, Profile: quality.Profile{Resolution: quality.Res1080p, Source: quality.SourceBluray, SourceParserVersion: quality.SourceTokenParserVersion}}})
	if ownership.Owned || ownership.Unknown || !ownership.Rejected {
		t.Fatal("known excluded quality became unknown", ownership)
	}
	r.CutoffScore = 0
	unknown := quality.EvaluateCandidate(r, "Movie", quality.Profile{})
	if unknown.Passed || unknown.MeetsCutoff || unknown.RejectedReason != "quality_unknown" {
		t.Fatal("zero cutoff authorized unknown", unknown)
	}
	r.RejectTerms = []string{"blocked", " BLOCKED "}
	if quality.ValidateRules(r) == nil {
		t.Fatal("case-insensitive duplicate accepted")
	}
	r.RejectTerms = []string{}
	r.ResolutionOrder = map[quality.Resolution]int{"9999p": 1}
	if quality.ValidateRules(r) == nil {
		t.Fatal("unknown resolution accepted")
	}
	r.ResolutionOrder = map[quality.Resolution]int{quality.Res2160p: -1}
	if quality.ValidateRules(r) == nil {
		t.Fatal("negative scoring accepted")
	}
}
