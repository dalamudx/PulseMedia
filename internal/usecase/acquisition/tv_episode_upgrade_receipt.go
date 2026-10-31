package acquisition

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

func (s *TVEpisodeUpgradeService) ReadCommand(ctx context.Context, a *ports.Actor, sub uuid.UUID, key string) (*ports.TVEpisodeUpgradeResult, error) {
	if e := upgradeReservationActor(a); e != nil {
		return nil, e
	}
	if sub == uuid.Nil || key == "" || len(key) > 128 {
		return nil, ports.ErrTVEpisodeUpgradeConflict
	}
	r, ok := s.repo.(ports.TVEpisodeUpgradeReceiptRepository)
	if !ok {
		return nil, ports.ErrUnavailable
	}
	return r.ReadTVEpisodeUpgradeCommand(ctx, a, sub, key)
}