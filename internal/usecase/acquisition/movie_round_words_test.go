package acquisition

import (
	"encoding/json"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/quality"
)

func TestMovieRoundWordPreferenceKeepsQualityReceipt(t *testing.T) {
	in := roundSelectorFixture()
	in.Candidates[0].Title = "Fight.Club.1999.720p.BluRay.Atmos.x264-SPARKS.mkv"
	in.Rules.CustomWords = []quality.CustomWordRule{{Word: "Atmos", Score: 100}}
	before, _ := json.Marshal(in)
	got := SelectMovieRound(in)
	after, _ := json.Marshal(in)
	if got.SelectedCandidateID == nil || *got.SelectedCandidateID != in.Candidates[0].ID || got.BestScore != 15 || got.Decisions[0].MeetsCutoff || string(before) != string(after) {
		t.Fatalf("preference changed quality receipt or input: %+v", got)
	}
	in.Rules.CustomWords[0].Score = -100
	got = SelectMovieRound(in)
	if got.SelectedCandidateID == nil || *got.SelectedCandidateID != in.Candidates[1].ID || got.BestScore != 25 {
		t.Fatalf("negative preference ignored: %+v", got)
	}
}

func TestMovieRoundWordFiltersCannotBeOutscored(t *testing.T) {
	in := roundSelectorFixture()
	in.Candidates[0].Title = "Fight.Club.1999.720p.BluRay.Atmos.x264-SPARKS.mkv"
	in.Rules.CustomWords = []quality.CustomWordRule{{Word: "Atmos", Required: true}, {Word: "SPARKS", Excluded: true}, {Word: "BluRay", Score: 1000}}
	got := SelectMovieRound(in)
	if got.SelectedCandidateID != nil || got.BestScore != -1 {
		t.Fatalf("score bypassed strict word rejection: %+v", got)
	}
	in.Rules.CustomWords = in.Rules.CustomWords[:1]
	got = SelectMovieRound(in)
	if got.SelectedCandidateID == nil || *got.SelectedCandidateID != in.Candidates[0].ID || got.BestScore != 15 {
		t.Fatalf("required word not enforced: %+v", got)
	}
}
