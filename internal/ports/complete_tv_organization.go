package ports

import (
	"context"
	"github.com/google/uuid"
)

// The command carries identities and confirmation, never caller-provided files.
type CompleteTVOrganizationCommand struct {
	TaskID                uuid.UUID `json:"task_id"`
	OperationID           uuid.UUID `json:"operation_id"`
	TrackingJobID         uuid.UUID `json:"tracking_job_id"`
	ExpectedEvidenceStamp string    `json:"expected_evidence_stamp"`
	NamingVersion         int64     `json:"naming_version,omitempty"`
	Confirmed             bool      `json:"confirmed"`
}
type CompleteTVOrganizationReceipt struct {
	TaskID uuid.UUID `json:"task_id"`
	PlanID uuid.UUID `json:"plan_id"`
	JobID  uuid.UUID `json:"job_id"`
	State  string    `json:"state"`
}
type CompleteTVOrganizationRepository interface {
	ConfirmCompleteTVOrganization(context.Context, *Actor, CompleteTVOrganizationCommand, string) (*CompleteTVOrganizationReceipt, error)
}
