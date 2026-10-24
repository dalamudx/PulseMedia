package ports

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"time"
)

const UntrackedOrganizationJobKind = "untracked_organization"

var ErrUntrackedOrganizationStale = errors.New("untracked organization material changed")

// The command references immutable intent; it cannot replace paths, action or coverage.
type UntrackedOrganizationInput struct {
	ConfirmationID uuid.UUID `json:"confirmation_id"`
}

type UntrackedOrganizationOperation struct {
	ID             uuid.UUID `json:"id"`
	ActorID        uuid.UUID `json:"actor_id"`
	ScopeID        uuid.UUID `json:"scope_id"`
	FileID         uuid.UUID `json:"file_id"`
	ConfirmationID uuid.UUID `json:"confirmation_id"`
	PlanID         uuid.UUID `json:"plan_id"`
	StepID         uuid.UUID `json:"step_id"`
	JobID          uuid.UUID `json:"job_id"`
	ScopeVersion   int64     `json:"scope_version"`
	FileRevision   int64     `json:"file_revision"`
	MaterialHash   string    `json:"material_hash"`
	CreatedAt      time.Time `json:"created_at"`
}

type UntrackedOrganizationMaterial struct {
	Operation    UntrackedOrganizationOperation `json:"operation"`
	Confirmation UntrackedConfirmationReceipt   `json:"confirmation"`
}

// Queueing authorizes a controlled attempt, never a client-side physical action.
type UntrackedOrganizationReceipt struct {
	CommandKey       string                         `json:"command_key"`
	Operation        UntrackedOrganizationOperation `json:"operation"`
	State            string                         `json:"state"`
	ExecutionAllowed bool                           `json:"execution_allowed"`
}

type UntrackedOrganizationView struct {
	Operation        UntrackedOrganizationOperation `json:"operation"`
	State            string                         `json:"state"`
	ReviewRequired   bool                           `json:"review_required"`
	CompletedFileID  *uuid.UUID                     `json:"completed_file_id"`
	RecordedAt       *time.Time                     `json:"recorded_at"`
	ExecutionAllowed bool                           `json:"execution_allowed"`
}

type UntrackedOrganizationCapture interface {
	UntrackedOrganizationMaterial() UntrackedOrganizationMaterial
}

type UntrackedOrganizationPreparation struct {
	Receipt *UntrackedOrganizationReceipt
	Capture UntrackedOrganizationCapture
}

type UntrackedOrganizationRepository interface {
	PrepareUntrackedOrganization(context.Context, *Actor, uuid.UUID, uuid.UUID, UntrackedOrganizationInput, string) (*UntrackedOrganizationPreparation, error)
	CommitUntrackedOrganization(context.Context, UntrackedOrganizationCapture) (*UntrackedOrganizationReceipt, error)
	ReadUntrackedOrganizationReceipt(context.Context, *Actor, uuid.UUID, uuid.UUID, string) (*UntrackedOrganizationReceipt, error)
	ReadUntrackedOrganization(context.Context, *Actor, uuid.UUID, uuid.UUID, uuid.UUID) (*UntrackedOrganizationView, error)
}

type UntrackedOrganizationExecutionRepository interface {
	OrganizeUntrackedFile(context.Context, JobLease) error
	RecoverUntrackedOrganizations(context.Context, int) (int, error)
}
