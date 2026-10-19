package ports

import "github.com/google/uuid"

// AutomaticTVRoundCandidate comes from an immutable query/candidate proof.
// The repository independently fences membership and current SourceStamp.
type AutomaticTVRoundCandidate struct {
	Candidate           SearchCandidate
	QueryID             uuid.UUID
	QueryParametersHash string
	QuerySourceStamp    string
}

type AutomaticTVSelectionMaterial struct {
	Snapshot   AutomaticTVRoundSnapshot
	Candidates []AutomaticTVRoundCandidate
}

type AutomaticTVSelectionDecision struct {
	CandidateID         *uuid.UUID
	Score               int
	Reason              string
	IdentityRuleVersion string
	ParserVersion       string
}

// AutomaticTVSelector is pure. Its result is revalidated by the atomic
// finalization and preallocation boundary and is not execution permission.
type AutomaticTVSelector func(AutomaticTVSelectionMaterial) AutomaticTVSelectionDecision
