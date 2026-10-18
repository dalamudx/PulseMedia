package ports

import (
	"context"
	"github.com/google/uuid"
)

const MovieUpgradeTrackingJobKind = "movie_upgrade_tracking"

type MovieUpgradeTrackingJobRequest struct {
	OperationID uuid.UUID
	TaskID      uuid.UUID
	Lease       JobLease
}
type MovieUpgradeNativeJobTrackingRepository interface {
	TrackMovieUpgradeJob(context.Context, *Actor, MovieUpgradeTrackingJobRequest) (*MovieUpgradeTrackingResult, error)
}
type MovieUpgradeTrackingRuntime interface {
	ScheduleMovieUpgradeTracking(context.Context) (int, error)
	MovieUpgradeTrackingClaim(context.Context, JobLease) (*Actor, MovieUpgradeTrackingJobRequest, error)
}
