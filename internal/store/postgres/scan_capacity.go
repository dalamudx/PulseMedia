package postgres

import (
	"context"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/jackc/pgx/v5/pgtype"
)

// Capacity is sealed with the existing configuration, physical identity and job
// checks. It never mutates the frozen root row or grants scan success on its own.
func recordScanCapacity(ctx context.Context, q *db.Queries, in ports.ScanSeal, replay bool) error {
	if in.Capacity == nil {
		return nil
	}
	value := in.Capacity
	stamp := pgtype.Timestamptz{Time: value.SampledAt, Valid: !value.SampledAt.IsZero()}
	if !stamp.Valid {
		return ports.ErrScanObservationStale
	}
	if replay {
		// Compare after binding the DB timestamp type: offsets and nanoseconds must not
		// create a representation mismatch against the persisted instant.
		matches, err := q.RootCapacityObservationMatches(ctx, db.RootCapacityObservationMatchesParams{ScanRunID: in.RunID, TotalBytes: value.TotalBytes, AvailableBytes: value.AvailableBytes, SampledAt: stamp})
		if err != nil {
			return err
		}
		if !matches {
			return ports.ErrScanObservationStale
		}
		return nil
	}
	n, err := q.InsertRootCapacityObservation(ctx, db.InsertRootCapacityObservationParams{ScanRunID: in.RunID, TotalBytes: value.TotalBytes, AvailableBytes: value.AvailableBytes, SampledAt: stamp})
	if err != nil {
		return err
	}
	if n != 1 {
		return ports.ErrScanObservationStale
	}
	return nil
}
