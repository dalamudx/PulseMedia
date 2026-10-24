package quality_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/quality"
)

func wordPolicy() (quality.PolicyRules, quality.Profile) {
	return quality.PolicyRules{CutoffScore: 400, ResolutionOrder: map[quality.Resolution]int{quality.Res1080p: 300, quality.Res720p: 200}, SourceOrder: map[quality.Source]int{quality.SourceHDTV: 10}}, quality.Profile{Resolution: quality.Res1080p, Source: quality.SourceHDTV, SourceParserVersion: quality.SourceTokenParserVersion, ResolutionParserVersion: quality.ResolutionTokenParserVersion}
}

func TestPolicyWordsSeparateSelectionFromQuality(t *testing.T) {
	rules, profile := wordPolicy()
	rules.CustomWords = []quality.CustomWordRule{{Word: "Atmos", Score: 1000}}
	before, _ := json.Marshal(profile)
	got := quality.EvaluateCandidate(rules, "Film.2020.1080p.HDTV.Atmos", profile)
	if !got.Passed || got.Score != 310 || got.PreferenceScore != 1000 || got.SelectionScore() != 1310 || got.MeetsCutoff {
		t.Fatalf("title is not quality evidence: %+v", got)
	}
	after, _ := json.Marshal(profile)
	if string(before) != string(after) {
		t.Fatal("title changed quality facts")
	}
	rules.CustomWords[0].Score = -1000
	got = quality.EvaluateCandidate(rules, "Film.Atmos", profile)
	if !got.Passed || got.Score != 310 || got.PreferenceScore != -1000 || got.SelectionScore() != -690 || got.MeetsCutoff {
		t.Fatalf("negative preference changed evidence: %+v", got)
	}
}

func TestPolicyWordsDoNotPromoteUnknownQuality(t *testing.T) {
	rules, profile := wordPolicy()
	rules.CustomWords = []quality.CustomWordRule{{Word: "DV", Score: 1000}}
	profile.SourceParserVersion = "old-source"
	got := quality.EvaluateCandidate(rules, "Film.DV", profile)
	if got.Passed || got.RejectedReason != "quality_unknown" || got.Score != 0 || got.PreferenceScore != 0 {
		t.Fatalf("word promoted unknown evidence: %+v", got)
	}
}

func TestPolicyWordsStrictAdmissionAndLegacyGates(t *testing.T) {
	rules, profile := wordPolicy()
	rules.CustomWords = []quality.CustomWordRule{{Word: "Atmos", Required: true}, {Word: "CAM", Excluded: true}, {Word: "DV", Score: 200}}
	for _, name := range []string{"Film.DV", "Film.Atmos.CAM.DV"} {
		got := quality.EvaluateCandidate(rules, name, profile)
		if got.Passed || got.Score != 0 || got.PreferenceScore != 0 {
			t.Fatalf("keyword gate bypassed: %+v", got)
		}
	}
	rules.RejectTerms = []string{"SCAM"}
	if quality.EvaluateCandidate(rules, "Film.Atmos.SCAM.DV", profile).Passed {
		t.Fatal("new words bypassed legacy title rejection")
	}
	rules.CustomWords = []quality.CustomWordRule{{Word: "DV", Score: 1001}}
	if quality.ValidateRules(rules) == nil || quality.EvaluateCandidate(rules, "Film.DV", profile).Passed {
		t.Fatal("versioned policy accepted invalid word score")
	}
}

func TestPolicyWordsCannotReconstructStoredTitleEvidence(t *testing.T) {
	rules, profile := wordPolicy()
	rules.CustomWords = []quality.CustomWordRule{{Word: "Atmos", Required: true, Score: 1000}}
	if quality.EvaluateCandidate(rules, "Film (2020)/Film.mkv", profile).Passed {
		t.Fatal("candidate passed without required evidence")
	}
	got := quality.EvaluateStoredFile(rules, "Film (2020)/Film.mkv", profile)
	if !got.Passed || got.Score != 310 || got.PreferenceScore != 0 || got.MeetsCutoff {
		t.Fatalf("renaming changed stored quality: %+v", got)
	}
	rules.RejectTerms = []string{"CAM"}
	if quality.EvaluateStoredFile(rules, "Film.CAM.mkv", profile).Passed {
		t.Fatal("legacy stored-file negative gate lost")
	}
}

func TestPolicyWordsDoNotAuthorizeIntrinsicUpgrade(t *testing.T) {
	rules, candidate := wordPolicy()
	rules.CustomWords = []quality.CustomWordRule{{Word: "Atmos", Required: true, Score: 1000}}
	current := candidate
	current.Resolution = quality.Res720p
	got := quality.EvaluateUpgradeForMode(rules, current, candidate, quality.UpgradeStopAtCutoff)
	if !got.ScoresKnown || !got.ShouldUpgrade || got.CurrentScore != 210 || got.NewScore != 310 {
		t.Fatalf("intrinsic comparison inferred title: %+v", got)
	}
	same := quality.EvaluateUpgradeForMode(rules, candidate, candidate, quality.UpgradeStopAtCutoff)
	if !same.ScoresKnown || same.ShouldUpgrade {
		t.Fatalf("preference granted same-quality upgrade: %+v", same)
	}
}

func TestPolicyWordsPreserveLegacyRuleEncoding(t *testing.T) {
	const legacy = `{"cutoff_score":400,"prefer_hdr":false,"resolution_order":{"1080p":300},"source_order":{"HDTV":10},"reject_terms":null,"reject_groups":null}`
	var rules quality.PolicyRules
	if err := json.Unmarshal([]byte(legacy), &rules); err != nil {
		t.Fatal(err)
	}
	for _, words := range [][]quality.CustomWordRule{nil, {}} {
		rules.CustomWords = words
		raw, err := json.Marshal(rules)
		if err != nil || string(raw) != legacy {
			t.Fatalf("empty new field rewrote frozen rules: %s %v", raw, err)
		}
	}
	rules.CustomWords = []quality.CustomWordRule{{Word: "Atmos", Score: 100}}
	raw, err := json.Marshal(rules)
	if err != nil {
		t.Fatal(err)
	}
	var restored quality.PolicyRules
	if json.Unmarshal(raw, &restored) != nil || !reflect.DeepEqual(rules, restored) || quality.ValidateRules(restored) != nil {
		t.Fatal("new words lost across version encoding")
	}
}
