package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type ChannelReplacementStore struct {
	pool *pgxpool.Pool
	ids  ports.IDGen
}

func NewChannelReplacementStore(pool *pgxpool.Pool, ids ports.IDGen) *ChannelReplacementStore {
	return &ChannelReplacementStore{pool, ids}
}
func (s *ChannelReplacementStore) ReplaceNotificationChannel(ctx context.Context, a *ports.Actor, id uuid.UUID, c ports.ChannelReplacementWrite, key, hash string) (out *ports.ChannelReplacementReceipt, err error) {
	if a == nil {
		return nil, ports.ErrUnauthorized
	}
	if !a.IsAdmin() {
		return nil, ports.ErrForbidden
	}
	if id == uuid.Nil || c.ExpectedUpdatedAt.IsZero() || c.Name == "" || len([]rune(c.Name)) > 120 || (c.ChannelType != "webhook" && c.ChannelType != "telegram") || len(c.Ciphertext) < 32 || len(c.Ciphertext) > 12000 || key == "" || len(key) > 160 || len(hash) != 64 {
		return nil, ports.ErrInvalidChannelReplacement
	}
	defer func() {
		if err != nil && !errors.Is(err, ports.ErrForbidden) && !errors.Is(err, ports.ErrUnauthorized) && !errors.Is(err, ports.ErrNotFound) && !errors.Is(err, ports.ErrChannelReplacementConflict) {
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
	old, e := q.LockNotificationChannel(ctx, id)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, ports.ErrNotFound
	}
	if e != nil {
		return nil, e
	}
	cached, e := q.GetIdempotencyRecord(ctx, db.GetIdempotencyRecordParams{ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: key})
	if e == nil {
		var r ports.ChannelReplacementReceipt
		if cached.RequestHash != hash || json.Unmarshal(cached.ResponseBody, &r) != nil || r.OldChannelID != id || r.NewChannelID == uuid.Nil {
			return nil, ports.ErrChannelReplacementConflict
		}
		return &r, nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return nil, e
	}
	if old.ReplacedBy.Valid || !old.UpdatedAt.Time.Equal(c.ExpectedUpdatedAt) {
		return nil, ports.ErrChannelReplacementConflict
	}
	next, e := q.CreateNotificationChannel(ctx, db.CreateNotificationChannelParams{ID: s.ids.NewUUID(), Name: c.Name, ChannelType: c.ChannelType, ConfigEncrypted: c.Ciphertext})
	if e != nil {
		return nil, e
	}
	stopped, e := q.ReplaceNotificationChannel(ctx, db.ReplaceNotificationChannelParams{ID: id, UpdatedAt: pgtype.Timestamptz{Time: c.ExpectedUpdatedAt, Valid: true}, ReplacedBy: pgtype.UUID{Bytes: next.ID, Valid: true}})
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, ports.ErrChannelReplacementConflict
	}
	if e != nil {
		return nil, e
	}
	out = &ports.ChannelReplacementReceipt{OldChannelID: id, NewChannelID: next.ID, OldEnabled: false, NewEnabled: true, OldUpdatedAt: stopped.UpdatedAt.Time, NewUpdatedAt: next.UpdatedAt.Time}
	raw, _ := json.Marshal(out)
	if _, e = q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: s.ids.NewUUID(), ActorType: string(a.Type), ActorID: a.ID, Action: "notification_channel.replaced", TargetType: "notification_channel", TargetID: id.String(), Payload: raw}); e != nil {
		return nil, e
	}
	if e = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "notification_channel", AggregateID: id.String()}); e != nil {
		return nil, e
	}
	seq, e := q.GetNextAggregateSeq(ctx, db.GetNextAggregateSeqParams{AggregateType: "notification_channel", AggregateID: id.String()})
	if e != nil {
		return nil, e
	}
	if _, e = q.InsertOutboxEvent(ctx, db.InsertOutboxEventParams{ID: s.ids.NewUUID(), AggregateType: "notification_channel", AggregateID: id.String(), EventType: "notification_channel.replaced", Seq: int64(seq), Payload: raw}); e != nil {
		return nil, e
	}
	if _, e = q.CreateIdempotencyRecord(ctx, db.CreateIdempotencyRecordParams{ID: s.ids.NewUUID(), ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: key, RequestHash: hash, ResponseCode: 201, ResponseBody: raw, ResourceID: pgtype.Text{String: next.ID.String(), Valid: true}}); e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return out, nil
}
