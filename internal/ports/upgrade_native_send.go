package ports

import (
	"context"
	"github.com/google/uuid"
)

// These are explicitly constructed trusted dependencies, never request callbacks.
// Dispatch proof is issued privately only after an unambiguous original-session commit.
type MovieUpgradeNativeDispatchProof any

type MovieUpgradeNativeDescriptor struct {
	OperationID  uuid.UUID
	TaskID       uuid.UUID
	ClientID     uuid.UUID
	ClientDigest string
	SavePath     string
	InfoHash     string
	RawSHA256    string
}

type MovieUpgradeNativePayload struct {
	DispatchID   uuid.UUID
	Binding      MovieUpgradeSendReturnBinding
	TorrentBytes []byte
}

type MovieUpgradeNativeDispatchVerifier interface {
	ConsumeMovieUpgradeDispatch(context.Context, MovieUpgradeNativeDispatchProof, MovieUpgradeNativeDescriptor) (MovieUpgradeNativePayload, error)
}

type MovieUpgradeNativeSendAttempt interface {
	MovieUpgradeSendReturnVerifier
	ExecuteMovieUpgradeSend(context.Context, MovieUpgradeNativeDispatchProof) (MovieUpgradeSendReturnProof, error)
	Close(context.Context) error
}

type MovieUpgradeNativeSendDriver interface {
	OpenMovieUpgradeNativeSend(context.Context, MovieUpgradeNativeDescriptor) (MovieUpgradeNativeSendAttempt, error)
}

type MovieUpgradeNativeSendRepository interface {
	SendMovieUpgrade(context.Context, *Actor, uuid.UUID, uuid.UUID, MovieUpgradeMetainfoProof, string) (*MovieUpgradeSendResultReceipt, error)
}
