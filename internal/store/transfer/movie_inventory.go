package transfer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/domain/naming"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"path"
	"path/filepath"
)

// No inventory denotes an existing direct-file movie. Directory sources require
// the immutable complete inventory and the same client configuration at every fence.
func validateMovieInventory(ctx context.Context, q *db.Queries, task db.DownloadTask) (int64, uuid.UUID, string, error) {
	inv, e := q.GetDownloadFileInventory(ctx, task.ID)
	if errors.Is(e, pgx.ErrNoRows) {
		return task.TotalBytes, uuid.Nil, "", nil
	}
	if e != nil {
		return 0, uuid.Nil, "", e
	}
	if task.State != "completed" || task.Progress != 1 || task.DownloadedBytes < task.TotalBytes || !task.ContentPath.Valid || inv.OperationID != task.OperationID || inv.ClientID != task.ClientID || inv.InfoHash != task.InfoHash || inv.TotalBytes != task.TotalBytes {
		return 0, uuid.Nil, "", ErrReviewRequired
	}
	var files []naming.InventoryFile
	if json.Unmarshal(inv.Files, &files) != nil {
		return 0, uuid.Nil, "", ErrReviewRequired
	}
	selected, remote, ok := naming.MovieDirectoryVideo(path.Clean(task.SavePath), inv.ContentPath, files, inv.TotalBytes)
	if !ok || remote != task.ContentPath.String {
		return 0, uuid.Nil, "", ErrReviewRequired
	}
	cfg, e := q.LockEpisodeSubmissionClient(ctx, task.ClientID)
	if e != nil {
		return 0, uuid.Nil, "", e
	}
	stamp, e := clientStamp(cfg)
	if e != nil || stamp != inv.ClientStamp {
		return 0, uuid.Nil, "", ErrReviewRequired
	}
	var mapping map[string]string
	if json.Unmarshal(cfg.RemotePathMap, &mapping) != nil {
		return 0, uuid.Nil, "", ErrReviewRequired
	}
	local := naming.TranslateRemotePath(mapping, remote)
	if !filepath.IsAbs(local) || filepath.Clean(local) != local {
		return 0, uuid.Nil, "", ErrReviewRequired
	}
	return selected.SizeBytes, task.ID, local, nil
}
func validateMovieInventoryEvidence(ctx context.Context, q *db.Queries, op uuid.UUID, f transferTargetEvidence) error {
	task, e := q.GetDownloadTaskByOpID(ctx, op)
	if errors.Is(e, pgx.ErrNoRows) && f.InventoryTaskID == uuid.Nil {
		return nil
	}
	if e != nil {
		return ErrReviewRequired
	}
	size, id, local, e := validateMovieInventory(ctx, q, task)
	if e != nil || id != f.InventoryTaskID || (id != uuid.Nil && (local != f.InventorySourcePath || size != f.InventorySizeBytes)) {
		return ErrReviewRequired
	}
	return nil
}

// Use the same canonical stamp as the frozen execution contract.
func clientStamp(c db.DownloadClient) (string, error) {
	raw, e := json.Marshal([]any{c.ID, c.ClientType, c.Url, c.Username, c.PasswordEncrypted, c.RemotePathMap})
	if e != nil {
		return "", e
	}
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:]), nil
}
