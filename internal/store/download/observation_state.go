package download

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/jackc/pgx/v5/pgtype"
)

// A network observation cannot regress a task advanced while the read was in
// flight. Version-producing writes use the same unit -> acquisition -> task order.
func (m *Manager) recordObservationState(ctx context.Context, snapshot db.DownloadTask, state, reason string) (*db.DownloadTask, error) {
	tx, e := m.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := m.queries.WithTx(tx)
	if _, e = q.LockDownloadTaskUnits(ctx, snapshot.ID); e != nil {
		return nil, e
	}
	if e = q.LockDownloadAcquisition(ctx, snapshot.OperationID); e != nil {
		return nil, e
	}
	current, e := q.LockDownloadTask(ctx, snapshot.ID)
	if e != nil {
		return nil, e
	}
	if current.State != snapshot.State || current.InfoHash != snapshot.InfoHash || current.SavePath != snapshot.SavePath || current.ClientID != snapshot.ClientID {
		return &current, nil
	}
	if state == "tracking" {
		current, e = q.TransitionTaskToTracking(ctx, current.ID)
	} else {
		current, e = q.TransitionTaskToUnknown(ctx, db.TransitionTaskToUnknownParams{ID: current.ID, Error: pgtype.Text{String: reason, Valid: true}})
	}
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return &current, nil
}
