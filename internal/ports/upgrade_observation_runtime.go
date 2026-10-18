package ports

import (
	"context"
	"github.com/google/uuid"
)

// Scheduling never renews, adopts, or repeats an admitted observation.
type MovieUpgradeObservationRuntime interface {
	ScheduleMovieUpgradeObservations(context.Context) (int, error)
	MovieUpgradeObservationActor(context.Context, MovieUpgradeObservationJobRequest, uuid.UUID) (*Actor, error)
}
