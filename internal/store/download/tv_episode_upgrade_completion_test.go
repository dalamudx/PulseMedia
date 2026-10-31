package download

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"testing"
)

type tvUpgradeCompletionRows struct {
	ports.DownloadClient
	row *ports.TorrentInfo
}

func (s tvUpgradeCompletionRows) ListTorrents(context.Context, string) ([]*ports.TorrentInfo, error) {
	return []*ports.TorrentInfo{s.row}, nil
}

func TestTVEpisodeUpgradeNativeCompletionStates(t *testing.T) {
	for _, tc := range []struct {
		state    string
		bytes    int64
		progress float32
		complete bool
	}{
		{"uploading", 100, 1, true}, {"stalledUP", 100, 1, true}, {"queuedUP", 100, 1, true}, {"forcedUP", 100, 1, true}, {"pausedUP", 100, 1, true}, {"stoppedUP", 100, 1, true}, {"seeding", 100, 1, true},
		{"checkingUP", 100, 1, false}, {"checkingDL", 100, 1, false}, {"checkingResumeData", 100, 1, false}, {"moving", 100, 1, false}, {"downloading", 100, 1, false}, {"unknown", 100, 1, false}, {"uploading", 99, 1, false}, {"uploading", 101, 1, false}, {"uploading", 100, 0.5, false},
	} {
		t.Run(tc.state, func(t *testing.T) {
			original := &ports.TorrentInfo{State: tc.state, TotalBytes: 100, DownloadedBytes: tc.bytes, Progress: tc.progress}
			c := tvEpisodeUpgradeClient{DownloadClient: tvUpgradeCompletionRows{row: original}}
			rows, err := c.ListTorrents(t.Context(), "owned-tag")
			if err != nil || len(rows) != 1 || rows[0] == original || (rows[0].State == "completed") != tc.complete || original.State != tc.state {
				t.Fatalf("native completion normalization failed: %v %+v", err, rows)
			}
		})
	}
}
