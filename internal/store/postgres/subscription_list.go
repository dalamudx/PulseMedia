package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type SubscriptionListStore struct{ pool *pgxpool.Pool }

var _ ports.SubscriptionListReader = (*SubscriptionListStore)(nil)

func NewSubscriptionListStore(pool *pgxpool.Pool) *SubscriptionListStore {
	return &SubscriptionListStore{pool: pool}
}

func (s *SubscriptionListStore) ReadSubscriptionList(ctx context.Context, actor *ports.Actor, in ports.SubscriptionListQuery) (*ports.SubscriptionListPage, error) {
	if actor == nil {
		return nil, ports.ErrUnauthorized
	}
	id, err := uuid.Parse(actor.ID)
	if err != nil || id == uuid.Nil || actor.Type != ports.ActorTypeUser || (actor.Role != ports.RoleAdmin && actor.Role != ports.RoleMember) {
		return nil, ports.ErrForbidden
	}
	if actor.Scopes != nil {
		read := false
		for _, scope := range actor.Scopes {
			read = read || scope == "read"
		}
		if !read {
			return nil, ports.ErrForbidden
		}
	}
	if s.pool == nil {
		return nil, ports.ErrUnavailable
	}
	// A read-only transaction cannot take a row lock. Lock and validate the account
	// first, then switch the same transaction to read-only before any business read.
	// REPEATABLE READ rejects concurrent account changes rather than using old authority.
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return nil, ports.ErrUnavailable
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)
	account, err := q.LockSubscriptionListAccount(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ports.ErrForbidden
	}
	if err != nil {
		return nil, ports.ErrUnavailable
	}
	if !account.IsActive || account.Role != actor.Role {
		return nil, ports.ErrForbidden
	}
	if err = q.SetSubscriptionListReadOnly(ctx); err != nil {
		return nil, ports.ErrUnavailable
	}
	if in.Limit < 1 || in.Limit > 100 || in.Offset < 0 || in.Offset > 1000000 {
		return nil, ports.ErrInvalidSubscriptionQuery
	}
	out := &ports.SubscriptionListPage{}
	params := db.ReadSubscriptionListEntriesParams{IsAdmin: account.Role == ports.RoleAdmin, UserID: id, OwnOnly: in.OwnOnly, Search: in.Search, TmdbID: in.TMDBID, MediaType: in.MediaType, PageLimit: int32(in.Limit), PageOffset: int32(in.Offset), Calendar: in.Calendar, SubscriptionIds: []uuid.UUID{}}
	var wantedIDs []uuid.UUID
	if in.Calendar {
		page, err := q.ReadSubscriptionCalendarPage(ctx, db.ReadSubscriptionCalendarPageParams{IsAdmin: params.IsAdmin, UserID: id, UnknownDates: in.UnknownDates, StartDate: pgtype.Date{Time: in.Start, Valid: true}, EndDate: pgtype.Date{Time: in.End, Valid: true}, PageLimit: int32(in.Limit), PageOffset: int32(in.Offset)})
		if err != nil {
			return nil, ports.ErrUnavailable
		}
		out.Total = page.Total
		wantedIDs = page.WantedIds
		params.SubscriptionIds = page.SubscriptionIds
		params.PageOffset = 0
	} else {
		counts, err := q.CountSubscriptionList(ctx, db.CountSubscriptionListParams{IsAdmin: params.IsAdmin, UserID: id, OwnOnly: in.OwnOnly, Search: in.Search, TmdbID: in.TMDBID})
		if err != nil {
			return nil, ports.ErrUnavailable
		}
		out.Total, out.Movies, out.TV = counts.Total, counts.Movies, counts.Tv
		if in.MediaType == "movie" {
			out.Total = out.Movies
		}
		if in.MediaType == "tv" {
			out.Total = out.TV
		}
	}
	rows, err := q.ReadSubscriptionListEntries(ctx, params)
	if err != nil {
		return nil, ports.ErrUnavailable
	}
	items := make([]ports.SubscriptionListEntry, len(rows))
	positions := make(map[uuid.UUID]int, len(rows))
	for i, row := range rows {
		item := ports.SubscriptionListEntry{
			ID: row.ID, UserID: row.UserID, MediaID: row.MediaID, RootID: row.RootID,
			QualityPolicyID: row.QualityPolicyID, QualityPolicyRevision: row.QualityPolicyRevision,
			State: row.State, IntentVersion: row.IntentVersion, ScopeType: row.ScopeType,
			Title: row.MediaTitle, MediaType: row.MediaType, RootName: row.RootName,
			TMDBExternalID: row.TmdbExternalID, IndexerIDs: []uuid.UUID{}, WantedItems: []ports.SubscriptionListWanted{},
		}
		item.TotalEpisodes, item.SatisfiedEpisodes = int(row.TotalEpisodes), int(row.SatisfiedEpisodes)
		if poster := media.TMDBPosterURL(row.PosterPath); poster != "" {
			item.PosterURL = &poster
		}
		if row.MediaYear.Valid {
			year := int(row.MediaYear.Int32)
			item.Year = &year
		}
		if account.Role == ports.RoleAdmin {
			path := row.RootPath
			item.RootPath = &path
		}
		// Preserve legacy bindings decoding; credentials and endpoint URLs are never loaded.
		_ = json.Unmarshal(row.IndexerIds, &item.IndexerIDs)
		if row.DownloaderID.Valid {
			value := uuid.UUID(row.DownloaderID.Bytes)
			item.DownloaderID = &value
		}
		items[i], positions[row.ID] = item, i
	}
	if len(wantedIDs) > 0 {
		wanted, err := q.ReadSubscriptionListWanted(ctx, wantedIDs)
		if err != nil {
			return nil, ports.ErrUnavailable
		}
		for _, row := range wanted {
			pos, ok := positions[row.SubscriptionID]
			if !ok {
				return nil, ports.ErrUnavailable
			}
			var air *time.Time
			if row.AirDate.Valid {
				value := row.AirDate.Time
				air = &value
			}
			var absolute *int
			if row.AbsoluteNumber.Valid {
				value := int(row.AbsoluteNumber.Int32)
				absolute = &value
			}
			items[pos].WantedItems = append(items[pos].WantedItems, ports.SubscriptionListWanted{
				ID: row.ID, UnitID: row.UnitID, UnitKey: row.UnitKey, IsSkipped: row.IsSkipped,
				IsSatisfied: row.IsSatisfied, CurrentFactStage: row.CurrentFactStage,
				UserSkipped: row.UserSkipped, ScopeExcluded: row.ScopeExcluded, Retired: row.Retired,
				AirDate: air, AbsoluteNumber: absolute, MetadataVersion: row.MetadataVersion,
			})
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, ports.ErrUnavailable
	}
	out.Items = items
	return out, nil
}
