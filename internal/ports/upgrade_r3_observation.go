package ports

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"time"
)

var ErrMovieUpgradeObservationReview = errors.New("movie upgrade observation requires manual review; protection retained")

// These receipts report a read-only observation. They never authorize Add,
// tracking, ingestion or protection release.
type MovieUpgradeObservationReceipt struct {
	ObservationID    uuid.UUID `json:"observation_id"`
	OperationID      uuid.UUID `json:"operation_id"`
	TaskID           uuid.UUID `json:"task_id"`
	Owner            uuid.UUID `json:"owner"`
	Token            uuid.UUID `json:"token"`
	LeaseUntil       time.Time `json:"lease_until"`
	Status           string    `json:"status"`
	ExecutionAllowed bool      `json:"execution_allowed"`
}
type MovieUpgradeObservationBinding struct {
	ObservationID     uuid.UUID
	OperationID       uuid.UUID
	TaskID            uuid.UUID
	Owner             uuid.UUID
	Token             uuid.UUID
	ObservationSHA256 string
	PermitSHA256      string
}
type MovieUpgradeObservationProof any

type MovieUpgradeVerifiedObservation struct {
	Binding    MovieUpgradeObservationBinding
	Outcome    string
	ReturnedAt time.Time
}
type MovieUpgradeObservationVerifier interface {
	VerifyMovieUpgradeObservation(context.Context, MovieUpgradeObservationProof, MovieUpgradeObservationBinding) (MovieUpgradeVerifiedObservation, error)
}
type MovieUpgradeObservationResult struct {
	ObservationID    uuid.UUID `json:"observation_id"`
	OperationID      uuid.UUID `json:"operation_id"`
	TaskID           uuid.UUID `json:"task_id"`
	Outcome          string    `json:"outcome"`
	Disposition      string    `json:"disposition"`
	ExecutionAllowed bool      `json:"execution_allowed"`
}
type MovieUpgradeObservationSession interface {
	AdmitMovieUpgradeObservation(context.Context, *Actor, uuid.UUID, string) (*MovieUpgradeObservationReceipt, error)
	RecordMovieUpgradeObservation(context.Context, *Actor, string, MovieUpgradeObservationProof) (*MovieUpgradeObservationResult, error)
	Close(context.Context) error
}
type MovieUpgradeObservationRepository interface {
	OpenMovieUpgradeObservationSession(context.Context, uuid.UUID) (MovieUpgradeObservationSession, error)
	// Historical reads never acquire or replace an execution permit, and do not
	// require the original connection or an unexpired observation execution lease.
	ReadMovieUpgradeObservationAdmission(context.Context, *Actor, uuid.UUID, uuid.UUID, string) (*MovieUpgradeObservationReceipt, error)
	ReadMovieUpgradeObservationResult(context.Context, *Actor, uuid.UUID, string) (*MovieUpgradeObservationResult, error)
}
