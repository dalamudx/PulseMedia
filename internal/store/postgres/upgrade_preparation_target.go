package postgres

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"strings"
)

// Both localized and original titles, when different, come from the same locked
// media row. No aliases, identity claims or target versions come from a caller.
func preparationMovieTarget(ctx context.Context, q *db.Queries, frozen upgradeFrozenSnapshot) (media.MovieUpgradeTarget, error) {
	v := frozen.Observation
	m, err := q.GetMediaByID(ctx, v.MediaID)
	if err != nil {
		return media.MovieUpgradeTarget{}, err
	}
	units, err := q.ListUpgradeUnits(ctx, v.MediaID)
	if err != nil {
		return media.MovieUpgradeTarget{}, err
	}
	if m.MediaType != "movie" || m.MetadataVersion != frozen.Target.Candidate.MediaVersion || !m.Year.Valid || m.Year.Int32 <= 0 || len(units) != 1 || units[0].ID != v.UnitID || units[0].Retired || units[0].UnitKey != "movie" || units[0].MetadataVersion != frozen.UnitVersion {
		return media.MovieUpgradeTarget{}, preparationConflict()
	}
	identities, err := q.ListSearchMediaIdentities(ctx, v.MediaID)
	if err != nil {
		return media.MovieUpgradeTarget{}, err
	}
	title := m.Title
	var aliases []string
	if m.OriginalTitle.Valid && strings.TrimSpace(m.OriginalTitle.String) != "" {
		title = m.OriginalTitle.String
		if !strings.EqualFold(title, m.Title) && strings.TrimSpace(m.Title) != "" {
			aliases = []string{m.Title}
		}
	}
	target := media.MovieUpgradeTarget{MediaID: m.ID, UnitID: v.UnitID, MediaVersion: m.MetadataVersion, UnitVersion: units[0].MetadataVersion, MediaType: media.TypeMovie, UnitKey: "movie", OriginalTitle: title, Year: int(m.Year.Int32), Aliases: aliases, ParserVersion: media.EpisodeParserVersion}
	for _, identity := range identities {
		target.Identities = append(target.Identities, media.Identity{Namespace: identity.Namespace, ExternalID: identity.ExternalID})
	}
	return target, nil
}
