package notify

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"

	"github.com/google/uuid"
)

func (d *Dispatcher) ListChannels(ctx context.Context, actor *ports.Actor) ([]ports.NotificationChannelObservation, error) {
	if actor == nil {
		return nil, ports.ErrUnauthorized
	}
	if actor.Type != ports.ActorTypeUser || actor.Scopes != nil || !actor.IsAdmin() {
		return nil, ports.ErrForbidden
	}
	if d.channels == nil {
		return nil, ports.ErrUnavailable
	}
	return d.channels.ListNotificationChannels(ctx, actor)
}

// The legacy unfenced mutation is intentionally unavailable. Use ChannelStateService.
func (d *Dispatcher) DisableChannel(ctx context.Context, actor *ports.Actor, id uuid.UUID) error {
	if actor == nil || !actor.IsAdmin() {
		return ports.ErrForbidden
	}
	return ports.ErrChannelStateConflict
}
