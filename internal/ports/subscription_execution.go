package ports

import (
	"context"
	"github.com/google/uuid"
	"time"
)

// Execution diagnostics deliberately contain no search text, paths, URLs or raw adapter data.
type SubscriptionExecution struct {
	SubscriptionID uuid.UUID                    `json:"subscription_id"`
	ObservedAt     time.Time                    `json:"observed_at"`
	Waiting        string                       `json:"waiting"`
	NextStep       string                       `json:"next_step"`
	Limited        bool                         `json:"limited"`
	Rounds         []SubscriptionExecutionRound `json:"rounds"`
}
type SubscriptionExecutionRound struct {
	ID            uuid.UUID                        `json:"id"`
	Kind          string                           `json:"kind"`
	State         string                           `json:"state"`
	CreatedAt     time.Time                        `json:"created_at"`
	Deadline      time.Time                        `json:"deadline"`
	IntentVersion int64                            `json:"intent_version"`
	Reason        string                           `json:"reason"`
	Sources       []SubscriptionExecutionSource    `json:"sources"`
	Candidates    []SubscriptionExecutionCandidate `json:"candidates"`
	Limited       bool                             `json:"limited"`
}
type SubscriptionExecutionSource struct {
	IndexerID uuid.UUID                   `json:"indexer_id"`
	State     string                      `json:"state"`
	Pages     []SubscriptionExecutionPage `json:"pages"`
}
type SubscriptionExecutionPage struct {
	QueryID        uuid.UUID  `json:"query_id"`
	Page           int32      `json:"page"`
	State          string     `json:"state"`
	Results        int32      `json:"results"`
	OffsetVerified bool       `json:"offset_verified"`
	ReceiptPresent bool       `json:"receipt_present"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
}
type SubscriptionExecutionCandidate struct {
	ID      uuid.UUID `json:"id"`
	Outcome string    `json:"outcome"`
	Reason  string    `json:"reason"`
}
type SubscriptionExecutionRepository interface {
	ReadSubscriptionExecution(context.Context, *Actor, uuid.UUID) (*SubscriptionExecution, error)
}
