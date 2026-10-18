package quality_test

import (
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"testing"
)

func TestLegacyResolutionObservationDoesNotBecomeCurrentEvidence(t *testing.T) {
	raw := []byte(`{"resolution":"720p","source":"HDTV","codec":"H264"}`)
	var p quality.Profile
	if e := json.Unmarshal(raw, &p); e != nil || p.Resolution != "" || p.Source != "" {
		t.Fatal("legacy inferred resolution not unknown", p, e)
	}
	rules := quality.PolicyRules{ResolutionOrder: map[quality.Resolution]int{quality.Res720p: 10}, SourceOrder: map[quality.Source]int{quality.SourceHDTV: 5}}
	if evaluated := quality.EvaluateCandidate(rules, "Show.2020.S01E01.HDTV", p); evaluated.Passed || evaluated.RejectedReason != "quality_unknown" {
		t.Fatal("old profile authorized", evaluated)
	}
	current := quality.Profile{Resolution: quality.Res720p, Source: quality.SourceHDTV, SourceParserVersion: quality.SourceTokenParserVersion, ResolutionParserVersion: quality.ResolutionTokenParserVersion}
	encoded, _ := json.Marshal(current)
	if e := json.Unmarshal(encoded, &p); e != nil || p.Resolution != quality.Res720p {
		t.Fatal("current explicit evidence lost", p, e)
	}
}
