package ports

import (
	"context"
	"github.com/google/uuid"
	"time"
)

type TransferRevisionPreview struct {
	PlanID            uuid.UUID `json:"plan_id"`
	TaskID            uuid.UUID `json:"task_id"`
	TargetRel         string    `json:"target_rel"`
	OriginalTargetRel string    `json:"original_target_rel"`
	SourceSize        int64     `json:"source_size"`
	MediaType         string    `json:"media_type,omitempty"`
	UnitKey           string    `json:"unit_key,omitempty"`
	Action            string    `json:"action,omitempty"`

	InputStamp string    `json:"input_stamp"`
	ObservedAt time.Time `json:"observed_at"`
	Revisable  bool      `json:"revisable"`
	Reason     string    `json:"reason"`
}
type TransferRevisionCommand struct {
	PlanID             uuid.UUID `json:"plan_id"`
	TargetRel          string    `json:"target_rel"`
	Action             string    `json:"action,omitempty"`
	ExpectedInputStamp string    `json:"expected_input_stamp"`
	Confirmed          bool      `json:"confirmed"`
	Reason             string    `json:"reason"`
}
type TransferRevisionReceipt struct {
	PreviousPlanID uuid.UUID `json:"previous_plan_id"`
	PlanID         uuid.UUID `json:"plan_id"`
	TaskID         uuid.UUID `json:"task_id"`
	JobID          uuid.UUID `json:"job_id"`
	State          string    `json:"state"`
}
type TransferRevisionRepository interface {
	PreviewTransferRevision(context.Context, *Actor, uuid.UUID, string) (*TransferRevisionPreview, error)
	CommitTransferRevision(context.Context, *Actor, TransferRevisionCommand, string) (*TransferRevisionReceipt, error)
}

// Action previews are a new optional capability. Existing hardlink repositories
// and their preview entry point retain their original contract.
type TransferActionRevisionRepository interface {
	PreviewTransferActionRevision(context.Context, *Actor, uuid.UUID, string, string) (*TransferRevisionPreview, error)
}
