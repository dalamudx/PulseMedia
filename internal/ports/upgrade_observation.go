package ports

import (
	"context"
	"github.com/google/uuid"
)

// Dedicated reservation observation extends the accepted read-only preview
// without turning its version fence into download authorization.
type FrozenUpgradeObservation struct {
	Preview            UpgradePreviewVersion `json:"preview"`
	ClientID           uuid.UUID             `json:"client_id"`
	ClientConfigDigest string                `json:"client_config_digest"`
}
type UpgradeObservationRepository interface {
	ReadFrozenUpgradeObservation(context.Context, *Actor, UpgradePreviewRequest) (*FrozenUpgradeObservation, error)
}
