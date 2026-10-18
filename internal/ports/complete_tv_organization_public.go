package ports

import (
	"context"
	"github.com/google/uuid"
)

// CompleteTVOrganizationReferences confirms one completed download using only
// persisted references. It cannot supply file facts or an execution baseline.
type CompleteTVOrganizationReferences struct {
	TaskID        uuid.UUID `json:"task_id"`
	OperationID   uuid.UUID `json:"operation_id"`
	TrackingJobID uuid.UUID `json:"tracking_job_id"`
}

type CompleteTVOrganizationConfirmationRepository interface {
	ConfirmCompleteTVOrganizationReferences(context.Context, *Actor, CompleteTVOrganizationReferences, string) (*CompleteTVOrganizationReceipt, error)
}
