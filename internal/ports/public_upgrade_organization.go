package ports

import (
	"context"
	"github.com/google/uuid"
	"time"
)

type MovieUpgradeOrganizationState struct {
	OperationID      uuid.UUID  `json:"operation_id"`
	Status           string     `json:"status"`
	AuthorizationID  *uuid.UUID `json:"authorization_id,omitempty"`
	PreparationID    *uuid.UUID `json:"preparation_id,omitempty"`
	JobID            *uuid.UUID `json:"job_id,omitempty"`
	CurrentFileID    uuid.UUID  `json:"current_file_id"`
	NewFileID        *uuid.UUID `json:"new_file_id,omitempty"`
	RetiredAt        *time.Time `json:"retired_at,omitempty"`
	RetainedUntil    *time.Time `json:"retained_until,omitempty"`
	ExecutionAllowed bool       `json:"execution_allowed"`
}
type PublicMovieUpgradeOrganizationRepository interface {
	ReviewMovieUpgradeOrganization(context.Context, *Actor, uuid.UUID, uuid.UUID) (*MovieUpgradeOrganizationReview, error)
	ReadMovieUpgradeOrganizationState(context.Context, *Actor, uuid.UUID, uuid.UUID) (*MovieUpgradeOrganizationState, error)
	AuthorizeMovieUpgradeOrganization(context.Context, *Actor, uuid.UUID, AuthorizeMovieUpgradeOrganization, string) (*MovieUpgradeOrganizationAuthorization, error)
	PrepareMovieUpgradeOrganization(context.Context, *Actor, uuid.UUID, PrepareMovieUpgradeOrganization, string) (*MovieUpgradeOrganizationPreparation, error)
	QueueMovieUpgradeOrganization(context.Context, *Actor, uuid.UUID, QueueMovieUpgradeOrganization, string) (*MovieUpgradeOrganizationJobReceipt, error)
}
