package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var _ ports.SubscriptionDetailsReader = (*SubscriptionListStore)(nil)

func (s *SubscriptionListStore) ReadSubscriptionDetails(ctx context.Context, a *ports.Actor, id uuid.UUID) (*ports.SubscriptionDetails, error) {
	if a == nil {
		return nil, ports.ErrUnauthorized
	}
	user, e := uuid.Parse(a.ID)
	if e != nil || user == uuid.Nil || a.Type != ports.ActorTypeUser || (a.Role != ports.RoleAdmin && a.Role != ports.RoleMember) {
		return nil, ports.ErrForbidden
	}
	if a.Scopes != nil {
		read := false
		for _, scope := range a.Scopes {
			read = read || scope == "read"
		}
		if !read {
			return nil, ports.ErrForbidden
		}
	}
	if s == nil || s.pool == nil {
		return nil, ports.ErrUnavailable
	}
	for attempt := 0; ; attempt++ {
		out, err := s.readSubscriptionDetails(ctx, a, user, id)
		var cause *pgconn.PgError
		if attempt < 2 && ctx.Err() == nil && errors.As(err, &cause) && cause.Code == "40001" {
			continue
		}
		if err != nil && !errors.Is(err, ports.ErrForbidden) && !errors.Is(err, ports.ErrNotFound) {
			err = ports.ErrUnavailable
		}
		return out, err
	}
}

func (s *SubscriptionListStore) readSubscriptionDetails(ctx context.Context, a *ports.Actor, user, id uuid.UUID) (*ports.SubscriptionDetails, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)
	account, err := q.LockSubscriptionListAccount(ctx, user)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ports.ErrForbidden
	}
	if err != nil {
		return nil, err
	}
	if !account.IsActive || account.Role != a.Role {
		return nil, ports.ErrForbidden
	}
	if err = q.SetSubscriptionListReadOnly(ctx); err != nil {
		return nil, err
	}
	sub, err := q.GetSubscriptionByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ports.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if account.Role != ports.RoleAdmin && sub.UserID != user {
		return nil, ports.ErrForbidden
	}
	media, err := q.GetMediaByID(ctx, sub.MediaID)
	if err != nil {
		return nil, err
	}
	root, err := q.GetLibraryRootByID(ctx, sub.RootID)
	if err != nil {
		return nil, err
	}
	wanted, err := q.ListWantedItemsWithUnitDetails(ctx, id)
	if err != nil {
		return nil, err
	}
	out := &ports.SubscriptionDetails{SubscriptionListEntry: ports.SubscriptionListEntry{
		ID: sub.ID, UserID: sub.UserID, MediaID: sub.MediaID, RootID: sub.RootID, QualityPolicyID: sub.QualityPolicyID, QualityPolicyRevision: sub.QualityPolicyRevision,
		State: sub.State, IntentVersion: sub.IntentVersion, ScopeType: sub.ScopeType, Title: media.Title, MediaType: media.MediaType, RootName: root.Name,
		IndexerIDs: []uuid.UUID{}, WantedItems: make([]ports.SubscriptionListWanted, 0, len(wanted)),
	}, SelectedUnitIDs: []uuid.UUID{}, Seasons: []int{}, IncludeSpecials: sub.IncludeSpecials}
	if json.Unmarshal(sub.SelectedUnitIds, &out.SelectedUnitIDs) != nil || json.Unmarshal(sub.Seasons, &out.Seasons) != nil || json.Unmarshal(sub.IndexerIds, &out.IndexerIDs) != nil {
		return nil, ports.ErrUnavailable
	}
	if media.Year.Valid {
		v := int(media.Year.Int32)
		out.Year = &v
	}
	if account.Role == ports.RoleAdmin {
		path := root.Path
		out.RootPath = &path
	}
	if sub.DownloaderID.Valid {
		v := uuid.UUID(sub.DownloaderID.Bytes)
		out.DownloaderID = &v
	}
	for _, w := range wanted {
		item := ports.SubscriptionListWanted{ID: w.ID, UnitID: w.UnitID, UnitKey: w.UnitKey, IsSkipped: w.IsSkipped, IsSatisfied: w.IsSatisfied, CurrentFactStage: w.CurrentFactStage,
			UserSkipped: w.UserSkipped, ScopeExcluded: w.ScopeExcluded, Retired: w.Retired, MetadataVersion: w.MetadataVersion}
		if w.AirDate.Valid {
			v := w.AirDate.Time
			item.AirDate = &v
		}
		if w.AbsoluteNumber.Valid {
			v := int(w.AbsoluteNumber.Int32)
			item.AbsoluteNumber = &v
		}
		out.WantedItems = append(out.WantedItems, item)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}
