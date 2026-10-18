package postgres

import (
	"context"
	"errors"
	"math"
	"strconv"
	"strings"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	domain "github.com/dalamudx/PulseMedia/internal/domain/subscription"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// Validate provider material again at publication. Counts describe observations;
// only the actual episode list supplies canonical units or retirement authority.
func validateCreationMetadata(p *ports.PreparedSubscriptionCreationMetadata, c ports.SubscriptionCreationContext, in ports.SubscriptionCreationInput) error {
	invalid := ports.ErrInvalidSubscriptionBindings
	if p == nil {
		if c.MediaID == uuid.Nil {
			return invalid
		}
		return nil
	}
	if c.TMDBID <= 0 || p.TMDBID != c.TMDBID || p.Media.Namespace != "tmdb."+c.MediaType || p.Media.ExternalID != strconv.FormatInt(c.TMDBID, 10) || p.Media.MediaType != c.MediaType || strings.TrimSpace(p.Media.Title) == "" || p.Media.Title != strings.TrimSpace(p.Media.Title) || len(p.Media.Title) > 2000 || (p.Media.Year != nil && (*p.Media.Year < 1 || *p.Media.Year > 9999)) {
		return invalid
	}
	if c.MediaType == "movie" {
		if len(p.Seasons) != 0 || len(p.Media.Seasons) != 0 || len(p.Units) != 1 || p.Units[0].UnitKey != media.FormatMovieUnitKey() || p.Units[0].SeasonNumber != nil || p.Units[0].EpisodeNumber != nil {
			return invalid
		}
		return nil
	}
	if len(p.Media.Seasons) > 64 || len(p.Seasons) > 64 || len(p.Units) > 20000 {
		return invalid
	}
	declared := map[int]int{}
	total := 0
	for _, s := range p.Media.Seasons {
		if _, ok := declared[s.Season]; ok || s.Season < 0 || s.Season > 999 || s.Count < 0 || s.Count > 5000 || (s.Season == 0 && !in.IncludeSpecials) {
			return invalid
		}
		if len(in.Seasons) > 0 {
			found := s.Season == 0 && in.IncludeSpecials
			for _, n := range in.Seasons {
				found = found || s.Season == n
			}
			if !found {
				return invalid
			}
		}
		declared[s.Season] = s.Count
		total += s.Count
		if total > 20000 {
			return invalid
		}
	}
	for _, n := range in.Seasons {
		if _, ok := declared[n]; !ok {
			return invalid
		}
	}
	expected := map[string][2]int32{}
	ids := map[int64]bool{}
	seen := map[int]bool{}
	for _, season := range p.Seasons {
		count, ok := declared[season.SeasonNumber]
		if !ok || seen[season.SeasonNumber] || len(season.Episodes) > count || (season.Complete && len(season.Episodes) > 0 && len(season.Episodes) != count) {
			return invalid
		}
		seen[season.SeasonNumber] = true
		for _, episode := range season.Episodes {
			key := media.FormatEpisodeUnitKey(season.SeasonNumber, episode.EpisodeNumber)
			if episode.EpisodeNumber < 1 || episode.EpisodeNumber > 9999 || episode.TMDBEpisodeID <= 0 || ids[episode.TMDBEpisodeID] {
				return invalid
			}
			if _, duplicate := expected[key]; duplicate {
				return invalid
			}
			if episode.AbsoluteNumber != nil && (*episode.AbsoluteNumber < 1 || *episode.AbsoluteNumber > math.MaxInt32) {
				return invalid
			}
			if episode.AirDate != nil && (episode.AirDate.Year() < 1 || episode.AirDate.Year() > 9999) {
				return invalid
			}
			ids[episode.TMDBEpisodeID] = true
			expected[key] = [2]int32{int32(season.SeasonNumber), int32(episode.EpisodeNumber)}
		}
	}
	for number, count := range declared {
		if count > 0 && !seen[number] {
			return invalid
		}
	}
	if len(expected) != len(p.Units) {
		return invalid
	}
	for _, u := range p.Units {
		coords, ok := expected[u.UnitKey]
		if !ok || u.SeasonNumber == nil || u.EpisodeNumber == nil || *u.SeasonNumber != coords[0] || *u.EpisodeNumber != coords[1] {
			return invalid
		}
		delete(expected, u.UnitKey)
	}
	return nil
}

func (t *subscriptionIntentTx) createCreationMedia(ctx context.Context, p *ports.PreparedSubscriptionCreationMetadata) (uuid.UUID, error) {
	id := t.ids.NewUUID()
	year := pgtype.Int4{}
	if p.Media.Year != nil {
		year = pgtype.Int4{Int32: *p.Media.Year, Valid: true}
	}
	if _, err := t.q.CreateMedia(ctx, db.CreateMediaParams{ID: id, Title: p.Media.Title, Year: year, MediaType: p.Media.MediaType}); err != nil {
		return uuid.Nil, err
	}
	if _, err := t.q.CreateMediaIdentity(ctx, db.CreateMediaIdentityParams{ID: t.ids.NewUUID(), MediaID: id, Namespace: p.Media.Namespace, ExternalID: p.Media.ExternalID}); err != nil {
		return uuid.Nil, err
	}
	if p.Media.MediaType == "movie" {
		if _, err := t.q.CreateMediaUnit(ctx, db.CreateMediaUnitParams{ID: t.ids.NewUUID(), MediaID: id, UnitKey: media.FormatMovieUnitKey()}); err != nil {
			return uuid.Nil, err
		}
	}
	return id, nil
}

// Call only after current policies, subscriptions, roots, units and media are
// locked and the related subscription set has been checked again.
func (t *subscriptionIntentTx) mergeCreationTV(ctx context.Context, id uuid.UUID, tmdb int64, seasons []ports.TVSeasonMetadata, subs []db.Subscription) error {
	if len(seasons) == 0 {
		return nil
	}
	complete := make([]ports.TVSeasonMetadata, 0, len(seasons))
	for _, season := range seasons {
		numbers := make([]int32, 0, len(season.Episodes))
		for _, episode := range season.Episodes {
			date := pgtype.Date{}
			if episode.AirDate != nil {
				date = pgtype.Date{Time: *episode.AirDate, Valid: true}
			}
			absolute := pgtype.Int4{}
			if episode.AbsoluteNumber != nil {
				absolute = pgtype.Int4{Int32: int32(*episode.AbsoluteNumber), Valid: true}
			}
			u, err := t.q.ApplyTVEpisodeMetadata(ctx, db.ApplyTVEpisodeMetadataParams{ID: t.ids.NewUUID(), MediaID: id, UnitKey: media.FormatEpisodeUnitKey(season.SeasonNumber, episode.EpisodeNumber), SeasonNumber: pgtype.Int4{Int32: int32(season.SeasonNumber), Valid: true}, EpisodeNumber: pgtype.Int4{Int32: int32(episode.EpisodeNumber), Valid: true}, AbsoluteNumber: absolute, AirDate: date})
			if err != nil {
				return err
			}
			if _, err = t.q.BindTMDBEpisodeIdentity(ctx, db.BindTMDBEpisodeIdentityParams{UnitID: u.ID, ExternalID: episode.TMDBEpisodeID, TmdbSeriesID: tmdb}); err != nil {
				return err
			}
			identity, err := t.q.GetTMDBEpisodeIdentityForUnit(ctx, u.ID)
			if errors.Is(err, pgx.ErrNoRows) {
				return ports.ErrSubscriptionBindingsConflict
			}
			if err != nil {
				return err
			}
			if identity.ExternalID != episode.TMDBEpisodeID || identity.TmdbSeriesID != tmdb {
				return ports.ErrSubscriptionBindingsConflict
			}
			numbers = append(numbers, int32(episode.EpisodeNumber))
		}
		if season.Complete && len(numbers) > 0 {
			if err := t.q.RetireMissingTVSeasonUnits(ctx, db.RetireMissingTVSeasonUnitsParams{MediaID: id, SeasonNumber: pgtype.Int4{Int32: int32(season.SeasonNumber), Valid: true}, Column3: numbers}); err != nil {
				return err
			}
			complete = append(complete, season)
		}
	}
	m, err := t.q.AdvanceTVMediaMetadataVersion(ctx, id)
	if err != nil {
		return err
	}
	if err = persistCompleteSeasonMetadata(ctx, t.q, ports.TVRefreshContext{MediaID: id, TMDBID: tmdb}, m.MetadataVersion, complete); err != nil {
		return err
	}
	units, err := t.q.LockScopeMediaUnits(ctx, id)
	if err != nil {
		return err
	}
	for _, sub := range subs {
		record, err := subscriptionIntentRecord(sub)
		if err != nil {
			return err
		}
		scope := domain.Scope{Type: record.ScopeType, Seasons: record.Seasons, Selected: record.SelectedUnitIDs, IncludeSpecials: record.IncludeSpecials}
		for _, u := range units {
			included := domain.ScopeContains(scope, domain.ScopeUnit{ID: u.ID, Season: int(u.SeasonNumber.Int32), TV: true, Retired: u.Retired})
			if _, err = t.q.ApplyScopeWanted(ctx, db.ApplyScopeWantedParams{ID: t.ids.NewUUID(), SubscriptionID: sub.ID, UnitID: u.ID, IntentVersion: sub.IntentVersion, IsSkipped: !included}); err != nil {
				return err
			}
		}
	}
	return nil
}
