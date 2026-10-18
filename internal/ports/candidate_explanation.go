package ports

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/domain/subscription"
	"github.com/google/uuid"
	"time"
)

type CandidateExplanationInput struct {
	ID, IndexerID uuid.UUID
	Rule          media.CandidateRuleInput
	SourceCurrent bool
	Blocked       bool
	ObservedAt    time.Time
}
type CandidateExplanationSnapshot struct {
	SubscriptionID, WantedID, RoundID uuid.UUID
	IntentVersion, PolicyRevision     int64
	RoundState, RoundReason           string
	ObservedAt                        time.Time
	Status                            subscription.StatusReport
	UnitStatus                        subscription.UnitStatus
	Items                             []CandidateExplanationInput
	Limited                           bool
}
type CandidateExplanationItem struct {
	ID               uuid.UUID `json:"id"`
	IndexerID        uuid.UUID `json:"indexer_id"`
	Title            string    `json:"title"`
	Decision, Reason string
	Score            int
	MeetsCutoff      bool
	Breakdown        []string
	Available        bool
	Blocked          bool
	WaitReason       string
	InputDigest      string
	Cached           bool
}
type CandidateExplanationReport struct {
	SubscriptionID, WantedID, RoundID uuid.UUID
	IntentVersion, PolicyRevision     int64
	RoundState, RoundReason           string
	ObservedAt                        time.Time
	Items                             []CandidateExplanationItem
	Blockers                          []string
	Limited                           bool
}
type CandidateExplanationRepository interface {
	ReadCandidateExplanation(context.Context, *Actor, uuid.UUID, uuid.UUID) (CandidateExplanationSnapshot, error)
	ReadCandidateRuleCache(context.Context, string) (*media.CandidateRuleDecision, error)
	WriteCandidateRuleCache(context.Context, string, media.CandidateRuleDecision) error
}
