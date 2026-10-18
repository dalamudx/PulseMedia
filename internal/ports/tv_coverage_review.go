package ports

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/google/uuid"
)

var ErrTVCoverageReviewConflict = errors.New("TV coverage review evidence changed")
var ErrInvalidTVCoverageReview = errors.New("invalid TV coverage candidate review")

type TVCoverageCandidateReviewCommand struct {
	SubscriptionID     uuid.UUID `json:"subscription_id"`
	CandidateID        uuid.UUID `json:"candidate_id"`
	ExpectedInputStamp string    `json:"expected_input_stamp"`
	Confirmed          bool      `json:"confirmed"`
	Reason             string    `json:"reason"`
}
type TVCoverageCandidateReviewObservation struct {
	SubscriptionID uuid.UUID `json:"subscription_id"`
	CandidateID    uuid.UUID `json:"candidate_id"`
	InputStamp     string    `json:"input_stamp"`
	MaterialStamp  string    `json:"material_stamp"`
	// Material excludes mutable files/facts/reservations and contains no secret references.
	Material         json.RawMessage      `json:"-"`
	Facts            json.RawMessage      `json:"-"`
	Rows             []media.NumberingRow `json:"rows"`
	CanReview        bool                 `json:"can_review"`
	HoldReason       string               `json:"hold_reason"`
	ExecutionAllowed bool                 `json:"execution_allowed"`
}
type TVCoverageCandidateReviewReceipt struct {
	ID             uuid.UUID `json:"id"`
	SubscriptionID uuid.UUID `json:"subscription_id"`
	CandidateID    uuid.UUID `json:"candidate_id"`
	InputStamp     string    `json:"input_stamp"`
	MaterialStamp  string    `json:"material_stamp"`
	Status         string    `json:"status"`
}
type TVCoverageCandidateReviewRepository interface {
	ReadTVCoverageCandidate(context.Context, *Actor, uuid.UUID, uuid.UUID) (*TVCoverageCandidateReviewObservation, error)
	ReadTVCoverageCandidateReviewReceipt(context.Context, *Actor, TVCoverageCandidateReviewCommand, string, string) (*TVCoverageCandidateReviewReceipt, error)
	CommitTVCoverageCandidateReview(context.Context, *Actor, TVCoverageCandidateReviewCommand, string, string) (*TVCoverageCandidateReviewReceipt, error)
}
