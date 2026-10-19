package download

import (
	"context"
	"errors"
	"math"
	"strings"

	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// NewRepositoryWithDownloadRates enables transactional native telemetry publication.
func NewRepositoryWithDownloadRates(pool *pgxpool.Pool, ids ports.IDGen, key []byte) *Repository {
	r := NewRepository(pool, ids, key)
	r.execution.recordRates = true
	return r
}

func (m *Manager) clientForRateSnapshot(ctx context.Context, config db.DownloadClient) (ports.DownloadClient, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.RLock()
	client, factory := m.clients[config.ID], m.factory
	m.mu.RUnlock()
	if client != nil {
		return client, nil
	}
	if factory == nil {
		return nil, ports.ErrUnavailable
	}
	plain, err := crypto.Decrypt(m.masterKey, config.PasswordEncrypted)
	if err != nil {
		return nil, ports.ErrUnavailable
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	client, err = factory(config.ClientType, config.Url, config.Username, string(plain))
	if err != nil || client == nil {
		return nil, ports.ErrUnavailable
	}
	return client, nil
}

func ratePublisherError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, ports.ErrDownloadTelemetryStale) || errors.Is(err, pgx.ErrNoRows) {
		return ports.ErrDownloadTelemetryStale
	}
	return ports.ErrUnavailable
}

// lockRateSnapshot fences the callback before any progress write, in client -> task order.
func lockRateSnapshot(ctx context.Context, tx pgx.Tx, original db.DownloadTask, config db.DownloadClient) (string, error) {
	q := db.New(tx)
	stamp, err := postgres.DownloadRateClientStamp(config)
	if err != nil {
		return "", err
	}
	currentConfig, err := q.LockDownloadClientForEdit(ctx, original.ClientID)
	if err != nil {
		return "", err
	}
	current, err := q.LockDownloadTask(ctx, original.ID)
	if err != nil {
		return "", err
	}
	currentStamp, err := postgres.DownloadRateClientStamp(currentConfig)
	if err != nil {
		return "", err
	}
	if config.ID != original.ClientID || currentStamp != stamp ||
		(currentConfig.ClientType != "qbittorrent" && currentConfig.ClientType != "transmission") ||
		current.ID != original.ID || current.OperationID != original.OperationID || current.ClientID != original.ClientID ||
		current.InfoHash != original.InfoHash || current.SavePath != original.SavePath ||
		original.State != "tracking" || current.State != "tracking" ||
		!original.UpdatedAt.Valid || !current.UpdatedAt.Valid || original.UpdatedAt.InfinityModifier != pgtype.Finite ||
		current.UpdatedAt.InfinityModifier != pgtype.Finite || !current.UpdatedAt.Time.Equal(original.UpdatedAt.Time) {
		return "", ports.ErrDownloadTelemetryStale
	}
	source, err := q.DownloadRateTaskSource(ctx, original.OperationID)
	if err != nil {
		return "", err
	}
	if source != "ordinary" {
		return "", ports.ErrDownloadTelemetryStale
	}
	return stamp, nil
}

func commitRateSnapshot(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE"); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (m *Manager) updateProgressWithRate(ctx context.Context, original db.DownloadTask, config db.DownloadClient, info *ports.TorrentInfo) (*db.DownloadTask, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if info == nil || original.InfoHash == "" || !strings.EqualFold(info.InfoHash, original.InfoHash) ||
		!sameOrChildPath(original.SavePath, info.SavePath) ||
		(info.ContentPath != "" && !sameOrChildPath(original.SavePath, info.ContentPath)) ||
		math.IsNaN(float64(info.Progress)) || math.IsInf(float64(info.Progress), 0) || info.Progress < 0 || info.Progress > 1 ||
		info.TotalBytes < 0 || info.DownloadedBytes < 0 || completedObservation(info) {
		return nil, ports.ErrDownloadTelemetryStale
	}
	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return nil, ratePublisherError(ctx, err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	stamp, err := lockRateSnapshot(ctx, tx, original, config)
	if err != nil {
		return nil, ratePublisherError(ctx, err)
	}
	observed, err := db.New(tx).UpdateDownloadTaskProgress(ctx, db.UpdateDownloadTaskProgressParams{
		ID: original.ID, State: "tracking", Progress: info.Progress,
		TotalBytes: info.TotalBytes, DownloadedBytes: info.DownloadedBytes,
		ContentPath: pgtype.Text{String: info.ContentPath, Valid: info.ContentPath != ""},
	})
	if err != nil {
		return nil, ratePublisherError(ctx, err)
	}
	if err = postgres.RecordDownloadRate(ctx, tx, observed, stamp, info.DownloadRateBytesPerSecond, "ordinary"); err != nil {
		return nil, ratePublisherError(ctx, err)
	}
	if err = commitRateSnapshot(ctx, tx); err != nil {
		return nil, ratePublisherError(ctx, err)
	}
	return &observed, nil
}

func (m *Manager) recordUnknownRate(ctx context.Context, original db.DownloadTask, config db.DownloadClient) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return ratePublisherError(ctx, err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	stamp, err := lockRateSnapshot(ctx, tx, original, config)
	if err != nil {
		return ratePublisherError(ctx, err)
	}
	if err = postgres.RecordDownloadRate(ctx, tx, original, stamp, nil, "ordinary"); err != nil {
		return ratePublisherError(ctx, err)
	}
	if err = commitRateSnapshot(ctx, tx); err != nil {
		return ratePublisherError(ctx, err)
	}
	return nil
}
