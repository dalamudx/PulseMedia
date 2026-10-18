package acquisition

import (
	"reflect"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

// Synthetic ranking controls, not parser validation corpus or golden samples.
func roundSelectorFixture() ports.MovieRoundSelectionInput {
	return ports.MovieRoundSelectionInput{
		PolicyID: "movie", PolicyRevision: 3, TargetTitle: "Fight Club", Year: 1999,
		Identities: map[string]string{"tmdb.movie": "550"},
		Rules: quality.PolicyRules{CutoffScore: 100,
			ResolutionOrder: map[quality.Resolution]int{quality.Res720p: 10, quality.Res1080p: 20},
			SourceOrder:     map[quality.Source]int{quality.SourceBluray: 5}},
		Candidates: []ports.MovieRoundCandidate{
			{ID: uuid.MustParse("ffffffff-ffff-4fff-8fff-ffffffffffff"), Title: "Fight.Club.1999.720p.BluRay.x264-SPARKS.mkv", SizeBytes: 1000, HasReference: true, SourceVersion: 7, Admissible: true},
			{ID: uuid.MustParse("11111111-1111-4111-8111-111111111111"), Title: "Fight.Club.1999.1080p.BluRay.x264-SPARKS.mkv", SizeBytes: 2000, HasReference: true, SourceVersion: 7, Admissible: true},
		},
	}
}

func TestSelectMovieRoundRanking(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*ports.MovieRoundSelectionInput)
		winner int
		score  int
	}{
		{"strict_best", func(*ports.MovieRoundSelectionInput) {}, 1, 25},
		{"tie_first_input_not_uuid", func(in *ports.MovieRoundSelectionInput) { in.Candidates[0].Title = in.Candidates[1].Title }, 0, 25},
		{"tie_reversed_input", func(in *ports.MovieRoundSelectionInput) {
			in.Candidates[0].Title = in.Candidates[1].Title
			in.Candidates[0], in.Candidates[1] = in.Candidates[1], in.Candidates[0]
		}, 0, 25},
		{"zero_allowed", func(in *ports.MovieRoundSelectionInput) {
			in.Rules.ResolutionOrder[quality.Res720p] = 0
			in.Rules.ResolutionOrder[quality.Res1080p] = 0
			in.Rules.SourceOrder[quality.SourceBluray] = 0
		}, 0, 0},
		{"negative_first_score", func(in *ports.MovieRoundSelectionInput) {
			in.Rules.ResolutionOrder[quality.Res720p] = -10
			in.Rules.ResolutionOrder[quality.Res1080p] = -20
			in.Rules.SourceOrder[quality.SourceBluray] = 0
		}, 0, -10},
		{"inadmissible_highest_skipped", func(in *ports.MovieRoundSelectionInput) { in.Candidates[1].Admissible = false }, 0, 15},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := roundSelectorFixture()
			tc.change(&in)
			got := SelectMovieRound(in)
			if got.SelectedCandidateID == nil || *got.SelectedCandidateID != in.Candidates[tc.winner].ID || got.BestScore != tc.score || got.Reason != "" {
				t.Fatalf("selection = %+v; want input %d, score %d", got, tc.winner, tc.score)
			}
			if len(got.Decisions) != len(in.Candidates) {
				t.Fatalf("missing candidate decisions: %+v", got.Decisions)
			}
			if got.Decisions[tc.winner].MeetsCutoff {
				t.Fatal("fixture must select below cutoff")
			}
		})
	}
}

func TestSelectMovieRoundGates(t *testing.T) {
	for _, tc := range []struct {
		name             string
		change           func(*ports.MovieRoundCandidate)
		decision, reason string
	}{
		{"inadmissible", func(c *ports.MovieRoundCandidate) { c.Admissible = false }, "skip", "candidate_inadmissible"},
		{"unknown_quality", func(c *ports.MovieRoundCandidate) { c.Title = "Fight.Club.1999" }, "hold", "quality_unknown"},
		{"wrong_year", func(c *ports.MovieRoundCandidate) { c.Title = "Fight.Club.2000.1080p.BluRay.x264" }, "hold", "match_unproven"},
		{"conflicting_identity", func(c *ports.MovieRoundCandidate) { c.Title = "Fight.Club.1999.1080p.BluRay.x264.{tmdb-999}" }, "hold", "match_unproven"},
		{"episode_coverage", func(c *ports.MovieRoundCandidate) { c.Title = "Fight.Club.1999.S01E01.1080p.BluRay.x264.{tmdb-550}" }, "hold", "match_unproven"},
		{"pack_coverage", func(c *ports.MovieRoundCandidate) { c.Title = "Fight.Club.1999.COMPLETE.1080p.BluRay.x264.{tmdb-550}" }, "hold", "match_unproven"},
		{"unproven_source", func(c *ports.MovieRoundCandidate) { c.SourceVersion = 0 }, "hold", "source_unproven"},
		{"unknown_size", func(c *ports.MovieRoundCandidate) { c.SizeBytes = 0 }, "hold", "size_unknown"},
		{"missing_reference", func(c *ports.MovieRoundCandidate) { c.HasReference = false }, "hold", "reference_missing"},
		{"unsafe_title", func(c *ports.MovieRoundCandidate) { c.Title += ".passkey=secret" }, "hold", "unsafe_title"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := roundSelectorFixture()
			in.Candidates = in.Candidates[:1]
			tc.change(&in.Candidates[0])
			got := SelectMovieRound(in)
			if got.SelectedCandidateID != nil || got.BestScore != -1 || got.Reason != "no_qualified_candidate" {
				t.Fatalf("unexpected selection: %+v", got)
			}
			if len(got.Decisions) != 1 || got.Decisions[0].Decision != tc.decision || got.Decisions[0].Reason != tc.reason {
				t.Fatalf("decisions = %+v; want %s/%s", got.Decisions, tc.decision, tc.reason)
			}
		})
	}
}

func TestSelectMovieRoundEmpty(t *testing.T) {
	got := SelectMovieRound(ports.MovieRoundSelectionInput{})
	if got.SelectedCandidateID != nil || got.BestScore != -1 || got.Reason != "no_qualified_candidate" || len(got.Decisions) != 0 {
		t.Fatalf("unexpected empty selection: %+v", got)
	}
}

func TestSelectMovieRoundFrozenPolicyAndNoMutation(t *testing.T) {
	frozen := roundSelectorFixture()
	before := roundSelectorFixture()
	first := SelectMovieRound(frozen)
	changed := roundSelectorFixture()
	changed.PolicyRevision = 4
	changed.Rules.ResolutionOrder[quality.Res720p] = 50
	second := SelectMovieRound(changed)
	if first.SelectedCandidateID == nil || *first.SelectedCandidateID != frozen.Candidates[1].ID || first.BestScore != 25 {
		t.Fatalf("revision 3: %+v", first)
	}
	if second.SelectedCandidateID == nil || *second.SelectedCandidateID != changed.Candidates[0].ID || second.BestScore != 55 {
		t.Fatalf("revision 4: %+v", second)
	}
	if !reflect.DeepEqual(first, SelectMovieRound(frozen)) || !reflect.DeepEqual(frozen, before) {
		t.Fatal("later selection changed frozen inputs or original result")
	}
	changed.Rules.RejectTerms = []string{"Fight"}
	rejected := SelectMovieRound(changed)
	if rejected.SelectedCandidateID != nil || rejected.Decisions[0].Decision != "reject" || rejected.Decisions[0].Reason != "reject_term" {
		t.Fatalf("frozen reject rule ignored: %+v", rejected)
	}
}
