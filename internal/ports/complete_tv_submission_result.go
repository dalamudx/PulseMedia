package ports

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

// ErrCompleteTVResultStale leaves all physical protection intact. The callback
// cannot prove authority for the original Add; recovery must observe, never resend.
var ErrCompleteTVResultStale = errors.New("complete TV result authority changed; protection retained")

// CompleteTVSubmissionResultCommand records exactly one physical Add observation.
// Metainfo is the same owned byte proof used to prepare and send that Add.
// Only an affirmative acknowledgement with no Add error means tracking. Every
// error (including timeout/cancellation) or absent acknowledgement means unknown.
// AddError is never persisted or logged. A canceled writeback context rolls back;
// callers must use a live, bounded context to persist an Add cancellation result.
type CompleteTVSubmissionResultCommand struct {
	Lease               JobLease
	TaskID, OperationID uuid.UUID
	Metainfo            *CompleteTVMetainfoProof `json:"-"`
	Acknowledged        bool
	AddError            error `json:"-"`
}

type CompleteTVSubmissionResultReceipt struct {
	TaskID uuid.UUID `json:"task_id"`
	State  string    `json:"state"`
}

// CompleteTVSubmissionResultRepository has no network or preparation capability.
// Only the original running preparation lease may record a result. Completed,
// competing and reclaimed callbacks are stale, even when their outcome agrees.
type CompleteTVSubmissionResultRepository interface {
	RecordCompleteTVSubmissionResult(context.Context, CompleteTVSubmissionResultCommand) (*CompleteTVSubmissionResultReceipt, error)
}
