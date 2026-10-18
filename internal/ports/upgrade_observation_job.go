package ports

import (
	"context"
	"github.com/google/uuid"
)

const MovieUpgradeObservationJobKind = "movie_upgrade_observation"

// A job lease fences execution; the original user's current authority is also required.
type MovieUpgradeObservationJobRequest struct {
	Lease                   JobLease
	OperationID, TaskID     uuid.UUID
	AdmissionKey, ResultKey string
}

type MovieUpgradeNativeJobObservationRepository interface {
	ObserveMovieUpgradeJob(context.Context, *Actor, MovieUpgradeObservationJobRequest) (*MovieUpgradeObservationResult, error)
}
