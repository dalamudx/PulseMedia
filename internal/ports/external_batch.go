package ports

import (
	"context"
	"github.com/google/uuid"
	"time"
)

const ExternalBatchLimit = 20

type ExternalBatchSelection struct {
	FileID       uuid.UUID                  `json:"file_id"`
	Confirmation UntrackedConfirmationInput `json:"confirmation"`
}
type ExternalBatchInput struct {
	Rows []ExternalBatchSelection `json:"rows"`
}
type ExternalBatchRow struct {
	Selection ExternalBatchSelection  `json:"selection"`
	Scope     UntrackedScope          `json:"scope"`
	File      UntrackedDiscoveredFile `json:"file"`
	Review    UntrackedReview         `json:"review"`
}

// A batch is an immutable manifest, never an execution permit.
type ExternalBatch struct {
	ID               uuid.UUID          `json:"id"`
	ActorID          uuid.UUID          `json:"actor_id"`
	ScopeID          uuid.UUID          `json:"scope_id"`
	CommandKey       string             `json:"command_key"`
	CreatedAt        time.Time          `json:"created_at"`
	Rows             []ExternalBatchRow `json:"rows"`
	ExecutionAllowed bool               `json:"execution_allowed"`
}
type ExternalBatchRowStatus struct {
	Confirmation *UntrackedConfirmationReceipt `json:"confirmation"`
	Organization *UntrackedOrganizationReceipt `json:"organization"`
	Progress     *UntrackedOrganizationView    `json:"progress"`
}
type ExternalBatchView struct {
	Batch  ExternalBatch            `json:"batch"`
	Status []ExternalBatchRowStatus `json:"status"`
}
type ExternalBatchRepository interface {
	CreateExternalBatch(context.Context, *Actor, uuid.UUID, ExternalBatchInput, string) (*ExternalBatch, error)
	ReadExternalBatch(context.Context, *Actor, uuid.UUID, uuid.UUID, string) (*ExternalBatch, error)
}
type ExternalBatchReviews interface {
	Confirm(context.Context, *Actor, uuid.UUID, uuid.UUID, UntrackedConfirmationInput, string) (*UntrackedConfirmationReceipt, error)
	ConfirmationReceipt(context.Context, *Actor, uuid.UUID, uuid.UUID, string) (*UntrackedConfirmationReceipt, error)
}
type ExternalBatchOrganizations interface {
	Organize(context.Context, *Actor, uuid.UUID, uuid.UUID, UntrackedOrganizationInput, string) (*UntrackedOrganizationReceipt, error)
	Receipt(context.Context, *Actor, uuid.UUID, uuid.UUID, string) (*UntrackedOrganizationReceipt, error)
	Read(context.Context, *Actor, uuid.UUID, uuid.UUID, uuid.UUID) (*UntrackedOrganizationView, error)
}
