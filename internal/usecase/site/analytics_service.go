package site

import (
	"context"

	"github.com/google/uuid"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

type TrafficStats struct {
	UploadBytes   int64   `json:"upload_bytes"`
	DownloadBytes int64   `json:"download_bytes"`
	Ratio         float64 `json:"ratio"`
	SeedingCount  int     `json:"seeding_count"`
	BonusPoints   float64 `json:"bonus_points"`
}

type CheckinResult = ports.SiteCheckinResult

type Service struct{ client ports.SiteAttendanceClient }

func NewService(client ports.SiteAttendanceClient) *Service { return &Service{client: client} }

func (s *Service) CheckinSite(ctx context.Context, siteURL, cookie string) (*CheckinResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.client.CheckinSite(ctx, siteURL, cookie)
}

// GetTrafficStats requires a site-specific authenticated parser, not guessed values.
func (s *Service) GetTrafficStats(ctx context.Context, siteID uuid.UUID) (*TrafficStats, error) {
	return nil, ports.ErrUnavailable
}
