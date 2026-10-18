package ports

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"time"
)

var ErrMovieUpgradeOrganizationReview = errors.New("movie upgrade organization requires manual review")

type MovieUpgradeOrganizationExecutionResult struct {
	PermitID         uuid.UUID `json:"permit_id"`
	JobID            uuid.UUID `json:"job_id"`
	OperationID      uuid.UUID `json:"operation_id"`
	PreparationID    uuid.UUID `json:"preparation_id"`
	Status           string    `json:"status"`
	ReturnedAt       time.Time `json:"returned_at"`
	ExecutionAllowed bool      `json:"execution_allowed"`
}

// Execute accepts only an already captured original claim. It owns the native
// call and its private return evidence; callers cannot submit a result proof.
// A returned link is not ingestion or retirement permission.
type MovieUpgradeOrganizationExecutionRepository interface {
	ExecuteMovieUpgradeOrganization(context.Context, *Actor, JobLease) (*MovieUpgradeOrganizationExecutionResult, error)
	ReadMovieUpgradeOrganizationExecutionReceipt(context.Context, *Actor, uuid.UUID) (*MovieUpgradeOrganizationExecutionResult, error)
}
