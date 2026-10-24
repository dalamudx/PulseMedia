package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/domain/notification"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type ChannelStateStore struct {
	pool *pgxpool.Pool
	ids  ports.IDGen
}

func NewChannelStateStore(pool *pgxpool.Pool, ids ports.IDGen) *ChannelStateStore {
	return &ChannelStateStore{pool, ids}
}
func (s *ChannelStateStore) SetChannelState(ctx context.Context, a *ports.Actor, id uuid.UUID, c ports.ChannelStateCommand, key, hash string) (out *ports.ChannelStateReceipt, err error) {
	if a == nil {
		return nil, ports.ErrUnauthorized
	}
	if !a.IsAdmin() {
		return nil, ports.ErrForbidden
	}
	if id == uuid.Nil || c.ExpectedUpdatedAt.IsZero() || key == "" || len(key) > 160 || len(hash) != 64 {
		return nil, ports.ErrInvalidChannelState
	}
	defer func() {
		if err != nil && !errors.Is(err, ports.ErrForbidden) && !errors.Is(err, ports.ErrUnauthorized) && !errors.Is(err, ports.ErrNotFound) && !errors.Is(err, ports.ErrChannelStateConflict) {
			if ctx.Err() != nil {
				err = ctx.Err()
			} else {
				err = ports.ErrUnavailable
			}
		}
	}()
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := db.New(tx)
	if e = lockAcquisitionUser(ctx, q, a); e != nil {
		return nil, e
	}
	lock, _ := json.Marshal([]string{string(a.Type), a.ID, key})
	if e = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "notification.channel.state.command", AggregateID: string(lock)}); e != nil {
		return nil, e
	}
	channel, e := q.LockNotificationChannel(ctx, id)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, ports.ErrNotFound
	}
	if e != nil {
		return nil, e
	}
	old, e := q.GetIdempotencyRecord(ctx, db.GetIdempotencyRecordParams{ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: key})
	if e == nil {
		var receipt ports.ChannelStateReceipt
		if old.RequestHash != hash || json.Unmarshal(old.ResponseBody, &receipt) != nil || receipt.ID != id {
			return nil, ports.ErrChannelStateConflict
		}
		return &receipt, nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return nil, e
	}
	if !channel.UpdatedAt.Time.Equal(c.ExpectedUpdatedAt) || (c.IsEnabled && (len(channel.ConfigEncrypted) < 40 || !notification.ValidKind(channel.ChannelType))) {
		return nil, ports.ErrChannelStateConflict
	}
	next, e := q.SetNotificationChannelState(ctx, db.SetNotificationChannelStateParams{ID: id, IsEnabled: c.IsEnabled, UpdatedAt: pgtype.Timestamptz{Time: c.ExpectedUpdatedAt, Valid: true}})
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, ports.ErrChannelStateConflict
	}
	if e != nil {
		return nil, e
	}
	out = &ports.ChannelStateReceipt{ID: id, IsEnabled: next.IsEnabled, UpdatedAt: next.UpdatedAt.Time}
	raw, _ := json.Marshal(out)
	if _, e = q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: s.ids.NewUUID(), ActorType: string(a.Type), ActorID: a.ID, Action: "notification_channel.state_changed", TargetType: "notification_channel", TargetID: id.String(), Payload: raw}); e != nil {
		return nil, e
	}
	if e = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "notification_channel", AggregateID: id.String()}); e != nil {
		return nil, e
	}
	seq, e := q.GetNextAggregateSeq(ctx, db.GetNextAggregateSeqParams{AggregateType: "notification_channel", AggregateID: id.String()})
	if e != nil {
		return nil, e
	}
	if _, e = q.InsertOutboxEvent(ctx, db.InsertOutboxEventParams{ID: s.ids.NewUUID(), AggregateType: "notification_channel", AggregateID: id.String(), EventType: "notification_channel.state_changed", Seq: int64(seq), Payload: raw}); e != nil {
		return nil, e
	}
	if _, e = q.CreateIdempotencyRecord(ctx, db.CreateIdempotencyRecordParams{ID: s.ids.NewUUID(), ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: key, RequestHash: hash, ResponseCode: 200, ResponseBody: raw, ResourceID: pgtype.Text{String: id.String(), Valid: true}}); e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return out, nil
}
