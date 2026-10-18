package download_test

import (
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/download"
)

func TestSeedingPolicyEvaluation(t *testing.T) {
	rule := download.SeedingRule{
		MaxSeedingHours: 48,
		MinRatio:        2.0,
		DeleteFiles:     false,
	}

	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	addedAt := now.Add(-50 * time.Hour) // 50 hours ago

	// 1. Torrent reached 48 hours -> Should remove
	infoTime := &ports.TorrentInfo{
		InfoHash:        "hash1",
		TotalBytes:      1000,
		DownloadedBytes: 1000,
		UploadedBytes:   500, // ratio 0.5 < 2.0
	}
	resTime := download.EvaluateSeedingPolicy(infoTime, addedAt, now, rule)
	if !resTime.ShouldRemove || resTime.Reason == "" {
		t.Fatalf("expected remove due to time, got: %+v", resTime)
	}

	// 2. Torrent reached ratio 2.5 (even if added 2 hours ago) -> Should remove
	recentAdded := now.Add(-2 * time.Hour)
	infoRatio := &ports.TorrentInfo{
		InfoHash:        "hash2",
		TotalBytes:      1000,
		DownloadedBytes: 1000,
		UploadedBytes:   2500, // ratio 2.5 >= 2.0
	}
	resRatio := download.EvaluateSeedingPolicy(infoRatio, recentAdded, now, rule)
	if !resRatio.ShouldRemove || resRatio.Reason == "" {
		t.Fatalf("expected remove due to ratio, got: %+v", resRatio)
	}

	// 3. Torrent has neither reached time nor ratio -> Keep seeding
	infoActive := &ports.TorrentInfo{
		InfoHash:        "hash3",
		TotalBytes:      1000,
		DownloadedBytes: 1000,
		UploadedBytes:   1000, // ratio 1.0 < 2.0
	}
	resActive := download.EvaluateSeedingPolicy(infoActive, recentAdded, now, rule)
	if resActive.ShouldRemove {
		t.Fatalf("expected keep seeding, got: %+v", resActive)
	}
}
