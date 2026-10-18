package transmission

import (
	"github.com/dalamudx/PulseMedia/internal/ports"
	"strings"
)

type torrentObservation struct {
	HashString  string   `json:"hashString"`
	Name        string   `json:"name"`
	DownloadDir string   `json:"downloadDir"`
	PercentDone float32  `json:"percentDone"`
	TotalSize   int64    `json:"totalSize"`
	HaveValid   *int64   `json:"haveValid"`
	Status      int      `json:"status"`
	Labels      []string `json:"labels"`
}

func (t torrentObservation) hasLabel(wanted string) bool {
	for _, label := range t.Labels {
		if label == wanted {
			return true
		}
	}
	return false
}

func (t torrentObservation) info() (*ports.TorrentInfo, error) {
	var downloaded int64
	if t.HaveValid != nil {
		downloaded = *t.HaveValid
		if downloaded < 0 || downloaded > t.TotalSize {
			return nil, ports.ErrBadResponse
		}
	}
	state := "downloading"
	switch t.Status {
	case 6:
		state = "seeding"
	case 5:
		state = "queuedUP"
	case 0:
		state = "pausedUP"
	}
	return &ports.TorrentInfo{InfoHash: strings.ToLower(t.HashString), Name: t.Name, SavePath: t.DownloadDir, Progress: t.PercentDone, TotalBytes: t.TotalSize, DownloadedBytes: downloaded, State: state, Tags: strings.Join(t.Labels, ",")}, nil
}
