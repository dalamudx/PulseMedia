package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type notificationTestPublisher interface {
	Publish(context.Context, *db.Queries, string, string, string, any, []string) (*db.OutboxEvent, error)
}

type NotificationTestStore struct {
	pool   *pgxpool.Pool
	ids    ports.IDGen
	outbox notificationTestPublisher
}

func NewNotificationTestStore(pool *pgxpool.Pool, ids ports.IDGen, ob notificationTestPublisher) *NotificationTestStore {
	return &NotificationTestStore{pool, ids, ob}
}
func (s *NotificationTestStore) QueueNotificationTest(ctx context.Context, a *ports.Actor, id uuid.UUID, expected time.Time, key, hash string) (out *ports.NotificationTestReceipt, err error) {
	if a == nil {
		return nil, ports.ErrUnauthorized
	}
	if a.Type != ports.ActorTypeUser || a.Scopes != nil || !a.IsAdmin() {
		return nil, ports.ErrForbidden
	}
	if id == uuid.Nil || expected.IsZero() || key == "" || len(key) > 160 || len(hash) != 64 {
		return nil, ports.ErrChannelStateConflict
	}
	if s == nil || s.pool == nil || s.ids == nil || s.outbox == nil {
		return nil, ports.ErrUnavailable
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
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer managementRollback(ctx, tx)
	q := db.New(tx)
	if err = lockAcquisitionUser(ctx, q, a); err != nil {
		return nil, err
	}
	lock, _ := json.Marshal([]string{string(a.Type), a.ID, key})
	if err = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "notification.test.command", AggregateID: string(lock)}); err != nil {
		return nil, err
	}
	old, e := q.GetIdempotencyRecord(ctx, db.GetIdempotencyRecordParams{ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: key})
	if e == nil {
		var r ports.NotificationTestReceipt
		if old.RequestHash != hash || json.Unmarshal(old.ResponseBody, &r) != nil || r.ChannelID != id || r.EventID == uuid.Nil || r.Status != "queued" || !old.ResourceID.Valid || old.ResourceID.String != r.EventID.String() {
			return nil, ports.ErrChannelStateConflict
		}
		return &r, nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return nil, e
	}
	channel, e := q.LockNotificationChannel(ctx, id)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, ports.ErrNotFound
	}
	if e != nil {
		return nil, e
	}
	if !channel.IsEnabled || channel.ReplacedBy.Valid || !channel.UpdatedAt.Time.Equal(expected) || len(channel.ConfigEncrypted) < 40 {
		return nil, ports.ErrChannelStateConflict
	}
	event, e := s.outbox.Publish(ctx, q, "notification.test", s.ids.NewUUID().String(), "notification.test.v1", map[string]string{"version": "notification.test.v1"}, []string{"notify:" + id.String()})
	if e != nil {
		return nil, e
	}
	if _, e = q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: s.ids.NewUUID(), ActorType: string(a.Type), ActorID: a.ID, Action: "notification.test.queued", TargetType: "notification_channel", TargetID: id.String(), Payload: []byte("{}")}); e != nil {
		return nil, e
	}
	out = &ports.NotificationTestReceipt{ChannelID: id, EventID: event.ID, Status: "queued"}
	raw, _ := json.Marshal(out)
	if _, e = q.CreateIdempotencyRecord(ctx, db.CreateIdempotencyRecordParams{ID: s.ids.NewUUID(), ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: key, RequestHash: hash, ResponseCode: 202, ResponseBody: raw, ResourceID: pgtype.Text{String: event.ID.String(), Valid: true}}); e != nil {
		return nil, e
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}
