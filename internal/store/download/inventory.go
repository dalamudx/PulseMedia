package download

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/jackc/pgx/v5"
	"math"
	"path"
	"strings"
)

type episodeInventoryObservation struct {
	files         []ports.TorrentFile
	clientStamp   string
	directoryPath string
}

func inventoryClientStamp(c db.DownloadClient) string {
	raw, _ := json.Marshal([]any{c.ID, c.ClientType, c.Url, c.Username, c.PasswordEncrypted, c.RemotePathMap})
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}
func (m *Manager) observeEpisodeInventory(ctx context.Context, task db.DownloadTask, client ports.DownloadClient, info *ports.TorrentInfo) (*episodeInventoryObservation, error) {
	record, e := m.queries.GetEpisodeExecutionAuthority(ctx, task.OperationID)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	var frozen ports.EpisodeExecutionSnapshot
	if json.Unmarshal(record.Snapshot, &frozen) != nil || !frozen.ValidNumberingShape() || frozen.ClientID != task.ClientID || frozen.CandidateID != task.CandidateID {
		return nil, ErrReconcilePending
	}
	provider, ok := client.(ports.DownloadFileInventoryClient)
	if !ok {
		return nil, ErrReconcilePending
	}
	files, e := provider.GetTorrentFiles(ctx, task.InfoHash)
	if e != nil {
		return nil, e
	}
	if e = validateInventory(files, info.TotalBytes); e != nil {
		return nil, e
	}
	second, e := client.GetTorrent(ctx, task.InfoHash)
	if e != nil {
		return nil, e
	}
	if second == nil || !completedObservation(second) || !strings.EqualFold(second.InfoHash, info.InfoHash) || second.SavePath != info.SavePath || second.ContentPath != info.ContentPath || second.TotalBytes != info.TotalBytes {
		return nil, ErrReconcilePending
	}
	cfg, e := m.queries.GetDownloadClientByID(ctx, task.ClientID)
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	stamp := inventoryClientStamp(cfg)
	if stamp != frozen.ClientStamp {
		return nil, ErrReconcilePending
	}
	if info.ContentPath == "" && len(files) == 1 {
		info.ContentPath = path.Join(info.SavePath, files[0].Name)
		if !sameOrChildPath(task.SavePath, info.ContentPath) {
			return nil, ErrReconcilePending
		}
	}
	return &episodeInventoryObservation{files: files, clientStamp: stamp}, nil
}
func validateInventory(files []ports.TorrentFile, total int64) error {
	if len(files) == 0 || len(files) > 1000 || total <= 0 {
		return ErrReconcilePending
	}
	seen := map[string]bool{}
	ids := map[int32]bool{}
	var sum int64
	for _, f := range files {
		if f.Index < 0 || ids[f.Index] || seen[f.Name] || f.Name == "" || len(f.Name) > 1024 || path.Clean(f.Name) != f.Name || path.IsAbs(f.Name) || f.Name == "." || f.Name == ".." || strings.HasPrefix(f.Name, "../") || strings.ContainsAny(f.Name, "\\:\x00\r\n") || strings.Contains(strings.ToLower(f.Name), "token=") || strings.Contains(strings.ToLower(f.Name), "passkey=") || f.SizeBytes < 0 || f.SizeBytes > math.MaxInt64-sum || f.Progress != 1 {
			return ErrReconcilePending
		}
		seen[f.Name] = true
		ids[f.Index] = true
		sum += f.SizeBytes
	}
	if sum != total {
		return ErrReconcilePending
	}
	return nil
}
func (m *Manager) freezeEpisodeInventory(ctx context.Context, q *db.Queries, task db.DownloadTask, info *ports.TorrentInfo, observed []*episodeInventoryObservation) error {
	record, e := q.GetEpisodeExecutionAuthority(ctx, task.OperationID)
	if errors.Is(e, pgx.ErrNoRows) {
		return m.freezeMovieDirectory(ctx, q, task, info, observed)
	}
	if e != nil {
		return ports.ErrUnavailable
	}
	if len(observed) != 1 || observed[0] == nil || validateInventory(observed[0].files, info.TotalBytes) != nil {
		return ErrReconcilePending
	}
	var frozen ports.EpisodeExecutionSnapshot
	if json.Unmarshal(record.Snapshot, &frozen) != nil || !frozen.ValidNumberingShape() || record.ClientID != task.ClientID || record.CandidateID != task.CandidateID || frozen.ClientStamp != observed[0].clientStamp {
		return ErrReconcilePending
	}
	client, e := q.LockEpisodeSubmissionClient(ctx, task.ClientID)
	if e != nil {
		return ports.ErrUnavailable
	}
	if inventoryClientStamp(client) != frozen.ClientStamp {
		return ErrReconcilePending
	}
	raw, e := json.Marshal(observed[0].files)
	if e != nil {
		return ports.ErrBadResponse
	}
	return q.CreateDownloadFileInventory(ctx, db.CreateDownloadFileInventoryParams{TaskID: task.ID, OperationID: task.OperationID, ClientID: task.ClientID, InfoHash: strings.ToLower(task.InfoHash), TotalBytes: info.TotalBytes, ContentPath: info.ContentPath, ClientStamp: frozen.ClientStamp, Files: raw})
}
