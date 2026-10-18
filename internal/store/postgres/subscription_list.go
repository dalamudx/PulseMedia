package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type SubscriptionListStore struct{ pool *pgxpool.Pool }

var _ ports.SubscriptionListReader = (*SubscriptionListStore)(nil)

func NewSubscriptionListStore(pool *pgxpool.Pool) *SubscriptionListStore {
	return &SubscriptionListStore{pool: pool}
}

func (s *SubscriptionListStore) ReadSubscriptionList(ctx context.Context, actor *ports.Actor) ([]ports.SubscriptionListEntry, error) {
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
	rows, err := q.ReadSubscriptionListEntries(ctx, db.ReadSubscriptionListEntriesParams{IsAdmin: account.Role == ports.RoleAdmin, UserID: id})
	if err != nil {
		return nil, ports.ErrUnavailable
	}
	items := make([]ports.SubscriptionListEntry, len(rows))
	ids := make([]uuid.UUID, len(rows))
	positions := make(map[uuid.UUID]int, len(rows))
	for i, row := range rows {
		item := ports.SubscriptionListEntry{
			ID: row.ID, UserID: row.UserID, MediaID: row.MediaID, RootID: row.RootID,
			QualityPolicyID: row.QualityPolicyID, QualityPolicyRevision: row.QualityPolicyRevision,
			State: row.State, IntentVersion: row.IntentVersion, ScopeType: row.ScopeType,
			Title: row.MediaTitle, MediaType: row.MediaType, RootName: row.RootName,
			TMDBExternalID: row.TmdbExternalID, IndexerIDs: []uuid.UUID{}, WantedItems: []ports.SubscriptionListWanted{},
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
		items[i], ids[i], positions[row.ID] = item, row.ID, i
	}
	if len(ids) > 0 {
		wanted, err := q.ReadSubscriptionListWanted(ctx, ids)
		if err != nil {
			return nil, ports.ErrUnavailable
		}
		for _, row := range wanted {
			pos, ok := positions[row.SubscriptionID]
			if !ok {
				return nil, ports.ErrUnavailable
			}
			items[pos].WantedItems = append(items[pos].WantedItems, ports.SubscriptionListWanted{
				ID: row.ID, UnitID: row.UnitID, UnitKey: row.UnitKey, IsSkipped: row.IsSkipped,
				IsSatisfied: row.IsSatisfied, CurrentFactStage: row.CurrentFactStage,
			})
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, ports.ErrUnavailable
	}
	return items, nil
}
