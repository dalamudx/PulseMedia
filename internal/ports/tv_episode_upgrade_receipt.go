package ports

import (
	"context"
	"github.com/google/uuid"
)

// Receipt recovery is read-only and never acquires a native client or physical slot.
type TVEpisodeUpgradeReceiptRepository interface {
	ReadTVEpisodeUpgradeCommand(context.Context, *Actor, uuid.UUID, string) (*TVEpisodeUpgradeResult, error)
}