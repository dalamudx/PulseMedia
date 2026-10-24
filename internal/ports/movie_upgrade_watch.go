package ports

import (
	"context"
	"github.com/google/uuid"
	"time"
)

// A hint can select the exact partial predecessor after its original 30-second gate.
// It cannot permit an early observation, sending, organization or reclamation.
type MovieUpgradeTrackingHintRequest struct {
	OperationID, TaskID, PriorObservationID uuid.UUID
	Binding                                 DownloadWatchBinding
	Hint                                    RootChangeHint
	SourceIdentity                          FileIdentity
	EventID, InstanceID                     uuid.UUID
	Sequence                                int64
	ReceivedAt, ManifestAt                  time.Time
}

type MovieUpgradeTrackingHintCapture interface {
	MovieUpgradeTrackingHintRequest() MovieUpgradeTrackingHintRequest
}

type MovieUpgradeTrackingHintScheduleResult struct {
	JobID      uuid.UUID
	Queued     bool
	RetryAfter time.Duration
}

type MovieUpgradeTrackingHintScheduler interface {
	ResolveMovieUpgradeTrackingHint(context.Context, MovieUpgradeTrackingHintRequest) (MovieUpgradeTrackingHintCapture, error)
	ValidateMovieUpgradeTrackingHint(context.Context, MovieUpgradeTrackingHintCapture) error
	ScheduleMovieUpgradeTrackingHint(context.Context, MovieUpgradeTrackingHintCapture) (*MovieUpgradeTrackingHintScheduleResult, error)
}
