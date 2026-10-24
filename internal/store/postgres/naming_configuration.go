package postgres

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/domain/naming"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/jackc/pgx/v5"
)

// Match the setting writer's key lock, including when the first row is absent.
// Existing operations must recover their original record before calling this.
func lockOrganizationNaming(ctx context.Context, q *db.Queries) (naming.TemplateRules, int64, error) {
	if err := q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "setting", AggregateID: naming.TemplateSettingKey}); err != nil {
		return naming.TemplateRules{}, 0, err
	}
	row, err := q.GetSetting(ctx, naming.TemplateSettingKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return naming.TemplateRules{}, 0, nil
	}
	if err != nil {
		return naming.TemplateRules{}, 0, err
	}
	rules, err := naming.DecodeTemplateRules(row.Value)
	if err != nil || row.Version < 1 {
		return naming.TemplateRules{}, 0, ports.ErrTransferReviewRequired
	}
	return rules, row.Version, nil
}

func upgradeNamingInput(target media.MovieUpgradeTarget, profile quality.Profile, extension, template string) (naming.MediaTemplateInput, error) {
	input := naming.MediaTemplateInput{Title: target.OriginalTitle, Year: target.Year, Quality: profile, Extension: extension}
	for _, identity := range target.Identities {
		if identity.Namespace == "tmdb.movie" && strings.Contains(template, "{tmdb_id}") {
			id, err := strconv.ParseInt(identity.ExternalID, 10, 64)
			if err != nil || id <= 0 || strconv.FormatInt(id, 10) != identity.ExternalID || input.TMDBID != 0 {
				return input, preparationConflict()
			}
			input.TMDBID = id
		}
		if identity.Namespace == "imdb" && strings.Contains(template, "{imdb_id}") {
			if input.IMDBID != "" {
				return input, preparationConflict()
			}
			input.IMDBID = identity.ExternalID
		}
	}
	return input, nil
}
