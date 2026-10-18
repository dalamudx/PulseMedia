package ports

import (
	"context"
	"github.com/google/uuid"
	"time"
)

// Preparation freezes a reviewed hardlink plan without authorizing execution.
type PrepareMovieUpgradeOrganization struct {
	OperationID     uuid.UUID `json:"operation_id"`
	AuthorizationID uuid.UUID `json:"authorization_id"`
	Confirmed       bool      `json:"confirmed"`
}
type MovieUpgradeOrganizationPreparation struct {
	ID               uuid.UUID `json:"preparation_id"`
	OperationID      uuid.UUID `json:"operation_id"`
	AuthorizationID  uuid.UUID `json:"authorization_id"`
	TaskID           uuid.UUID `json:"task_id"`
	PlanID           uuid.UUID `json:"plan_id"`
	StepID           uuid.UUID `json:"step_id"`
	Status           string    `json:"status"`
	PreparedAt       time.Time `json:"prepared_at"`
	ExecutionAllowed bool      `json:"execution_allowed"`
}
type MovieUpgradeOrganizationPreparationRepository interface {
	PrepareMovieUpgradeOrganization(context.Context, *Actor, PrepareMovieUpgradeOrganization, string) (*MovieUpgradeOrganizationPreparation, error)
	ReadMovieUpgradeOrganizationPreparationReceipt(context.Context, *Actor, uuid.UUID, string) (*MovieUpgradeOrganizationPreparation, error)
}
