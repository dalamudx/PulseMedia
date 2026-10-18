package ports

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/google/uuid"
)

// MovieUpgradeSendWork is ephemeral authorized input. Secrets and the claim are
// never job payloads, receipts, or JSON responses.
type MovieUpgradeSendWork struct {
	Request              MovieUpgradeSendJobRequest `json:"-"`
	Actor                *Actor                     `json:"-"`
	Target               media.MovieUpgradeTarget   `json:"-"`
	MetainfoSHA256       [32]byte                   `json:"-"`
	EndpointURL          string                     `json:"-"`
	APIKeyEncrypted      []byte                     `json:"-"`
	DownloadURLEncrypted []byte                     `json:"-"`
}
type MovieUpgradeSendWorkRepository interface {
	LoadMovieUpgradeSendWork(context.Context, MovieUpgradeSendJobRequest, uuid.UUID) (*MovieUpgradeSendWork, error)
}
type MovieUpgradeMetainfoInspector interface {
	InspectMovieUpgradeMetainfo(context.Context, []byte, media.MovieUpgradeTarget) (MovieUpgradeMetainfoProof, error)
}
type MovieUpgradeSendJobExecutor interface {
	ExecuteMovieUpgradeSendJob(context.Context, MovieUpgradeSendJobRequest, uuid.UUID) error
}
