package download

import (
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/jackc/pgx/v5/pgtype"
)

func completedObservation(info *ports.TorrentInfo) bool {
	if info.Progress != 1 || info.TotalBytes <= 0 || info.DownloadedBytes < info.TotalBytes {
		return false
	}
	switch info.State {
	case "uploading", "stalledUP", "queuedUP", "pausedUP", "forcedUP", "stoppedUP", "seeding", "completed":
		return true
	default:
		return false
	}
}

// Download completion and its durable organization work must never split.
func (m *Manager) completeForOrganizationGuarded(ctx context.Context, snapshot db.DownloadTask, info *ports.TorrentInfo, watch *ports.DownloadWatchTarget, inventory ...*episodeInventoryObservation) (*db.DownloadTask, error) {
	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := m.queries.WithTx(tx)
	if err = m.validateWatch(ctx, q, watch, true); err != nil {
		return nil, err
	}
	if _, err = q.LockDownloadTaskUnits(ctx, snapshot.ID); err != nil {
		return nil, err
	}
	if err = q.LockDownloadAcquisition(ctx, snapshot.OperationID); err != nil {
		return nil, err
	}
	if _, err = q.LockDownloadClientForEdit(ctx, snapshot.ClientID); err != nil {
		return nil, err
	}
	current, err := q.LockDownloadTask(ctx, snapshot.ID)
	if err != nil {
		return nil, err
	}
	if current.State != "tracking" {
		return &current, nil
	}
	if current.InfoHash != snapshot.InfoHash || current.SavePath != snapshot.SavePath || current.ClientID != snapshot.ClientID {
		return nil, ErrReconcilePending
	}
	if err = m.freezeEpisodeInventory(ctx, q, current, info, inventory); err != nil {
		return nil, err
	}
	completed, err := q.CompleteDownloadForOrganization(ctx, db.CompleteDownloadForOrganizationParams{ID: current.ID, TotalBytes: info.TotalBytes, DownloadedBytes: info.DownloadedBytes, ContentPath: pgtype.Text{String: info.ContentPath, Valid: info.ContentPath != ""}})
	if err != nil {
		return nil, err
	}
	if _, err = q.EnqueueDownloadOrganization(ctx, db.EnqueueDownloadOrganizationParams{ID: m.idgen.NewUUID(), TaskID: current.ID}); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(map[string]any{"task_id": current.ID, "operation_id": current.OperationID})
	if err != nil {
		return nil, err
	}
	if _, err = q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: m.idgen.NewUUID(), ActorType: "system", ActorID: current.OperationID.String(), Action: "download.completed", TargetType: "download_task", TargetID: current.ID.String(), Payload: payload}); err != nil {
		return nil, err
	}
	if err = m.commitWatchObservation(ctx, tx, q, watch, completed); err != nil {
		return nil, err
	}
	return &completed, nil
}
