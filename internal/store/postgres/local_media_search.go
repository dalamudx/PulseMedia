package postgres

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type LocalMediaSearchStore struct{ pool *pgxpool.Pool }

var _ ports.LocalMediaSearchReader = (*LocalMediaSearchStore)(nil)

func NewLocalMediaSearchStore(pool *pgxpool.Pool) *LocalMediaSearchStore {
	return &LocalMediaSearchStore{pool: pool}
}

func (s *LocalMediaSearchStore) SearchLocalMedia(ctx context.Context, actor *ports.Actor, query, kind string, year int) ([]*ports.MovieMetadata, error) {
	if actor == nil {
		return nil, ports.ErrUnauthorized
	}
	id, err := uuid.Parse(actor.ID)
	if err != nil || id == uuid.Nil || actor.Type != ports.ActorTypeUser || (actor.Role != ports.RoleAdmin && actor.Role != ports.RoleMember) {
		return nil, ports.ErrForbidden
	}
	if actor.Scopes != nil {
		readable := false
		for _, scope := range actor.Scopes {
			readable = readable || scope == "read"
		}
		if !readable {
			return nil, ports.ErrForbidden
		}
	}
	if s == nil || s.pool == nil {
		return nil, ports.ErrUnavailable
	}
	query = strings.TrimSpace(query)
	if query == "" || (kind != "movie" && kind != "tv" && kind != "multi") || year < 0 || year > 9999 {
		return nil, ports.ErrBadResponse
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return nil, ports.ErrUnavailable
	}
	defer func() {
		rollback, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(rollback)
	}()
	queries := db.New(tx)
	account, err := queries.LockSubscriptionListAccount(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ports.ErrForbidden
	}
	if err != nil {
		return nil, ports.ErrUnavailable
	}
	if !account.IsActive || account.Role != actor.Role {
		return nil, ports.ErrForbidden
	}
	if err = queries.SetSubscriptionListReadOnly(ctx); err != nil {
		return nil, ports.ErrUnavailable
	}
	rows, err := queries.SearchLocalMedia(ctx, db.SearchLocalMediaParams{MediaKind: kind, ReleaseYear: int32(year), SearchQuery: query})
	if err != nil {
		return nil, ports.ErrUnavailable
	}
	result := make([]*ports.MovieMetadata, 0, len(rows))
	for _, row := range rows {
		item := &ports.MovieMetadata{MediaType: row.MediaType, Title: row.Title}
		if row.Year.Valid {
			item.Year = int(row.Year.Int32)
		}
		if value, parseErr := strconv.ParseInt(row.TmdbExternalID, 10, 64); parseErr == nil && value > 0 {
			item.TMDBID = value
		}
		result = append(result, item)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, ports.ErrUnavailable
	}
	return result, nil
}
