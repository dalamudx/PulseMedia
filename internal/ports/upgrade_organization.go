package ports

import (
	"context"
	"github.com/google/uuid"
	"time"
)

// A completed native observation is evidence for a new explicit command, never
// permission to invoke the generic transfer or retirement entry points.
type MovieUpgradeOrganizationReview struct {
	OperationID        uuid.UUID `json:"operation_id"`
	ObservationID      uuid.UUID `json:"observation_id"`
	SubscriptionID     uuid.UUID `json:"subscription_id"`
	TaskID             uuid.UUID `json:"task_id"`
	RootID             uuid.UUID `json:"root_id"`
	UnitID             uuid.UUID `json:"unit_id"`
	CurrentFileID      uuid.UUID `json:"current_file_id"`
	CurrentFactID      uuid.UUID `json:"current_fact_id"`
	SourceRelativePath string    `json:"source_relative_path"`
	SourceSize         int64     `json:"source_size"`
	InputStamp         string    `json:"input_stamp"`
	CurrentScore       int       `json:"current_score"`
	NewScore           int       `json:"new_score"`
	ExecutionAllowed   bool      `json:"execution_allowed"`
}
type AuthorizeMovieUpgradeOrganization struct {
	OperationID   uuid.UUID `json:"operation_id"`
	ObservationID uuid.UUID `json:"observation_id"`
	InputStamp    string    `json:"input_stamp"`
	Confirmed     bool      `json:"confirmed"`
}
type MovieUpgradeOrganizationAuthorization struct {
	ID               uuid.UUID `json:"authorization_id"`
	OperationID      uuid.UUID `json:"operation_id"`
	ObservationID    uuid.UUID `json:"observation_id"`
	TaskID           uuid.UUID `json:"task_id"`
	Status           string    `json:"status"`
	AuthorizedAt     time.Time `json:"authorized_at"`
	ExecutionAllowed bool      `json:"execution_allowed"`
}
type MovieUpgradeOrganizationRepository interface {
	ReadMovieUpgradeOrganization(context.Context, *Actor, uuid.UUID) (*MovieUpgradeOrganizationReview, error)
	AuthorizeMovieUpgradeOrganization(context.Context, *Actor, AuthorizeMovieUpgradeOrganization, string) (*MovieUpgradeOrganizationAuthorization, error)
	ReadMovieUpgradeOrganizationReceipt(context.Context, *Actor, uuid.UUID, string) (*MovieUpgradeOrganizationAuthorization, error)
}
