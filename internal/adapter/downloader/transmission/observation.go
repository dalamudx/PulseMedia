package transmission

import (
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"path"
	"strings"
)

type torrentObservation struct {
	torrentFileObservation
	HashString   string          `json:"hashString"`
	Name         string          `json:"name"`
	DownloadDir  string          `json:"downloadDir"`
	PercentDone  float32         `json:"percentDone"`
	TotalSize    int64           `json:"totalSize"`
	HaveValid    *int64          `json:"haveValid"`
	Status       int             `json:"status"`
	Labels       []string        `json:"labels"`
	DownloadRate json.RawMessage `json:"rateDownload"`
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
	contentPath := ""
	// The display name is not file evidence; derive only from a complete native singleton inventory.
	if len(t.Files) == 1 && len(t.Stats) == 1 && t.TotalSize > 0 && downloaded == t.TotalSize && t.PercentDone == 1 && (t.Status == 0 || t.Status == 5 || t.Status == 6) {
		inventory := t.torrentFileObservation
		inventory.Hash = &t.HashString
		files, err := inventory.inventory(t.HashString)
		if err != nil {
			return nil, err
		}
		if !automaticTVPath(t.DownloadDir, true) {
			return nil, ports.ErrBadResponse
		}
		if files[0].SizeBytes == t.TotalSize && files[0].Progress == 1 {
			contentPath = path.Join(t.DownloadDir, files[0].Name)
		}
	}
	return &ports.TorrentInfo{InfoHash: strings.ToLower(t.HashString), Name: t.Name, SavePath: t.DownloadDir, ContentPath: contentPath, Progress: t.PercentDone, TotalBytes: t.TotalSize, DownloadedBytes: downloaded, State: state, Tags: strings.Join(t.Labels, ","), DownloadRateBytesPerSecond: nativeDownloadRate(t.DownloadRate)}, nil
}
