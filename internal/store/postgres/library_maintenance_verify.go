package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func (s *LibraryMaintenanceStore) maintenanceConfiguredRoot(ctx context.Context, f maintenanceFrozen, state maintenanceSnapshot) error {
	current, e := s.files.OpenRoot(ctx, f.RootPath)
	if e != nil {
		return e
	}
	defer current.Close()
	id, e := current.Identity(ctx)
	if e != nil {
		return e
	}
	if id.Device != state.Baseline.Device || id.Inode != state.Baseline.Inode {
		return maintenanceChanged()
	}
	return nil
}
func (s *LibraryMaintenanceStore) maintenanceCleanupObserved(ctx context.Context, root ports.TransferRoot, f maintenanceFrozen, state maintenanceSnapshot) bool {
	if s.maintenanceConfiguredRoot(ctx, f, state) != nil {
		return false
	}
	id, e := root.Identity(ctx)
	if e != nil || id.Device != f.Root.Device || id.Inode != f.Root.Inode {
		return false
	}
	marker := state.Root.Marker
	if marker == "" {
		marker = ".pulsemedia_root"
	}
	m, e := root.Inspect(ctx, marker)
	if e != nil || m != f.Marker {
		return false
	}
	if _, e = root.Inspect(ctx, f.Source); !errors.Is(e, fs.ErrNotExist) {
		return false
	}
	if f.ReplacementID != nil {
		var replacement maintenanceSnapshot
		if json.Unmarshal(f.ReplacementState, &replacement) != nil {
			return false
		}
		id, e = root.Inspect(ctx, replacement.File.Rel)
		if e != nil || id != f.Replacement {
			return false
		}
	}
	return true
}

// Expected side effects are computed from the pre-call graph, not from rows that
// a late audit/event trigger might already have tampered with.
func maintenanceExpectedAfter(ctx context.Context, tx pgx.Tx, f maintenanceFrozen, c ports.LibraryMaintenanceCommand, out ports.LibraryMaintenanceResult, at time.Time, fact uuid.UUID, profile json.RawMessage) (json.RawMessage, error) {
	expected, e := db.New(tx).GetLibraryMaintenanceExpectedAfterState(ctx, db.GetLibraryMaintenanceExpectedAfterStateParams{Source: f.SourceState, Replacement: nullableMaintenanceJSON(f.ReplacementState), At: pgtype.Timestamptz{Time: at, Valid: true}, Action: c.Action, FileID: out.FileID, FactID: fact, Profile: nullableMaintenanceJSON(profile), Target: f.Target})
	return expected, e
}
func nullableMaintenanceJSON(v json.RawMessage) []byte {
	if len(v) == 0 {
		return nil
	}
	return v
}
