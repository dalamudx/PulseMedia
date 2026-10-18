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

type NotificationChannelStore struct {
	pool *pgxpool.Pool
	ids  ports.IDGen
}

func NewNotificationChannelStore(pool *pgxpool.Pool, ids ports.IDGen) *NotificationChannelStore {
	return &NotificationChannelStore{pool, ids}
}
func channelObservation(row db.NotificationChannel) ports.NotificationChannelObservation {
	out := ports.NotificationChannelObservation{ID: row.ID, Name: row.Name, ChannelType: row.ChannelType, IsEnabled: row.IsEnabled, UpdatedAt: row.UpdatedAt.Time}
	if row.ReplacedBy.Valid {
		id := uuid.UUID(row.ReplacedBy.Bytes)
		out.ReplacedBy = &id
	}
	return out
}
func (s *NotificationChannelStore) CreateNotificationChannel(ctx context.Context, actor *ports.Actor, c ports.NotificationChannelWrite, key, hash string) (*ports.NotificationChannelObservation, error) {
	if actor == nil {
		return nil, ports.ErrUnauthorized
	}
	if !actor.IsAdmin() {
		return nil, ports.ErrForbidden
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := db.New(tx)
	if e = lockAcquisitionUser(ctx, q, actor); e != nil {
		return nil, e
	}
	if key != "" {
		lock, _ := json.Marshal([]string{string(actor.Type), actor.ID, key})
		if e = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "notification.channel.create", AggregateID: string(lock)}); e != nil {
			return nil, ports.ErrUnavailable
		}
		old, err := q.GetIdempotencyRecord(ctx, db.GetIdempotencyRecordParams{ActorType: string(actor.Type), ActorID: actor.ID, IdempotencyKey: key})
		if err == nil {
			var receipt struct {
				ID uuid.UUID `json:"id"`
			}
			if old.RequestHash != hash || json.Unmarshal(old.ResponseBody, &receipt) != nil || receipt.ID == uuid.Nil || !old.ResourceID.Valid || old.ResourceID.String != receipt.ID.String() {
				return nil, ports.ErrChannelCreateConflict
			}
			row, err := q.GetNotificationChannelByID(ctx, receipt.ID)
			if err != nil {
				return nil, ports.ErrUnavailable
			}
			out := channelObservation(row)
			return &out, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, ports.ErrUnavailable
		}
	}
	row, e := q.CreateNotificationChannel(ctx, db.CreateNotificationChannelParams{ID: s.ids.NewUUID(), Name: c.Name, ChannelType: c.ChannelType, ConfigEncrypted: c.Ciphertext})
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	if _, e = q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: s.ids.NewUUID(), ActorType: string(actor.Type), ActorID: actor.ID, Action: "notification_channel.created", TargetType: "notification_channel", TargetID: row.ID.String(), Payload: []byte("{}")}); e != nil {
		return nil, ports.ErrUnavailable
	}
	if key != "" {
		raw, _ := json.Marshal(map[string]string{"id": row.ID.String()})
		if _, e = q.CreateIdempotencyRecord(ctx, db.CreateIdempotencyRecordParams{ID: s.ids.NewUUID(), ActorType: string(actor.Type), ActorID: actor.ID, IdempotencyKey: key, RequestHash: hash, ResponseCode: 201, ResponseBody: raw, ResourceID: pgtype.Text{String: row.ID.String(), Valid: true}}); e != nil {
			return nil, ports.ErrUnavailable
		}
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, ports.ErrUnavailable
	}
	out := channelObservation(row)
	return &out, nil
}
func (s *NotificationChannelStore) ListNotificationChannels(ctx context.Context, actor *ports.Actor) ([]ports.NotificationChannelObservation, error) {
	if actor == nil {
		return nil, ports.ErrUnauthorized
	}
	if !actor.IsAdmin() {
		return nil, ports.ErrForbidden
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := db.New(tx)
	if e = lockAcquisitionUser(ctx, q, actor); e != nil {
		return nil, e
	}
	rows, e := q.ListNotificationChannels(ctx)
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	out := make([]ports.NotificationChannelObservation, 0, len(rows))
	for _, r := range rows {
		out = append(out, channelObservation(r))
	}
	return out, nil
}
