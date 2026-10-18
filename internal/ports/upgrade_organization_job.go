package ports

import (
	"context"
	"github.com/google/uuid"
	"time"
)

const MovieUpgradeOrganizationJobKind = "movie_upgrade_organization_execute"

// A new command queues a dedicated job; preparation receipts cannot execute it.
type QueueMovieUpgradeOrganization struct {
	OperationID   uuid.UUID `json:"operation_id"`
	PreparationID uuid.UUID `json:"preparation_id"`
	Confirmed     bool      `json:"confirmed"`
}
type MovieUpgradeOrganizationJobReceipt struct {
	CommandID        uuid.UUID `json:"command_id"`
	JobID            uuid.UUID `json:"job_id"`
	OperationID      uuid.UUID `json:"operation_id"`
	PreparationID    uuid.UUID `json:"preparation_id"`
	QueuedAt         time.Time `json:"queued_at"`
	Status           string    `json:"status"`
	ExecutionAllowed bool      `json:"execution_allowed"`
}
type MovieUpgradeOrganizationJobClaim struct {
	ClaimID          uuid.UUID `json:"claim_id"`
	JobID            uuid.UUID `json:"job_id"`
	CommandID        uuid.UUID `json:"command_id"`
	OperationID      uuid.UUID `json:"operation_id"`
	PreparationID    uuid.UUID `json:"preparation_id"`
	ClaimedAt        time.Time `json:"claimed_at"`
	Status           string    `json:"status"`
	ExecutionAllowed bool      `json:"execution_allowed"`
}

// Claim capture is one-time logical fencing, not a filesystem execution permit.
type MovieUpgradeOrganizationJobRepository interface {
	QueueMovieUpgradeOrganization(context.Context, *Actor, QueueMovieUpgradeOrganization, string) (*MovieUpgradeOrganizationJobReceipt, error)
	ReadMovieUpgradeOrganizationJobReceipt(context.Context, *Actor, uuid.UUID, string) (*MovieUpgradeOrganizationJobReceipt, error)
	CaptureMovieUpgradeOrganizationJob(context.Context, *Actor, JobLease) (*MovieUpgradeOrganizationJobClaim, error)
}
