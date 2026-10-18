package ports

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/google/uuid"
)

var ErrMovieUpgradeTrackingReview = errors.New("movie upgrade tracking requires manual review")

// Tracking observes a confirmed physical send. It never changes the original
// task or authorizes Add, ingestion, retirement, or protection release.
type MovieUpgradeTrackingDescriptor struct {
	Identity MovieUpgradeNativeObservationDescriptor `json:"identity"`
	Target   media.MovieUpgradeTarget                `json:"target"`
	Files    []media.TVTorrentFile                   `json:"files"`
}
type MovieUpgradeTrackingQueryProof interface{}
type MovieUpgradeTrackingProof interface{}
type MovieUpgradeVerifiedTracking struct {
	Binding    MovieUpgradeObservationBinding
	Outcome    string
	Reason     string
	ReturnedAt time.Time
	Evidence   json.RawMessage
}
type MovieUpgradeTrackingQueryVerifier interface {
	ConsumeMovieUpgradeTrackingQuery(context.Context, MovieUpgradeTrackingQueryProof, MovieUpgradeTrackingDescriptor) (MovieUpgradeObservationBinding, error)
}
type MovieUpgradeTrackingReturnVerifier interface {
	VerifyMovieUpgradeTracking(context.Context, MovieUpgradeTrackingProof, MovieUpgradeObservationBinding) (MovieUpgradeVerifiedTracking, error)
}
type MovieUpgradeTrackingAttempt interface {
	MovieUpgradeTrackingReturnVerifier
	QueryMovieUpgradeTracking(context.Context, MovieUpgradeTrackingQueryProof) (MovieUpgradeTrackingProof, error)
	Close(context.Context) error
}
type MovieUpgradeNativeTrackingDriver interface {
	OpenMovieUpgradeNativeTracking(context.Context, MovieUpgradeTrackingDescriptor) (MovieUpgradeTrackingAttempt, error)
}
type MovieUpgradeTrackingMetainfoInspector interface {
	MovieUpgradeMetainfoVerifier
	InspectMovieUpgradeTrackingMetainfo(context.Context, []byte, media.MovieUpgradeTarget) (MovieUpgradeMetainfoProof, error)
}
type MovieUpgradeTrackingResult struct {
	OperationID      uuid.UUID `json:"operation_id"`
	TaskID           uuid.UUID `json:"task_id"`
	ObservationID    uuid.UUID `json:"observation_id"`
	Outcome          string    `json:"outcome"`
	Reason           string    `json:"reason"`
	Disposition      string    `json:"disposition"`
	ExecutionAllowed bool      `json:"execution_allowed"`
}
type MovieUpgradeTrackingRepository interface {
	TrackMovieUpgrade(context.Context, *Actor, uuid.UUID, uuid.UUID, string, string) (*MovieUpgradeTrackingResult, error)
}
