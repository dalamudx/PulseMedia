package ports

import (
	"context"
	"errors"
	"github.com/google/uuid"
)

var ErrSubmissionRetryConflict = errors.New("submission retry inputs changed")

type SubmissionRetryPreview struct {
	OperationID    uuid.UUID  `json:"operation_id"`
	JobID          *uuid.UUID `json:"job_id,omitempty"`
	CanRetry       bool       `json:"can_retry"`
	HoldReason     string     `json:"hold_reason,omitempty"`
	InputStamp     string     `json:"input_stamp,omitempty"`
	TargetTitle    string     `json:"target_title,omitempty"`
	RootName       string     `json:"root_name,omitempty"`
	DownloaderName string     `json:"downloader_name,omitempty"`
}
type SubmissionRetryCommand struct {
	OperationID, JobID uuid.UUID
	Key, ExpectedStamp string
	Acknowledged       bool
}
type SubmissionRetryReceipt struct {
	OperationID uuid.UUID `json:"operation_id"`
	JobID       uuid.UUID `json:"job_id"`
	State       string    `json:"state"`
}
type SubmissionRetryRepository interface {
	PreviewSubmissionRetry(context.Context, *Actor, uuid.UUID) (*SubmissionRetryPreview, error)
	RetrySubmission(context.Context, *Actor, SubmissionRetryCommand) (*SubmissionRetryReceipt, error)
}
