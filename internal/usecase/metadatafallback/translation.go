package metadatafallback

import (
	"github.com/dalamudx/PulseMedia/internal/ports"
	"strings"
)

// SupplementTranslation only changes display fields with explicit missing-locale evidence.
// It neither changes canonical metadata nor carries episode or acquisition authority.
func SupplementTranslation(primary *ports.TVDetails, local *ports.TVTranslation, identity ports.TVArtworkIdentity, source *ports.TVDBTranslation) (*ports.TVDetails, error) {
	if primary == nil || primary.TMDBID <= 0 || local == nil || local.TMDBID != primary.TMDBID || local.Language != "zh-CN" || identity.TMDBID != primary.TMDBID || identity.TVDBID <= 0 || source == nil || source.TVDBID != identity.TVDBID || source.Language != "zho" {
		return nil, ports.ErrBadResponse
	}
	result := *primary
	result.Genres = append([]string(nil), primary.Genres...)
	result.Seasons = append([]ports.TVSeasonDetails(nil), primary.Seasons...)
	fields := []string{}
	if strings.TrimSpace(local.Title) == "" && strings.TrimSpace(source.Title) != "" {
		result.Title = source.Title
		fields = append(fields, "title")
	}
	if strings.TrimSpace(local.Overview) == "" && strings.TrimSpace(source.Overview) != "" {
		result.Overview = source.Overview
		fields = append(fields, "overview")
	}
	if len(fields) > 0 {
		result.TranslationSource = &ports.TVTranslationSource{Provider: "tvdb", SourceID: source.TVDBID, Language: source.Language, Fields: fields}
	}
	return &result, nil
}
