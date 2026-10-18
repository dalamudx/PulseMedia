package ports

import (
	"context"
	"github.com/google/uuid"
	"time"
)

// Dispatch records an irreversible intent to send, but performs no network I/O.
// Its public receipt cannot authorize or repeat an Add.
type MovieUpgradeSendDispatchReceipt struct {
	OperationID      uuid.UUID `json:"operation_id"`
	DispatchID       uuid.UUID `json:"dispatch_id"`
	TaskID           uuid.UUID `json:"task_id"`
	DispatchedAt     time.Time `json:"dispatched_at"`
	Status           string    `json:"status"`
	ExecutionAllowed bool      `json:"execution_allowed"`
}

type MovieUpgradeSendDispatchSession interface {
	MovieUpgradeSendSession
	DispatchMovieUpgradeSend(context.Context, *Actor, MovieUpgradeMetainfoProof, string) (*MovieUpgradeSendDispatchReceipt, error)
}

type MovieUpgradeSendDispatchRepository interface {
	OpenMovieUpgradeSendDispatchSession(context.Context, uuid.UUID) (MovieUpgradeSendDispatchSession, error)
}
