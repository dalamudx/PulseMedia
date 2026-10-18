package postgres

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type NotificationBatchQueryStore struct{ pool *pgxpool.Pool }

func NewNotificationBatchQueryStore(pool *pgxpool.Pool) *NotificationBatchQueryStore {
	return &NotificationBatchQueryStore{pool}
}
func (s *NotificationBatchQueryStore) ListNotificationBatches(ctx context.Context, a *ports.Actor) ([]ports.NotificationBatchSummary, error) {
	if a == nil {
		return nil, ports.ErrUnauthorized
	}
	if !a.IsAdmin() {
		return nil, ports.ErrForbidden
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := db.New(tx)
	if e = lockAcquisitionUser(ctx, q, a); e != nil {
		return nil, e
	}
	rows, e := q.ListManagedNotificationBatches(ctx)
	if e != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ports.ErrUnavailable
	}
	out := []ports.NotificationBatchSummary{}
	for _, r := range rows {
		row := ports.NotificationBatchSummary{ID: r.ID, MediaID: r.MediaID, ChannelID: r.ChannelID, RecipientName: r.RecipientName, RecipientEnabled: r.RecipientEnabled, State: r.ObservedState, Deadline: r.Deadline.Time, MemberCount: int(r.MemberCount), UnitCount: int(r.UnitCount), FileCount: int(r.FileCount), RootCount: int(r.RootCount)}
		if r.EventID.Valid {
			id := uuid.UUID(r.EventID.Bytes)
			row.EventID = &id
		}
		out = append(out, row)
	}
	return out, nil
}
