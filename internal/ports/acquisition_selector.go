package ports

import (
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/google/uuid"
)

// MovieRoundSelector ranks frozen, ordered material synchronously inside the
// caller's UoW. It performs no I/O and grants no submission authority.
type MovieRoundSelector func(MovieRoundSelectionInput) MovieRoundSelection

// MovieRoundSelectionInput carries the exact frozen revision, never live policy
// defaults. Candidates retain query order or the explicit candidate-ID order.
type MovieRoundSelectionInput struct {
	PolicyID       string
	PolicyRevision int64
	Rules          quality.PolicyRules
	TargetTitle    string
	Year           int
	Identities     map[string]string
	Candidates     []MovieRoundCandidate
}

// Admissible is established by the repository from the subscription block list
// and frozen indexer membership/configuration version. False always skips rules.
type MovieRoundCandidate struct {
	ID            uuid.UUID
	Title         string
	SizeBytes     int64
	HasReference  bool
	SourceVersion int64
	Admissible    bool
}

type MovieRoundSelection struct {
	SelectedCandidateID *uuid.UUID
	BestScore           int
	Reason              string
	Decisions           []MovieRoundCandidateDecision
}

type MovieRoundCandidateDecision struct {
	CandidateID uuid.UUID
	Decision    string
	Reason      string
	Score       int
	MeetsCutoff bool
	Breakdown   []string
}
