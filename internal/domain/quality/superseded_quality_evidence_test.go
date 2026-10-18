package quality_test

import (
	"bytes"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"testing"
)

func TestSupersededSourceCodecVersionsStayUnknownWithoutResigning(t *testing.T) {
	raw := []byte(`{"resolution":"1080p","resolution_parser_version":"resolution.tokens.v1","source":"Bluray","source_parser_version":"source.tokens.v1","codec":"HEVC","codec_parser_version":"codec.tokens.v1","hdr":"HDR10","hdr_parser_version":"hdr.tokens.v1","group":"OriginalGroup"}`)
	original := bytes.Clone(raw)
	var profile quality.Profile
	if err := json.Unmarshal(raw, &profile); err != nil {
		t.Fatal(err)
	}
	if profile.Source != "" || profile.Codec != "" || profile.SourceParserVersion != "" || profile.CodecParserVersion != "" {
		t.Fatal("superseded contradiction semantics still trusted", profile)
	}
	if profile.Resolution != quality.Res1080p || profile.HDR != quality.HDR10 || profile.Group != "OriginalGroup" {
		t.Fatal("independent evidence was lost", profile)
	}
	if !bytes.Equal(original, raw) {
		t.Fatal("original evidence rewritten")
	}
	defaults := quality.DefaultPolicy()
	rules := quality.PolicyRules{ResolutionOrder: defaults.ResolutionOrder, SourceOrder: defaults.SourceOrder, PreferHDR: defaults.PreferHDR}
	if quality.KnownProfile(profile) || quality.EvaluateCandidate(rules, "Movie", profile).Passed || quality.AssessOwnership(rules, []quality.FileEvidence{{Verified: true, Profile: profile}}).Owned {
		t.Fatal("old signed source still permits candidate or ownership")
	}
	direct := quality.Profile{Resolution: quality.Res1080p, ResolutionParserVersion: quality.ResolutionTokenParserVersion, Source: quality.SourceBluray, SourceParserVersion: "source.tokens.v1"}
	if quality.KnownProfile(direct) || quality.DefaultPolicy().Score(direct) != 300 {
		t.Fatal("direct v1 source still obtains source bonus", direct)
	}
}
