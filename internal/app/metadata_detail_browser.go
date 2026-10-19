package app

import (
	"context"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

var _ ports.MetadataDetailBrowser = (*dynamicTMDBProvider)(nil)

func (p *dynamicTMDBProvider) GetMediaCredits(ctx context.Context, kind string, id int64, language string) (*ports.MetadataCredits, error) {
	provider, ok := p.getClient(ctx).(ports.MetadataDetailBrowser)
	if !ok {
		return nil, ports.ErrUnavailable
	}
	return provider.GetMediaCredits(ctx, kind, id, language)
}

func (p *dynamicTMDBProvider) GetTVSeasonDetails(ctx context.Context, id int64, season int, language string) (*ports.MetadataSeasonDetails, error) {
	provider, ok := p.getClient(ctx).(ports.MetadataDetailBrowser)
	if !ok {
		return nil, ports.ErrUnavailable
	}
	return provider.GetTVSeasonDetails(ctx, id, season, language)
}
