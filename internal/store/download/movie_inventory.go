package download

import (
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/domain/naming"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"path"
	"strings"
)

func (m *Manager) observeMovieDirectory(ctx context.Context, task db.DownloadTask, client ports.DownloadClient, info *ports.TorrentInfo, stamp string) (*episodeInventoryObservation, error) {
	provider, ok := client.(ports.DownloadFileInventoryClient)
	if !ok {
		return nil, ErrReconcilePending
	}
	if path.Clean(info.SavePath) != path.Clean(task.SavePath) {
		return nil, ErrReconcilePending
	}
	inputs, e := m.queries.GetDownloadOrganizationInputs(ctx, task.ID)
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	if len(inputs) == 0 {
		return nil, ErrReconcilePending
	}
	for _, i := range inputs {
		if i.MediaType != "movie" || i.UnitKey != "movie" || i.UnitID != inputs[0].UnitID || i.RootID != inputs[0].RootID {
			return nil, ErrReconcilePending
		}
	}
	files, e := provider.GetTorrentFiles(ctx, task.InfoHash)
	if e != nil {
		return nil, e
	}
	_, selected, ok := naming.MovieDirectoryVideo(path.Clean(task.SavePath), info.ContentPath, files, info.TotalBytes)
	if !ok {
		return nil, ErrReconcilePending
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
	if inventoryClientStamp(cfg) != stamp {
		return nil, ErrReconcilePending
	}
	observed := &episodeInventoryObservation{files: files, clientStamp: stamp, directoryPath: info.ContentPath}
	info.ContentPath = selected
	return observed, nil
}
func (m *Manager) freezeMovieDirectory(ctx context.Context, q *db.Queries, task db.DownloadTask, info *ports.TorrentInfo, observed []*episodeInventoryObservation) error {
	if len(observed) != 1 || observed[0] == nil || observed[0].directoryPath == "" {
		return nil
	}
	o := observed[0]
	_, selected, ok := naming.MovieDirectoryVideo(path.Clean(task.SavePath), o.directoryPath, o.files, info.TotalBytes)
	if !ok || selected != info.ContentPath {
		return ErrReconcilePending
	}
	inputs, e := q.GetDownloadOrganizationInputs(ctx, task.ID)
	if e != nil {
		return ports.ErrUnavailable
	}
	if len(inputs) == 0 {
		return ErrReconcilePending
	}
	for _, i := range inputs {
		if i.MediaType != "movie" || i.UnitKey != "movie" || i.UnitID != inputs[0].UnitID || i.RootID != inputs[0].RootID {
			return ErrReconcilePending
		}
	}
	cfg, e := q.LockEpisodeSubmissionClient(ctx, task.ClientID)
	if e != nil {
		return ports.ErrUnavailable
	}
	if inventoryClientStamp(cfg) != o.clientStamp {
		return ErrReconcilePending
	}
	raw, e := json.Marshal(o.files)
	if e != nil {
		return ports.ErrBadResponse
	}
	return q.CreateDownloadFileInventory(ctx, db.CreateDownloadFileInventoryParams{TaskID: task.ID, OperationID: task.OperationID, ClientID: task.ClientID, InfoHash: strings.ToLower(task.InfoHash), TotalBytes: info.TotalBytes, ContentPath: o.directoryPath, ClientStamp: o.clientStamp, Files: raw})
}
