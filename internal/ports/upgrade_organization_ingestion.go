package ports

import (
	"context"
	"github.com/google/uuid"
	"time"
)

// Ingestion consumes a trusted linked result and observes the files again. It
// never repeats the link or grants retirement/recycle permission.
type MovieUpgradeOrganizationIngestionResult struct {
	OperationID      uuid.UUID `json:"operation_id"`
	JobID            uuid.UUID `json:"job_id"`
	FileID           uuid.UUID `json:"file_id"`
	FactID           uuid.UUID `json:"fact_id"`
	IngestedAt       time.Time `json:"ingested_at"`
	Status           string    `json:"status"`
	ExecutionAllowed bool      `json:"execution_allowed"`
}

type MovieUpgradeOrganizationIngestionRepository interface {
	IngestMovieUpgradeOrganization(context.Context, *Actor, uuid.UUID) (*MovieUpgradeOrganizationIngestionResult, error)
	ReadMovieUpgradeOrganizationIngestionReceipt(context.Context, *Actor, uuid.UUID) (*MovieUpgradeOrganizationIngestionResult, error)
}

// Pending results survive process interruption and require a fresh command check.
type MovieUpgradeOrganizationIngestionWork struct {
	JobID uuid.UUID
	Actor Actor
}
type MovieUpgradeOrganizationRuntime interface {
	MovieUpgradeOrganizationClaim(context.Context, JobLease) (*Actor, error)
	PendingMovieUpgradeOrganizationIngestion(context.Context) ([]MovieUpgradeOrganizationIngestionWork, error)
}
