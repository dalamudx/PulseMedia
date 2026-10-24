package transfer

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/dalamudx/PulseMedia/internal/domain/naming"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/jackc/pgx/v5"
)

// The same key lock is used by SettingsStore, including first creation with no row.
// Call only while constructing a new plan, after recovering any original plan.
func lockTransferNaming(ctx context.Context, q *db.Queries) (naming.TemplateRules, int64, error) {
	if err := q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "setting", AggregateID: naming.TemplateSettingKey}); err != nil {
		return naming.TemplateRules{}, 0, err
	}
	record, err := q.GetSetting(ctx, naming.TemplateSettingKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return naming.TemplateRules{}, 0, nil
	}
	if err != nil {
		return naming.TemplateRules{}, 0, err
	}
	rules, err := naming.DecodeTemplateRules(record.Value)
	if err != nil || record.Version < 1 {
		return naming.TemplateRules{}, 0, ErrReviewRequired
	}
	return rules, record.Version, nil
}

func transferNamingInput(ctx context.Context, q *db.Queries, parent db.Medium, unit db.MediaUnit, profile quality.Profile, extension, template string) (naming.MediaTemplateInput, error) {
	input := naming.MediaTemplateInput{Title: parent.Title, Extension: extension, Quality: profile}
	if parent.Year.Valid {
		input.Year = int(parent.Year.Int32)
	}
	if parent.MediaType == "tv" {
		if !unit.SeasonNumber.Valid || !unit.EpisodeNumber.Valid {
			return input, ErrReviewRequired
		}
		input.Season = int(unit.SeasonNumber.Int32)
		input.Episode = int(unit.EpisodeNumber.Int32)
	}
	needTMDB := strings.Contains(template, "{tmdb_id}")
	needIMDB := strings.Contains(template, "{imdb_id}")
	if !needTMDB && !needIMDB {
		return input, nil
	}
	identities, err := q.ListSearchMediaIdentities(ctx, parent.ID)
	if err != nil {
		return input, err
	}
	for _, identity := range identities {
		if needTMDB && identity.Namespace == "tmdb."+parent.MediaType {
			id, err := strconv.ParseInt(identity.ExternalID, 10, 64)
			if err != nil || id <= 0 || strconv.FormatInt(id, 10) != identity.ExternalID || input.TMDBID != 0 {
				return input, ErrReviewRequired
			}
			input.TMDBID = id
		}
		if needIMDB && identity.Namespace == "imdb" {
			if input.IMDBID != "" {
				return input, ErrReviewRequired
			}
			input.IMDBID = identity.ExternalID
		}
	}
	return input, nil
}
