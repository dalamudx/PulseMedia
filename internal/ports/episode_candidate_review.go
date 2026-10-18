package ports

import (
	"context"
	"errors"
	"github.com/google/uuid"
)

var ErrEpisodeCandidateReviewConflict = errors.New("episode candidate review inputs changed")
var ErrInvalidEpisodeCandidateReview = errors.New("invalid episode candidate review")

type EpisodeCandidateReviewCommand struct {
	SubscriptionID     uuid.UUID `json:"subscription_id"`
	CandidateID        uuid.UUID `json:"candidate_id"`
	ExpectedInputStamp string    `json:"expected_input_stamp"`
	Confirmed          bool      `json:"confirmed"`
	Reason             string    `json:"reason"`
}
type EpisodeCandidateReviewReceipt struct {
	ID             uuid.UUID `json:"id"`
	SubscriptionID uuid.UUID `json:"subscription_id"`
	CandidateID    uuid.UUID `json:"candidate_id"`
	UnitID         uuid.UUID `json:"unit_id"`
	InputStamp     string    `json:"input_stamp"`
	Status         string    `json:"status"`
}
type EpisodeCandidateReviewRepository interface {
	ReadEpisodeCandidateReviewReceipt(context.Context, *Actor, EpisodeCandidateReviewCommand, string, string) (*EpisodeCandidateReviewReceipt, error)
	CommitEpisodeCandidateReview(context.Context, *Actor, EpisodeCandidateReviewCommand, string, string) (*EpisodeCandidateReviewReceipt, error)
}
