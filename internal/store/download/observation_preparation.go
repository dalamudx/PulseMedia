package download

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/domain/naming"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"math"
	"strings"
)

func (r *Repository) PrepareDownloadObservation(ctx context.Context, op uuid.UUID, reconcile bool, watch *ports.DownloadWatchTarget) (*ports.PreparedDownloadObservation, error) {
	return r.execution.PrepareDownloadObservation(ctx, op, reconcile, watch)
}
func (m *Manager) PrepareDownloadObservation(ctx context.Context, op uuid.UUID, reconcile bool, watch *ports.DownloadWatchTarget) (*ports.PreparedDownloadObservation, error) {
	if watch != nil {
		copied := *watch
		watch = &copied
	}
	if err := m.validateWatch(ctx, m.queries, watch, false); err != nil {
		return nil, err
	}
	if err := m.requireNoFrozenUpgrade(ctx, op); err != nil {
		return nil, err
	}
	task, err := m.queries.GetDownloadTaskByOpID(ctx, op)
	if err != nil {
		return nil, err
	}
	if err = m.requireGenericObservation(ctx, op); err != nil {
		return nil, err
	}
	dto, _ := taskDTO(&task, nil)
	p := &ports.PreparedDownloadObservation{Task: dto}
	if (reconcile && task.State != "submitting" && task.State != "unknown") || (!reconcile && task.State != "tracking") {
		return p, nil
	}
	release, err := m.acquireClientBudget(ctx, task.ClientID)
	if err != nil {
		return nil, err
	}
	retained := false
	defer func() {
		if !retained {
			release()
		}
	}()
	if err = m.validateWatch(ctx, m.queries, watch, false); err != nil {
		return nil, err
	}
	var config db.DownloadClient
	var client ports.DownloadClient
	if !reconcile {
		config, err = m.queries.GetDownloadClientByID(ctx, task.ClientID)
		if err != nil {
			return nil, ports.ErrUnavailable
		}
	}
	if !reconcile && (m.recordRates || watch != nil) {
		client, err = m.clientForRateSnapshot(ctx, config)
	} else {
		client, err = m.clientFor(ctx, task.ClientID)
	}
	if err != nil {
		return nil, err
	}
	if watch != nil {
		client = &watchReadClient{DownloadClient: client, manager: m, target: *watch}
	}
	p.Client = client
	p.Release = release
	p.Complete = func(c context.Context, result ports.DownloadObservationResult) (*ports.DownloadExecutionTask, error) {
		if reconcile {
			return taskDTO(m.completeReconciliation(c, task, result))
		}
		return taskDTO(m.completeOrdinaryObservation(c, task, config, watch, result))
	}
	if !reconcile {
		p.InventoryRequired = func(c context.Context, info *ports.TorrentInfo) (bool, error) {
			if info == nil || task.InfoHash == "" || !strings.EqualFold(task.InfoHash, info.InfoHash) || !sameOrChildPath(task.SavePath, info.SavePath) || (info.ContentPath != "" && !sameOrChildPath(task.SavePath, info.ContentPath)) || !completedObservation(info) {
				return false, nil
			}
			_, e := m.queries.GetEpisodeExecutionAuthority(c, task.OperationID)
			if e == nil {
				return true, nil
			}
			if !errors.Is(e, pgx.ErrNoRows) {
				return false, ports.ErrUnavailable
			}
			return info.ContentPath != "" && !naming.IsVideoPath(info.ContentPath), nil
		}
	}
	retained = true
	return p, nil
}

func (m *Manager) completeReconciliation(ctx context.Context, task db.DownloadTask, result ports.DownloadObservationResult) (*db.DownloadTask, error) {
	info, err := result.Info, result.Err
	if err == nil && info != nil && task.InfoHash == "" {
		hash := strings.ToLower(info.InfoHash)
		_, e := hex.DecodeString(hash)
		if (len(hash) != 40 && len(hash) != 64) || e != nil {
			info = nil
		} else {
			task, err = m.queries.SetObservedDownloadInfoHash(ctx, db.SetObservedDownloadInfoHashParams{ID: task.ID, InfoHash: hash})
		}
	}
	if err != nil {
		if !errors.Is(err, ports.ErrNotFound) {
			return nil, err
		}
	}
	if info == nil || (task.InfoHash != "" && !strings.EqualFold(info.InfoHash, task.InfoHash)) {
		// No observation (or the adapter returned the wrong torrent): keep the
		// reservation and await operator review, never infer that Add failed.
		_, _ = m.queries.IncrementReconcileCount(ctx, task.ID)
		return &task, ErrReconcilePending
	}

	// Verify save path matches (已存在任务复用且不改路径)
	if !sameOrChildPath(task.SavePath, info.SavePath) ||
		(info.ContentPath != "" && !sameOrChildPath(task.SavePath, info.ContentPath)) {
		_, _ = m.recordObservationState(ctx, task, "unknown", "downloader save path mismatch")
		return nil, ErrPathMismatch
	}

	if math.IsNaN(float64(info.Progress)) || math.IsInf(float64(info.Progress), 0) || info.Progress < 0 || info.Progress > 1 || info.TotalBytes < 0 || info.DownloadedBytes < 0 {
		return nil, ErrReconcilePending
	}
	// Successfully reconciled: transition to tracking
	_, err = m.recordObservationState(ctx, task, "tracking", "")
	if err != nil {
		return nil, fmt.Errorf("failed to recover task to tracking: %w", err)
	}

	// Update latest progress
	finalTask, err := m.queries.UpdateDownloadTaskProgress(ctx, db.UpdateDownloadTaskProgressParams{
		ID:              task.ID,
		State:           "tracking",
		Progress:        info.Progress,
		TotalBytes:      info.TotalBytes,
		DownloadedBytes: info.DownloadedBytes,
		ContentPath:     pgtype.Text{String: info.ContentPath, Valid: info.ContentPath != ""},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to update reconciled task progress: %w", err)
	}

	return &finalTask, nil
}

func (m *Manager) completeOrdinaryObservation(ctx context.Context, task db.DownloadTask, configSnapshot db.DownloadClient, watch *ports.DownloadWatchTarget, result ports.DownloadObservationResult) (*db.DownloadTask, error) {
	info, err := result.Info, result.Err
	if errors.Is(err, ports.ErrNotFound) {
		_, err = m.recordObservationStateGuarded(ctx, task, "unknown", "torrent no longer visible; manual reconciliation required", watch)
		if err != nil {
			return nil, err
		}
		return nil, ErrReconcilePending
	}
	if err != nil {
		if watch != nil {
			if _, fenceErr := m.writeWatchProgress(ctx, task, configSnapshot, nil, watch); fenceErr != nil {
				return nil, fenceErr
			}
			return nil, err
		}
		if m.recordRates && ctx.Err() == nil {
			if rateErr := m.recordUnknownRate(ctx, task, configSnapshot); rateErr != nil {
				return nil, rateErr
			}
		}
		return nil, err
	}
	if info == nil || task.InfoHash == "" || !strings.EqualFold(info.InfoHash, task.InfoHash) ||
		!sameOrChildPath(task.SavePath, info.SavePath) ||
		(info.ContentPath != "" && !sameOrChildPath(task.SavePath, info.ContentPath)) ||
		math.IsNaN(float64(info.Progress)) || math.IsInf(float64(info.Progress), 0) || info.Progress < 0 || info.Progress > 1 || info.TotalBytes < 0 || info.DownloadedBytes < 0 {
		_, err = m.recordObservationStateGuarded(ctx, task, "unknown", "download identity or path requires review", watch)
		if err != nil {
			return nil, err
		}
		return nil, ErrReconcilePending
	}
	if completedObservation(info) {
		inventory, e := m.completeEpisodeInventory(ctx, task, info, result.Files, result.Second)
		if e != nil {
			return nil, e
		}
		if inventory == nil && info.ContentPath != "" && !naming.IsVideoPath(info.ContentPath) {
			inventory, e = m.completeMovieDirectory(ctx, task, info, inventoryClientStamp(configSnapshot), result.Files, result.Second)
			if e != nil {
				return nil, e
			}
		}
		return m.completeForOrganizationGuarded(ctx, task, info, watch, inventory)
	}
	if watch != nil {
		return m.writeWatchProgress(ctx, task, configSnapshot, info, watch)
	}
	if m.recordRates {
		return m.updateProgressWithRate(ctx, task, configSnapshot, info)
	}
	observed, err := m.queries.UpdateDownloadTaskProgress(ctx, db.UpdateDownloadTaskProgressParams{
		ID: task.ID, State: "tracking", Progress: info.Progress,
		TotalBytes: info.TotalBytes, DownloadedBytes: info.DownloadedBytes,
		ContentPath: pgtype.Text{String: info.ContentPath, Valid: info.ContentPath != ""},
	})
	if err != nil {
		return nil, err
	}
	return &observed, nil
}
