package ports

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/google/uuid"
)

// Public preparation never grants permission to send. Internal lease/proof inputs
// are constructed by the service and are never accepted from an HTTP caller.
type PublicMovieUpgradePreparation struct {
	SubscriptionID uuid.UUID
	OperationID    uuid.UUID
	Confirmed      bool
}

type MovieUpgradePreparationWork struct {
	Target               media.MovieUpgradeTarget `json:"-"`
	EndpointURL          string                   `json:"-"`
	APIKeyEncrypted      []byte                   `json:"-"`
	DownloadURLEncrypted []byte                   `json:"-"`
}

type PublicMovieUpgradePreparationRepository interface {
	MovieUpgradePreparationRepository
	// A nil result with nil error is an authorized miss, never permission to send.
	ReadMovieUpgradePreparation(context.Context, *Actor, PublicMovieUpgradePreparation, string) (*MovieUpgradePreparationResult, error)
	LoadMovieUpgradePreparationWork(context.Context, *Actor, PublicMovieUpgradePreparation, MovieUpgradePreparationLease) (*MovieUpgradePreparationWork, error)
}

type MovieUpgradePreparationInspector interface {
	InspectMovieUpgradePreparation(context.Context, *MovieUpgradePreparationWork) (MovieUpgradeMetainfoProof, error)
}
