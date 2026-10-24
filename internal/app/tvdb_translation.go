package app

import (
	"context"
	"strings"
	"time"

	tvdbadapter "github.com/dalamudx/PulseMedia/internal/adapter/metadata/tvdb"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/metadatafallback"
)

// Only the HTTP browse provider uses translated display fields. Creation, refresh,
// authorization, sidecars and frozen receipts retain the primary provider.
type tvdbBrowseProvider struct {
	ports.MetadataProvider
	translations ports.TVTranslationProvider
	source       tvdbSource
}

func (p *tvdbBrowseProvider) GetTVDetails(ctx context.Context, id int64, language string) (*ports.TVDetails, error) {
	primary, err := p.MetadataProvider.GetTVDetails(ctx, id, language)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	if primary == nil || primary.TMDBID != id {
		return nil, ports.ErrBadResponse
	}
	if language != "zh-CN" && language != "" {
		return primary, nil
	}
	translated := p.supplement(ctx, primary)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if translated != nil {
		return translated, nil
	}
	return primary, nil
}
func (p *tvdbBrowseProvider) supplement(ctx context.Context, primary *ports.TVDetails) *ports.TVDetails {
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	binding, credentials, err := p.source.read(bounded)
	if err != nil || p.translations == nil || p.source.identity == nil {
		return nil
	}
	local, err := p.translations.GetTVTranslation(bounded, primary.TMDBID, "zh-CN")
	if err != nil || local == nil || local.TMDBID != primary.TMDBID || local.Language != "zh-CN" {
		return nil
	}
	if strings.TrimSpace(local.Title) != "" && strings.TrimSpace(local.Overview) != "" {
		return nil
	}
	current, _, err := p.source.read(bounded)
	if err != nil || current != binding {
		return nil
	}
	identity, err := p.source.identity.ResolveTVArtworkIdentity(bounded, primary.TMDBID)
	if err != nil || identity.TMDBID != primary.TMDBID || identity.TVDBID <= 0 {
		return nil
	}
	source := tvdbadapter.NewClientWithPIN(credentials.APIKey, credentials.PIN, p.source.httpClient)
	translation, err := source.GetSeriesTranslation(bounded, identity.TVDBID, "zho")
	if err != nil {
		return nil
	}
	current, _, err = p.source.read(bounded)
	if err != nil || current != binding || bounded.Err() != nil {
		return nil
	}
	result, err := metadatafallback.SupplementTranslation(primary, local, identity, translation)
	if err != nil {
		return nil
	}
	return result
}
func (p *dynamicTMDBProvider) GetTVTranslation(ctx context.Context, id int64, language string) (*ports.TVTranslation, error) {
	provider, ok := p.getClient(ctx).(ports.TVTranslationProvider)
	if !ok {
		return nil, ports.ErrUnavailable
	}
	return provider.GetTVTranslation(ctx, id, language)
}
