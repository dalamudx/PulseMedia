package postgres

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
)

type tvUpgradeQuery interface {
	db.DBTX
}

// RejectTVEpisodeUpgradeOperation is called before ordinary task/receipt shortcuts.
// Historical schemas can omit the new table; surviving dedicated demand still denies.
func RejectTVEpisodeUpgradeOperation(ctx context.Context, q tvUpgradeQuery, op uuid.UUID) error {
	queries := db.New(q)
	value, e := queries.CheckTVEpisodeUpgradeSchemaInstalled(ctx)
	if e != nil {
		return e
	}
	installed, ok := value.(bool)
	if !ok {
		return ports.ErrTVEpisodeUpgradeObservation
	}
	var denied bool
	if installed {
		result, err := queries.CheckTVEpisodeUpgradeOperationIsolated(ctx, op)
		if err != nil {
			return err
		}
		if !result.Valid {
			return ports.ErrTVEpisodeUpgradeObservation
		}
		denied = result.Bool
	} else {
		denied, e = queries.CheckTVEpisodeUpgradeDedicatedDemand(ctx, op)
		if e != nil {
			return e
		}
	}
	if denied {
		return ports.ErrTVEpisodeUpgradeObservation
	}
	return nil
}
