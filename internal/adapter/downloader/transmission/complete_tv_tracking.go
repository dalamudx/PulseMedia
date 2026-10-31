package transmission

import (
	"context"
	"math"
	"strings"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

// CompleteTVManifestTrackingObserver exposes native facts only. Transmission RPC
// cannot export torrent bytes; the repository must bind this inventory to the
// original inspected preparation, never a synthesized or caller-supplied torrent.
func (c *Client) CompleteTVManifestTrackingObserver() ports.CompleteTVManifestTrackingClient {
	return dedicatedManifestReader{automaticTVReader{client: c}}
}

type dedicatedManifestReader struct{ native automaticTVReader }

var _ ports.CompleteTVManifestTrackingClient = dedicatedManifestReader{}

func (r dedicatedManifestReader) ListTorrents(ctx context.Context, tag string) ([]*ports.TorrentInfo, error) {
	tasks, err := r.native.ListAutomaticTVTasks(ctx, tag)
	if err != nil {
		return nil, err
	}
	out := make([]*ports.TorrentInfo, 0, len(tasks))
	for _, t := range tasks {
		state := "error"
		switch t.State {
		case "stopped":
			state = "stoppedDL"
		case "checking":
			state = "checkingDL"
		case "queued":
			state = "queuedDL"
		case "downloading":
			state = "downloading"
		case "seeding":
			state = "uploading"
		}
		if t.Complete {
			switch t.State {
			case "stopped":
				state = "stoppedUP"
			case "queued":
				state = "stalledUP"
			}
		}
		progress := dedicatedProgress(t.VerifiedBytes, t.TotalBytes)
		// Checking/downloading counters cannot certify completion.
		if !t.Complete && progress == 1 {
			progress = math.Nextafter32(1, 0)
		}
		out = append(out, &ports.TorrentInfo{InfoHash: t.InfoHash, SavePath: t.SavePath, State: state, Tags: strings.Join(t.Tags, ","), TotalBytes: t.TotalBytes, DownloadedBytes: t.VerifiedBytes, Progress: progress, DownloadRateBytesPerSecond: t.DownloadRateBytesPerSecond})
	}
	return out, nil
}

func dedicatedProgress(done, size int64) float32 {
	if size == 0 || done == size {
		return 1
	}
	p := float32(float64(done) / float64(size))
	if p >= 1 {
		return math.Nextafter32(1, 0)
	}
	return p
}

func (r dedicatedManifestReader) GetTorrentFiles(ctx context.Context, hash string) ([]ports.TorrentFile, error) {
	files, err := r.native.GetAutomaticTVFiles(ctx, hash)
	if err != nil {
		return nil, err
	}
	if len(files) > 1000 {
		return nil, ports.ErrUnavailable
	}
	out := make([]ports.TorrentFile, 0, len(files))
	for _, f := range files {
		// Wanted=false is deliberately retained; it cannot hide a native file.
		if len(f.Path) > 1024 {
			return nil, ports.ErrUnavailable
		}
		out = append(out, ports.TorrentFile{Index: int32(f.Index), Name: f.Path, SizeBytes: f.Size, Progress: float64(dedicatedProgress(f.CompletedBytes, f.Size))})
	}
	return out, nil
}
