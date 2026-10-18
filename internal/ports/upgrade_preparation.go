package ports

import (
	"context"

	"github.com/google/uuid"
)

// A preparation lease is authority to attempt the dedicated DB preparation
// transaction. It never authorizes a downloader request.
type ClaimMovieUpgradePreparation struct {
	OperationID uuid.UUID
	Owner       uuid.UUID
}

type MovieUpgradePreparationLease struct {
	OperationID uuid.UUID `json:"operation_id"`
	Owner       uuid.UUID `json:"owner"`
	Token       uuid.UUID `json:"token"`
	// Informational only. The repository always rechecks database time.
	LeaseUntil string `json:"lease_until"`
}

type PrepareMovieUpgrade struct {
	OperationID uuid.UUID
	Owner       uuid.UUID
	Token       uuid.UUID
	Proof       MovieUpgradeMetainfoProof `json:"-"`
}

type MovieUpgradePreparationResult struct {
	OperationID      uuid.UUID `json:"operation_id"`
	TaskID           uuid.UUID `json:"task_id"`
	RawSHA256        string    `json:"raw_sha256"`
	Status           string    `json:"status"`
	ExecutionAllowed bool      `json:"execution_allowed"`
}

type MovieUpgradePreparationRepository interface {
	ClaimMovieUpgradePreparation(
		context.Context, *Actor, ClaimMovieUpgradePreparation,
	) (*MovieUpgradePreparationLease, error)

	PrepareMovieUpgrade(
		context.Context, *Actor, PrepareMovieUpgrade, string,
	) (*MovieUpgradePreparationResult, error)
}
