package acquisition

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"strings"
)

type PublicUpgradeSendService struct {
	commands ports.MovieUpgradeSendCommandRepository
}

func NewPublicUpgradeSendService(commands ports.MovieUpgradeSendCommandRepository) (*PublicUpgradeSendService, error) {
	if commands == nil {
		return nil, ports.ErrUnavailable
	}
	return &PublicUpgradeSendService{commands: commands}, nil
}
func (s *PublicUpgradeSendService) Confirm(ctx context.Context, a *ports.Actor, c ports.QueueMovieUpgradeSend, key string) (*ports.MovieUpgradeSendJobReceipt, error) {
	if err := upgradeReservationActor(a); err != nil {
		return nil, err
	}
	if c.SubscriptionID == uuid.Nil || c.OperationID == uuid.Nil || c.TaskID == uuid.Nil || !c.Confirmed || key == "" || strings.TrimSpace(key) != key || len(key) > 130 {
		return nil, ports.ErrUpgradeReservationConflict
	}
	return s.commands.QueueMovieUpgradeSend(ctx, a, c, key)
}
