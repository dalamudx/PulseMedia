package ports

import (
	"context"
	"github.com/google/uuid"
)

// CompleteTVExecutor is the controlled runtime only; it grants no acquisition or ingestion authority.
type CompleteTVExecutor interface {
	ObserveCompleteTVTracking(context.Context, CompleteTVTrackingRepository, TorrentMetainfoInspector, CompleteTVTrackingRequest) (*CompleteTVTrackingReceipt, error)
	SendCompleteTV(context.Context, JobLease, uuid.UUID) (*CompleteTVSubmissionResultReceipt, error)
	ObserveCompleteTV(context.Context, CompleteTVObservationRepository, CompleteTVObservationRequest) (*CompleteTVObservationReceipt, error)
}

// CompleteTVRecoveryRepository schedules observation, never infers remote absence or releases protection.
type CompleteTVRecoveryRepository interface {
	RecoverCompleteTVJobs(context.Context) error
}

// CompleteTVTrackingScheduler creates fresh read-only tracking work only.
type CompleteTVTrackingScheduler interface {
	ScheduleCompleteTVTracking(context.Context) error
}
