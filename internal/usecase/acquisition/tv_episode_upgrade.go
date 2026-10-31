package acquisition

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

// TVEpisodeUpgradeService is separate from movie and automatic acquisition authority.
type TVEpisodeUpgradeService struct {
	repo ports.TVEpisodeUpgradeRepository
}

func NewTVEpisodeUpgradeService(repo ports.TVEpisodeUpgradeRepository) *TVEpisodeUpgradeService {
	return &TVEpisodeUpgradeService{repo: repo}
}
func (s *TVEpisodeUpgradeService) Preview(ctx context.Context, a *ports.Actor, r ports.TVEpisodeUpgradePreviewRequest) (*ports.TVEpisodeUpgradeResult, error) {
	if e := upgradeReservationActor(a); e != nil {
		return nil, e
	}
	if s.repo == nil {
		return nil, ports.ErrUnavailable
	}
	if r.SubscriptionID == uuid.Nil || r.UnitID == uuid.Nil || r.CurrentFileID == uuid.Nil || r.CandidateID == uuid.Nil {
		return nil, ports.ErrTVEpisodeUpgradeConflict
	}
	return s.repo.PreviewTVEpisodeUpgrade(ctx, a, r)
}
func (s *TVEpisodeUpgradeService) Command(ctx context.Context, a *ports.Actor, c ports.TVEpisodeUpgradeCommand, key string) (*ports.TVEpisodeUpgradeResult, error) {
	if e := upgradeReservationActor(a); e != nil {
		return nil, e
	}
	if s.repo == nil {
		return nil, ports.ErrUnavailable
	}
	if c.SubscriptionID == uuid.Nil || !c.Confirmed || key == "" || len(key) > 128 {
		return nil, ports.ErrTVEpisodeUpgradeConflict
	}
	switch c.Action {
	case "reserve", "prepare", "send", "observe", "organization-preview", "organization-confirm", "organize", "retirement-preview", "retirement-confirm", "retire":
	default:
		return nil, ports.ErrTVEpisodeUpgradeConflict
	}
	if c.Action != "reserve" && c.OperationID == uuid.Nil {
		return nil, ports.ErrTVEpisodeUpgradeConflict
	}
	return s.repo.CommandTVEpisodeUpgrade(ctx, a, c, key)
}