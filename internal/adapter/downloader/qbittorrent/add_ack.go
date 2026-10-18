package qbittorrent

import (
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"strings"
)

// Older Web APIs acknowledge with Ok.; Web API 2.15 reports per-torrent counts
// and IDs. Pending, partial, or unrelated replies cannot confirm a single Add.
func confirmAddAcknowledgement(raw []byte, p ports.AddTorrentParam) error {
	if strings.TrimSpace(string(raw)) == "Ok." {
		return nil
	}
	if !uniqueCompleteTVObservationObject(raw) {
		return ports.ErrBadResponse
	}
	var ack struct {
		IDs       []string `json:"added_torrent_ids"`
		Failed    *int64   `json:"failure_count"`
		Pending   *int64   `json:"pending_count"`
		Succeeded *int64   `json:"success_count"`
	}
	if err := json.Unmarshal(raw, &ack); err != nil || ack.Failed == nil || ack.Pending == nil || ack.Succeeded == nil || *ack.Failed != 0 || *ack.Pending != 0 || *ack.Succeeded != 1 || len(ack.IDs) != 1 {
		return ports.ErrBadResponse
	}
	expected := p.InfoHash
	if expected == "" && p.DownloadURL != "" {
		var err error
		expected, err = ports.DownloadMagnetHash(p.DownloadURL)
		if err != nil {
			return ports.ErrBadResponse
		}
	}
	if !validTorrentHash(expected) || !strings.EqualFold(ack.IDs[0], expected) {
		return ports.ErrBadResponse
	}
	return nil
}
