package acquisition_test

import (
	"encoding/json"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
)

func TestAutomaticTVWordPreferenceKeepsAuthorityQualityScore(t *testing.T) {
	in := automaticTVMaterial(t)
	preferred := in.Candidates[0]
	preferred.Candidate.ID[14] = 1
	preferred.Candidate.Title = "Fixture.2020.S01E03.1080p.WEB-DL.Atmos.mkv"
	preferred.Candidate.Seeders = 0
	automaticTVProof(&preferred)
	in.Candidates = append(in.Candidates, preferred)
	var rules quality.PolicyRules
	if json.Unmarshal(in.Snapshot.Rules, &rules) != nil {
		t.Fatal("fixture rules")
	}
	rules.CustomWords = []quality.CustomWordRule{{Word: "Atmos", Score: 100}}
	in.Snapshot.Rules = automaticTVJSON(t, rules)
	before, _ := json.Marshal(in)
	got := acquisition.SelectAutomaticTVEpisode(in)
	after, _ := json.Marshal(in)
	if got.CandidateID == nil || *got.CandidateID != preferred.Candidate.ID || got.Score != 330 || string(before) != string(after) {
		t.Fatalf("TV preference changed authority score or input: %+v", got)
	}
	rules.CustomWords[0].Score = -1000
	in.Snapshot.Rules = automaticTVJSON(t, rules)
	in.Candidates = in.Candidates[1:]
	got = acquisition.SelectAutomaticTVEpisode(in)
	if got.CandidateID == nil || *got.CandidateID != preferred.Candidate.ID || got.Score != 330 {
		t.Fatalf("negative preference invalidated intrinsic quality: %+v", got)
	}
}

func TestAutomaticTVWordFiltersBeforeSeederTieBreak(t *testing.T) {
	in := automaticTVMaterial(t)
	var rules quality.PolicyRules
	if json.Unmarshal(in.Snapshot.Rules, &rules) != nil {
		t.Fatal("fixture rules")
	}
	for _, word := range []quality.CustomWordRule{{Word: "Atmos", Required: true}, {Word: "WEB-DL", Excluded: true}} {
		rules.CustomWords = []quality.CustomWordRule{word}
		in.Snapshot.Rules = automaticTVJSON(t, rules)
		got := acquisition.SelectAutomaticTVEpisode(in)
		if got.CandidateID != nil || got.Score != -1 {
			t.Fatalf("TV keyword rejection bypassed: %+v", got)
		}
	}
}
