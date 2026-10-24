package app

import (
	"context"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

var _ ports.PersonCreditsProvider = (*dynamicTMDBProvider)(nil)

func (p *dynamicTMDBProvider) GetPersonCredits(ctx context.Context, id int64, role string) (ports.PersonCredits, error) {
	if err := ctx.Err(); err != nil {
		return ports.PersonCredits{}, err
	}
	if id <= 0 || (role != "actor" && role != "director") {
		return ports.PersonCredits{}, ports.ErrRejected
	}
	provider, ok := p.getClient(ctx).(ports.PersonCreditsProvider)
	if !ok {
		return ports.PersonCredits{}, ports.ErrUnavailable
	}
	return provider.GetPersonCredits(ctx, id, role)
}
