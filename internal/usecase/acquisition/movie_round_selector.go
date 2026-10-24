package acquisition

import (
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

// SelectMovieRound preserves the finalizer's single-movie ranking semantics:
// evaluate admissible candidates in input order, accepting allowed quality even
// below the upgrade cutoff, and retain the first candidate on equal scores.
func SelectMovieRound(in ports.MovieRoundSelectionInput) ports.MovieRoundSelection {
	out := ports.MovieRoundSelection{
		BestScore: -1,
		Reason:    "no_qualified_candidate",
		Decisions: make([]ports.MovieRoundCandidateDecision, 0, len(in.Candidates)),
	}
	bestSelectionScore := 0
	for _, candidate := range in.Candidates {
		if !candidate.Admissible {
			out.Decisions = append(out.Decisions, ports.MovieRoundCandidateDecision{
				CandidateID: candidate.ID, Decision: "skip", Reason: "candidate_inadmissible",
			})
			continue
		}
		recognized := media.Recognize(candidate.Title)
		// Keep the original evaluator inputs, including its zero/default context
		// fields. Policy identity/revision describe Rules; they add no new gate.
		evaluation := media.ExplainMovieCandidate(media.CandidateRuleInput{
			Title: candidate.Title, TargetTitle: in.TargetTitle, Year: in.Year,
			Identities: in.Identities, SizeBytes: candidate.SizeBytes,
			HasReference: candidate.HasReference, SourceVersion: candidate.SourceVersion,
			Profile: recognized.Quality, Rules: in.Rules,
		})
		out.Decisions = append(out.Decisions, ports.MovieRoundCandidateDecision{
			CandidateID: candidate.ID, Decision: evaluation.Decision,
			Reason: evaluation.Reason, Score: evaluation.Score,
			MeetsCutoff: evaluation.MeetsCutoff, Breakdown: evaluation.Breakdown,
		})
		if evaluation.Decision != "accept" {
			continue
		}
		selectionScore := evaluation.Score + evaluation.PreferenceScore
		if out.SelectedCandidateID == nil || selectionScore > bestSelectionScore {
			id := candidate.ID
			out.SelectedCandidateID = &id
			out.BestScore = evaluation.Score
			bestSelectionScore = selectionScore
			out.Reason = ""
		}
	}
	return out
}

var _ ports.MovieRoundSelector = SelectMovieRound
