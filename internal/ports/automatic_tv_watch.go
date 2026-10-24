package ports

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// The hint selects a normal observation only after its original next_poll_at.
// ReceivedAt uses the local Clock; ManifestAt is the DB horizon of the source list.
// Hint contains only kind/path; metadata is carried explicitly below.
type AutomaticTVObservationHintRequest struct {
	OperationID, TaskID, PriorJobID, SendAttemptID uuid.UUID
	Binding                                        DownloadWatchBinding
	Hint                                           RootChangeHint
	SourceIdentity                                 FileIdentity
	EventID, InstanceID                            uuid.UUID
	Sequence                                       int64
	ReceivedAt, ManifestAt                         time.Time
}

// Captures are repository-issued and cannot be reconstructed by a caller.
type AutomaticTVObservationHintCapture interface {
	AutomaticTVObservationHintRequest() AutomaticTVObservationHintRequest
}

type AutomaticTVObservationHintScheduleResult struct {
	JobID      uuid.UUID
	Queued     bool
	RetryAfter time.Duration
}

type AutomaticTVObservationHintScheduler interface {
	ResolveAutomaticTVObservationHint(context.Context, AutomaticTVObservationHintRequest) (AutomaticTVObservationHintCapture, error)
	ScheduleAutomaticTVObservationHint(context.Context, AutomaticTVObservationHintCapture) (*AutomaticTVObservationHintScheduleResult, error)
}
