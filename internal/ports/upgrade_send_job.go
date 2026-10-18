package ports

import (
	"context"
	"github.com/google/uuid"
	"time"
)

const MovieUpgradeSendJobKind = "movie_upgrade_native_send"

// QueueMovieUpgradeSend is a NEW explicit confirmation, not a historical receipt.
type QueueMovieUpgradeSend struct {
	// SubscriptionID binds a public route; zero preserves internal callers.
	SubscriptionID uuid.UUID `json:"-"`
	OperationID    uuid.UUID `json:"operation_id"`
	TaskID         uuid.UUID `json:"task_id"`
	Confirmed      bool      `json:"confirmed"`
}
type MovieUpgradeSendJobReceipt struct {
	OperationID      uuid.UUID `json:"operation_id"`
	TaskID           uuid.UUID `json:"task_id"`
	CommandID        uuid.UUID `json:"command_id"`
	JobID            uuid.UUID `json:"job_id"`
	QueuedAt         time.Time `json:"queued_at"`
	Status           string    `json:"status"`
	ExecutionAllowed bool      `json:"execution_allowed"`
}
type MovieUpgradeSendCommandRepository interface {
	QueueMovieUpgradeSend(context.Context, *Actor, QueueMovieUpgradeSend, string) (*MovieUpgradeSendJobReceipt, error)
}
type MovieUpgradeSendJobRequest struct {
	Lease       JobLease
	OperationID uuid.UUID
	TaskID      uuid.UUID
	CommandID   uuid.UUID
	// ExpectedClaim freezes the authorized running row before network fetch.
	ExpectedClaim []byte `json:"-"`
}
type MovieUpgradeNativeJobSendRepository interface {
	SendMovieUpgradeJob(context.Context, *Actor, MovieUpgradeSendJobRequest, MovieUpgradeMetainfoProof) (*MovieUpgradeSendResultReceipt, error)
}
