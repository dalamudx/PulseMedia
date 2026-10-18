package download

import (
	"fmt"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

type SeedingRule struct {
	MaxSeedingHours int     `json:"max_seeding_hours"`
	MinRatio        float64 `json:"min_ratio"`
	DeleteFiles     bool    `json:"delete_files"`
}

type SeedingEvaluation struct {
	ShouldRemove bool   `json:"should_remove"`
	Reason       string `json:"reason"`
}

// EvaluateSeedingPolicy evaluates whether a torrent has met seeding criteria and can be safely removed.
func EvaluateSeedingPolicy(info *ports.TorrentInfo, addedAt time.Time, now time.Time, rule SeedingRule) SeedingEvaluation {
	if info == nil {
		return SeedingEvaluation{ShouldRemove: false, Reason: "nil_info"}
	}

	// 1. Ratio check: uploaded / downloaded
	if rule.MinRatio > 0 && info.DownloadedBytes > 0 {
		currentRatio := float64(info.UploadedBytes) / float64(info.DownloadedBytes)
		if currentRatio >= rule.MinRatio {
			return SeedingEvaluation{
				ShouldRemove: true,
				Reason:       fmt.Sprintf("分享率已达标: %.2f >= %.2f", currentRatio, rule.MinRatio),
			}
		}
	}

	// 2. Seeding time check
	if rule.MaxSeedingHours > 0 && !addedAt.IsZero() {
		seedingDuration := now.Sub(addedAt)
		if seedingDuration >= time.Duration(rule.MaxSeedingHours)*time.Hour {
			return SeedingEvaluation{
				ShouldRemove: true,
				Reason:       fmt.Sprintf("做种时间已达标: %.1f小时 >= %d小时", seedingDuration.Hours(), rule.MaxSeedingHours),
			}
		}
	}

	return SeedingEvaluation{
		ShouldRemove: false,
		Reason:       "做种监控中",
	}
}
