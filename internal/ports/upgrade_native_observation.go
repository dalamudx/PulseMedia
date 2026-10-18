package ports

import (
	"context"
	"github.com/google/uuid"
)

// A descriptor and a historical receipt confer no native query capability.
type MovieUpgradeNativeObservationDescriptor struct {
	OperationID, TaskID, ClientID                  uuid.UUID
	ClientDigest, InfoHash, OperationTag, SavePath string
	TotalBytes                                     int64
}
type MovieUpgradeNativeQueryProof any
type MovieUpgradeNativeQueryVerifier interface {
	ConsumeMovieUpgradeQuery(context.Context, MovieUpgradeNativeQueryProof, MovieUpgradeNativeObservationDescriptor) (MovieUpgradeObservationBinding, error)
}
type MovieUpgradeNativeObservationAttempt interface {
	QueryMovieUpgrade(context.Context, MovieUpgradeNativeQueryProof) (MovieUpgradeObservationProof, error)
	MovieUpgradeObservationVerifier
	Close(context.Context) error
}
type MovieUpgradeNativeObservationDriver interface {
	OpenMovieUpgradeNativeObservation(context.Context, MovieUpgradeNativeObservationDescriptor) (MovieUpgradeNativeObservationAttempt, error)
}
type MovieUpgradeNativeObservationRepository interface {
	ObserveMovieUpgrade(context.Context, *Actor, uuid.UUID, uuid.UUID, string, string) (*MovieUpgradeObservationResult, error)
}

// This read contract deliberately exposes no downloader mutation methods.
type MovieUpgradeTorrentObserver interface {
	ListMovieUpgradeTorrents(context.Context, string) ([]*TorrentInfo, error)
}
