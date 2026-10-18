package ports

import (
	"context"
	"github.com/google/uuid"
	"time"
)

type SearchRound struct {
	ID, SubscriptionID, WantedItemID              uuid.UUID
	SnapshotIntentVersion, SnapshotPolicyRevision int64
	State                                         string
	Deadline                                      time.Time
	CandidateIds                                  []byte
	SelectedCandidateID                           *uuid.UUID
	AbortReason                                   *string
	CreatedAt                                     time.Time
	FinishedAt                                    *time.Time
	DecisionSnapshot                              []byte
	OperationID                                   *uuid.UUID
}
type SearchRoundStart struct {
	WantedID          uuid.UUID
	Window            time.Duration
	Scheduled, Shared bool
	// ManualNow reads the injected clock under the admission locks. Scheduled rounds use database time.
	ManualNow func() time.Time
	// QualifyShared is a pure usecase decision over the source read under the original locks.
	QualifyShared func(SearchSharedSource) bool
}
type SearchRoundRepository interface {
	StartRound(context.Context, SearchRoundStart) (*SearchRound, error)
	AddCandidate(context.Context, uuid.UUID, uuid.UUID) error
}
type MovieRoundFinalizer interface {
	FinalizeMovieRound(context.Context, *Actor, uuid.UUID, *RoundJobAuthority) (*RoundDecision, error)
}

// SearchSharedSource contains frozen rule inputs and grants no new execution authority.
type SearchSharedSource struct {
	Candidate  SearchCandidate
	Rules      []byte
	Title      string
	Year       int32
	Identities map[string]string
}
