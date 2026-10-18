package ports

import (
	"context"
	"github.com/google/uuid"
	"time"
)

type TransferRecoveryPreview struct {
	PlanID      uuid.UUID `json:"plan_id"`
	TaskID      uuid.UUID `json:"task_id"`
	InputStamp  string    `json:"input_stamp"`
	ObservedAt  time.Time `json:"observed_at"`
	Recoverable bool      `json:"recoverable"`
	Reason      string    `json:"reason"`
}
type TransferRecoveryCommand struct {
	PlanID             uuid.UUID `json:"plan_id"`
	ExpectedInputStamp string    `json:"expected_input_stamp"`
	Confirmed          bool      `json:"confirmed"`
	Reason             string    `json:"reason"`
}
type TransferRecoveryReceipt struct {
	PlanID uuid.UUID `json:"plan_id"`
	TaskID uuid.UUID `json:"task_id"`
	JobID  uuid.UUID `json:"job_id"`
	State  string    `json:"state"`
}
type TransferRecoveryRepository interface {
	PreviewOriginalTransferRecovery(context.Context, *Actor, uuid.UUID) (*TransferRecoveryPreview, error)
	CommitOriginalTransferRecovery(context.Context, *Actor, TransferRecoveryCommand, string) (*TransferRecoveryReceipt, error)
}
