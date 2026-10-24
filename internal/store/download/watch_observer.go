package download

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

var _ ports.DownloadWatchObserver = (*Repository)(nil)

func (r *Repository) ObserveDownloadWatchTarget(ctx context.Context, target ports.DownloadWatchTarget) (*ports.DownloadExecutionTask, error) {
	return taskDTO(r.execution.ObserveDownloadWatchTarget(ctx, target))
}

func (m *Manager) ObserveDownloadWatchTarget(ctx context.Context, target ports.DownloadWatchTarget) (*db.DownloadTask, error) {
	return m.observeTask(ctx, target.OperationID, &target)
}

func (m *Manager) ObserveTask(ctx context.Context, opID uuid.UUID) (*db.DownloadTask, error) {
	return m.observeTask(ctx, opID, nil)
}

func (m *Manager) validateWatch(ctx context.Context, q *db.Queries, watch *ports.DownloadWatchTarget, lock bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if watch == nil {
		return nil
	}
	return postgres.ValidateDownloadWatchTarget(ctx, q, *watch, lock)
}

func (m *Manager) validateWatchTail(ctx context.Context, q *db.Queries, watch *ports.DownloadWatchTarget, observed db.DownloadTask) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if watch == nil {
		return nil
	}
	return postgres.ValidateDownloadWatchTargetAfterObservation(ctx, q, *watch, observed)
}

func (m *Manager) recordObservationState(ctx context.Context, task db.DownloadTask, state, reason string) (*db.DownloadTask, error) {
	return m.recordObservationStateGuarded(ctx, task, state, reason, nil)
}


// The outer pipeline owns the physical budget until these synchronous calls return.
// Each inventory probe, including the second torrent read, checks the same binding.
type watchReadClient struct {
	ports.DownloadClient
	manager *Manager
	target  ports.DownloadWatchTarget
}

func (c *watchReadClient) GetTorrent(ctx context.Context, hash string) (*ports.TorrentInfo, error) {
	if err := c.manager.validateWatch(ctx, c.manager.queries, &c.target, false); err != nil {
		return nil, err
	}
	info, err := c.DownloadClient.GetTorrent(ctx, hash)
	if fence := c.manager.validateWatch(ctx, c.manager.queries, &c.target, false); fence != nil {
		return nil, fence
	}
	return info, err
}

func (c *watchReadClient) GetTorrentFiles(ctx context.Context, hash string) ([]ports.TorrentFile, error) {
	if err := c.manager.validateWatch(ctx, c.manager.queries, &c.target, false); err != nil {
		return nil, err
	}
	provider, ok := c.DownloadClient.(ports.DownloadFileInventoryClient)
	if !ok {
		return nil, ErrReconcilePending
	}
	files, err := provider.GetTorrentFiles(ctx, hash)
	if fence := c.manager.validateWatch(ctx, c.manager.queries, &c.target, false); fence != nil {
		return nil, fence
	}
	return files, err
}

func (*watchReadClient) AddTorrent(context.Context, ports.AddTorrentParam) error {
	return ports.ErrDownloadWatchStale
}
func (*watchReadClient) RemoveTorrent(context.Context, string, bool) error {
	return ports.ErrDownloadWatchStale
}
func (*watchReadClient) ListTorrents(context.Context, string) ([]*ports.TorrentInfo, error) {
	return nil, ports.ErrDownloadWatchStale
}
func (*watchReadClient) Test(context.Context) error { return ports.ErrDownloadWatchStale }

// A failed network sample and a progress sample use the same locked binding fence.
func (m *Manager) writeWatchProgress(ctx context.Context, original db.DownloadTask, config db.DownloadClient, info *ports.TorrentInfo, watch *ports.DownloadWatchTarget) (*db.DownloadTask, error) {
	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := db.New(tx)
	if err = m.validateWatch(ctx, q, watch, true); err != nil {
		return nil, err
	}
	current, err := q.LockDownloadTask(ctx, original.ID)
	if err != nil {
		return nil, err
	}
	if !sameWatchObservationVersion(original, current) {
		return nil, ports.ErrDownloadWatchStale
	}
	observed := current
	if info != nil {
		observed, err = q.UpdateDownloadTaskProgress(ctx, db.UpdateDownloadTaskProgressParams{
			ID: original.ID, State: "tracking", Progress: info.Progress,
			TotalBytes: info.TotalBytes, DownloadedBytes: info.DownloadedBytes,
			ContentPath: pgtype.Text{String: info.ContentPath, Valid: info.ContentPath != ""},
		})
		if err != nil {
			return nil, err
		}
	}
	if m.recordRates {
		stamp, stampErr := postgres.DownloadRateClientStamp(config)
		if stampErr != nil {
			return nil, stampErr
		}
		var rate *int64
		if info != nil {
			rate = info.DownloadRateBytesPerSecond
		}
		if err = postgres.RecordDownloadRate(ctx, tx, observed, stamp, rate, "ordinary"); err != nil {
			return nil, err
		}
	}
	if err = m.commitWatchObservation(ctx, tx, q, watch, observed); err != nil {
		return nil, err
	}
	return &observed, nil
}

// Flush deferred checks before the tail fence, so they cannot mutate authority afterward.
func (m *Manager) commitWatchObservation(ctx context.Context, tx pgx.Tx, q *db.Queries, watch *ports.DownloadWatchTarget, observed db.DownloadTask) error {
	if watch != nil {
		if _, err := tx.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE"); err != nil {
			return err
		}
		if err := m.validateWatchTail(ctx, q, watch, observed); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
