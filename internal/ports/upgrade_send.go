package ports

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"time"
)

var ErrMovieUpgradeSendObservation = errors.New("movie upgrade send requires observation; protection retained")

// Sessions pin the original connection. Close never deletes persisted authority.
type MovieUpgradeSendSession interface {
	ArmMovieUpgradeSend(context.Context, *Actor, uuid.UUID, MovieUpgradeMetainfoProof, string) (*MovieUpgradeSendArmReceipt, error)
	RecordMovieUpgradeSendResult(context.Context, *Actor, string, MovieUpgradeSendReturnProof) (*MovieUpgradeSendResultReceipt, error)
	Close(context.Context) error
}
type MovieUpgradeSendSessionRepository interface {
	OpenMovieUpgradeSendSession(context.Context, uuid.UUID) (MovieUpgradeSendSession, error)
}

// A receipt is historical evidence, never a replayable Add capability.
type MovieUpgradeSendArmReceipt struct {
	OperationID      uuid.UUID `json:"operation_id"`
	TaskID           uuid.UUID `json:"task_id"`
	Token            uuid.UUID `json:"token"`
	LeaseUntil       time.Time `json:"lease_until"`
	Status           string    `json:"status"`
	ExecutionAllowed bool      `json:"execution_allowed"`
}
