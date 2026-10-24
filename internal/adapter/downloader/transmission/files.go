package transmission

import (
	"context"
	"encoding/hex"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"path"
	"strings"
)

var _ ports.DownloadFileInventoryClient = (*Client)(nil)

type torrentFileObservation struct {
	Hash  *string `json:"hashString"`
	Files []struct {
		Name     *string `json:"name"`
		Length   *int64  `json:"length"`
		Complete *int64  `json:"bytesCompleted"`
	} `json:"files"`
	Stats []struct {
		Complete *int64 `json:"bytesCompleted"`
		Wanted   *bool  `json:"wanted"`
	} `json:"fileStats"`
}

func (c *Client) GetTorrentFiles(ctx context.Context, hash string) ([]ports.TorrentFile, error) {
	if len(hash) != 40 && len(hash) != 64 {
		return nil, ports.ErrBadResponse
	}
	if _, e := hex.DecodeString(hash); e != nil {
		return nil, ports.ErrBadResponse
	}
	var result struct {
		Torrents []torrentFileObservation `json:"torrents"`
	}
	if e := c.doRPC(ctx, "torrent-get", map[string]any{"fields": []string{"hashString", "files", "fileStats"}, "ids": []string{strings.ToLower(hash)}}, &result); e != nil {
		return nil, e
	}
	if len(result.Torrents) == 0 {
		return nil, ports.ErrNotFound
	}
	if len(result.Torrents) != 1 {
		return nil, ports.ErrBadResponse
	}
	return result.Torrents[0].inventory(hash)
}

func (t torrentFileObservation) inventory(hash string) ([]ports.TorrentFile, error) {
	if t.Hash == nil || !strings.EqualFold(*t.Hash, hash) || len(t.Files) == 0 || len(t.Files) > 1000 || len(t.Stats) != len(t.Files) {
		return nil, ports.ErrBadResponse
	}
	names := map[string]bool{}
	out := make([]ports.TorrentFile, 0, len(t.Files))
	for i, f := range t.Files {
		stats := t.Stats[i]
		if f.Name == nil || f.Length == nil || f.Complete == nil || stats.Complete == nil || stats.Wanted == nil || *f.Length < 0 || *f.Complete < 0 || *f.Complete > *f.Length || *stats.Complete != *f.Complete {
			return nil, ports.ErrBadResponse
		}
		name := *f.Name
		if name == "" || len(name) > 1024 || path.IsAbs(name) || path.Clean(name) != name || name == "." || name == ".." || strings.HasPrefix(name, "../") || strings.ContainsAny(name, "\\:\x00\r\n") || names[name] {
			return nil, ports.ErrBadResponse
		}
		names[name] = true
		progress := float64(1)
		if *f.Length > 0 {
			progress = float64(*f.Complete) / float64(*f.Length)
		}
		out = append(out, ports.TorrentFile{Index: int32(i), Name: name, SizeBytes: *f.Length, Progress: progress})
	}
	return out, nil
}
