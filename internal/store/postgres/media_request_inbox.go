package postgres

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

var _ ports.MediaRequestInboxReader = (*MediaRequestStore)(nil)

func (s *MediaRequestStore) ReadMediaRequestInbox(ctx context.Context, a *ports.Actor) (out *ports.MediaRequestInboxSummary, err error) {
	defer func() { err = mediaRequestError(ctx, err) }()
	tx, q, actor, err := s.begin(ctx, a, false)
	if err != nil {
		return nil, err
	}
	defer creationRollback(ctx, tx)
	if actor.Role != ports.RoleAdmin {
		return nil, ports.ErrForbidden
	}
	row, err := q.ReadMediaRequestInbox(ctx)
	if err != nil {
		return nil, err
	}
	if row.PendingCount < 0 || !row.ObservedAt.Valid {
		return nil, ports.ErrUnavailable
	}
	return &ports.MediaRequestInboxSummary{PendingCount: row.PendingCount, ObservedAt: row.ObservedAt.Time.UTC()}, nil
}

func (s *MediaRequestStore) ListPendingMediaRequests(ctx context.Context, a *ports.Actor, limit int, before *uuid.UUID) (out []ports.MediaRequest, err error) {
	defer func() { err = mediaRequestError(ctx, err) }()
	tx, q, actor, err := s.begin(ctx, a, false)
	if err != nil {
		return nil, err
	}
	defer creationRollback(ctx, tx)
	if actor.Role != ports.RoleAdmin {
		return nil, ports.ErrForbidden
	}
	if limit < 1 || limit > 100 || before != nil && *before == uuid.Nil {
		return nil, ports.ErrRejected
	}
	cursor := pgtype.UUID{}
	if before != nil {
		if _, err := mediaRequestRead(ctx, q, actor, *before); err != nil {
			return nil, err
		}
		cursor = pgtype.UUID{Bytes: *before, Valid: true}
	}
	rows, err := q.ListPendingMediaRequests(ctx, db.ListPendingMediaRequestsParams{BeforeID: cursor, PageLimit: int32(limit)})
	if err != nil {
		return nil, err
	}
	out = make([]ports.MediaRequest, 0, len(rows))
	for _, row := range rows {
		if _, err := mediaRequestOriginal(row); err != nil {
			return nil, err
		}
		item, err := mediaRequestDTO(row)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, nil
}
