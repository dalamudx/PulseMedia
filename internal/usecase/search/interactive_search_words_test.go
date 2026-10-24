package search_test

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/search"
)

func TestInteractiveWordsOrderBeforeSeedersAndKeepQuality(t *testing.T) {
	low := knownInteractiveProfile()
	high := low
	high.Resolution = quality.Res2160p
	a := rankedInteractiveRow("00000000-0000-0000-0000-000000000001", math.MaxInt32, persistedInteractiveProfile(t, high))
	b := rankedInteractiveRow("00000000-0000-0000-0000-000000000002", 0, persistedInteractiveProfile(t, low))
	a.Candidate.Title, b.Candidate.Title = "Movie.2160p", "Movie.1080p.Atmos"
	a.PreferenceScore, b.PreferenceScore = math.MaxInt, math.MinInt
	input := []ports.InteractiveSearchCandidate{a, b}
	before, _ := json.Marshal(input)
	policy := quality.DefaultPolicy()
	policy.CustomWords = []quality.CustomWordRule{{Word: "Atmos", Score: 200}}
	got, err := search.RankInteractiveSearchCandidates(input, policy)
	after, _ := json.Marshal(input)
	if err != nil || len(got) != 2 || got[0].Candidate.ID != b.Candidate.ID || got[0].QualityScore != 330 || got[0].PreferenceScore != 200 || string(before) != string(after) {
		t.Fatalf("global preference order: %+v %v", got, err)
	}
}

func TestInteractiveWordsStrictFilterAndLiteralBoundary(t *testing.T) {
	row := rankedInteractiveRow("00000000-0000-0000-0000-000000000001", math.MaxInt32, persistedInteractiveProfile(t, knownInteractiveProfile()))
	policy := quality.DefaultPolicy()
	policy.CustomWords = []quality.CustomWordRule{{Word: "DV", Required: true}, {Word: "CAM", Excluded: true}, {Word: "Atmos", Score: 1000}}
	for _, title := range []string{"Movie.DVD.Atmos", "Movie.DV.CAM.Atmos"} {
		row.Candidate.Title = title
		got, err := search.RankInteractiveSearchCandidates([]ports.InteractiveSearchCandidate{row}, policy)
		if err != nil || len(got) != 0 {
			t.Fatalf("hard filter bypassed: %s %+v %v", title, got, err)
		}
	}
	row.Candidate.Title = "Movie.DV.SCAM.Atmos"
	got, err := search.RankInteractiveSearchCandidates([]ports.InteractiveSearchCandidate{row}, policy)
	if err != nil || len(got) != 1 || got[0].PreferenceScore != 1000 {
		t.Fatalf("literal boundary: %+v %v", got, err)
	}
}

func TestInteractiveWordsCannotPromoteUnknownOverPenalizedKnown(t *testing.T) {
	good := rankedInteractiveRow("00000000-0000-0000-0000-000000000001", 0, persistedInteractiveProfile(t, knownInteractiveProfile()))
	unknown := good
	unknown.Candidate.ID[15] = 2
	unknown.Candidate.Seeders = math.MaxInt32
	unknown.Candidate.QualityProfile = []byte(`{}`)
	good.Candidate.Title, unknown.Candidate.Title = "Movie.Dubbed", "Movie.Atmos"
	policy := quality.DefaultPolicy()
	policy.CustomWords = []quality.CustomWordRule{{Word: "Dubbed", Score: -1000}, {Word: "Atmos", Score: 1000}}
	got, err := search.RankInteractiveSearchCandidates([]ports.InteractiveSearchCandidate{unknown, good}, policy)
	if err != nil || len(got) != 2 || got[0].Candidate.ID != good.Candidate.ID || got[0].PreferenceScore != -1000 || got[1].QualityScore != 0 || got[1].PreferenceScore != 0 {
		t.Fatalf("unknown evidence promoted: %+v %v", got, err)
	}
}

func TestInteractiveWordsRejectInvalidPolicyDeclarations(t *testing.T) {
	policy := quality.DefaultPolicy()
	policy.CustomWords = []quality.CustomWordRule{{Word: "DV", Score: 1}, {Word: "dv", Score: 1}}
	if _, err := search.RankInteractiveSearchCandidates(nil, policy); err == nil {
		t.Fatal("ambiguous word policy accepted")
	}
}
