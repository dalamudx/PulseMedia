package quality_test

import (
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"testing"
)

func TestLegacySourceCodecCannotAuthorizeOwnershipOrScore(t *testing.T) {
	raw := []byte(`{"resolution":"1080p","resolution_parser_version":"resolution.tokens.v1","source":"Bluray","codec":"HEVC"}`)
	var p quality.Profile
	if e := json.Unmarshal(raw, &p); e != nil || p.Resolution != quality.Res1080p || p.Source != "" || p.Codec != "" {
		t.Fatalf("legacy facts trusted: %+v %v", p, e)
	}
	r := quality.PolicyRules{ResolutionOrder: map[quality.Resolution]int{quality.Res1080p: 300}, SourceOrder: map[quality.Source]int{quality.SourceBluray: 40}}
	if quality.KnownProfile(p) || quality.AssessOwnership(r, []quality.FileEvidence{{Verified: true, Profile: p}}).Owned {
		t.Fatal("legacy source granted ownership")
	}
	forged := quality.Profile{Resolution: quality.Res1080p, ResolutionParserVersion: quality.ResolutionTokenParserVersion, Source: quality.SourceBluray}
	if quality.KnownProfile(forged) || quality.DefaultPolicy().Score(forged) != 300 {
		t.Fatal("unversioned direct source granted score")
	}
	forged.SourceParserVersion = quality.SourceTokenParserVersion
	if !quality.KnownProfile(forged) || quality.DefaultPolicy().Score(forged) != 340 {
		t.Fatal("current token source not recognized")
	}
}
