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
	"strings"
)

type DeliveryManagementStore struct {
	pool *pgxpool.Pool
	ids  ports.IDGen
}

func NewDeliveryManagementStore(pool *pgxpool.Pool, ids ports.IDGen) *DeliveryManagementStore {
	return &DeliveryManagementStore{pool, ids}
}
func integrationRecipient(ctx context.Context, q *db.Queries, consumer string) (string, bool, string, error) {
	kind, id, ok := strings.Cut(consumer, ":")
	if !ok {
		return "", false, "", ports.ErrDeliveryCommandConflict
	}
	uid, e := uuid.Parse(id)
	if e != nil {
		return "", false, "", ports.ErrDeliveryCommandConflict
	}
	if kind == "notify" {
		channel, e := q.GetNotificationChannelByID(ctx, uid)
		if e != nil {
			return "", false, "", e
		}
		risk := "receiver_event_id_dedup_required"
		if channel.ChannelType == "telegram" {
			risk = "telegram_may_duplicate"
		}
		return channel.Name, channel.IsEnabled, risk, nil
	}
	if kind == "mediaserver.refresh" {
		server, e := q.GetMediaServerByID(ctx, uid)
		if e != nil {
			return "", false, "", e
		}
		return server.Name, true, "media_server_refresh_may_repeat", nil
	}
	return "", false, "", ports.ErrDeliveryCommandConflict
}
func (s *DeliveryManagementStore) ListIntegrationDeliveries(ctx context.Context, a *ports.Actor) ([]ports.DeliverySummary, error) {
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
	rows, e := q.ListManagedIntegrationDeliveries(ctx)
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	out := []ports.DeliverySummary{}
	for _, r := range rows {
		name, enabled, risk, e := integrationRecipient(ctx, q, r.Consumer)
		if e != nil {
			if errors.Is(e, pgx.ErrNoRows) || errors.Is(e, ports.ErrDeliveryCommandConflict) {
				name, risk = "recipient unavailable", "recipient_unavailable"
				if errors.Is(e, ports.ErrDeliveryCommandConflict) {
					r.Consumer = "recipient.unavailable:" + r.ID.String()
				}
			} else {
				return nil, ports.ErrUnavailable
			}
		}
		if e == nil && !enabled {
			risk = "recipient_disabled"
		}
		out = append(out, ports.DeliverySummary{ID: r.ID, EventID: r.EventID, Consumer: r.Consumer, AggregateType: r.AggregateType, AggregateID: r.AggregateID, Sequence: r.Seq, State: r.State, Attempts: int(r.Attempts), LeaseToken: r.LeaseToken, UpdatedAt: r.UpdatedAt.Time, NotBefore: r.NotBefore.Time, RecipientName: name, DuplicateRisk: risk})
	}
	return out, nil
}
func (s *DeliveryManagementStore) ReplayIntegrationDelivery(ctx context.Context, a *ports.Actor, id uuid.UUID, c ports.DeliveryReplayCommand, key, hash string) (out *ports.DeliveryReplayReceipt, err error) {
	if a == nil {
		return nil, ports.ErrUnauthorized
	}
	if !a.IsAdmin() {
		return nil, ports.ErrForbidden
	}
	if id == uuid.Nil || !c.AcceptDuplicateRisk || c.ExpectedToken < 0 || c.ExpectedAttempts < 1 || c.ExpectedUpdatedAt.IsZero() || key == "" || len(key) > 160 || len(hash) != 64 {
		return nil, ports.ErrInvalidDeliveryCommand
	}
	defer func() {
		if err != nil && !errors.Is(err, ports.ErrForbidden) && !errors.Is(err, ports.ErrUnauthorized) && !errors.Is(err, ports.ErrNotFound) && !errors.Is(err, ports.ErrDeliveryCommandConflict) {
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
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)
	if e = lockAcquisitionUser(ctx, q, a); e != nil {
		return nil, e
	}
	lock, _ := json.Marshal([]string{string(a.Type), a.ID, key})
	if e = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "integration.delivery.command", AggregateID: string(lock)}); e != nil {
		return nil, e
	}
	d, e := q.LockOutboxDelivery(ctx, id)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, ports.ErrNotFound
	}
	if e != nil {
		return nil, e
	}
	old, e := q.GetIdempotencyRecord(ctx, db.GetIdempotencyRecordParams{ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: key})
	if e == nil {
		var result ports.DeliveryReplayReceipt
		if old.RequestHash != hash || json.Unmarshal(old.ResponseBody, &result) != nil || result.ID != id {
			return nil, ports.ErrDeliveryCommandConflict
		}
		return &result, nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return nil, e
	}
	_, enabled, _, e := integrationRecipient(ctx, q, d.Consumer)
	if e != nil || !enabled {
		return nil, ports.ErrDeliveryCommandConflict
	}
	if d.State != "dead" || d.LeaseToken != c.ExpectedToken || int(d.Attempts) != c.ExpectedAttempts || !d.UpdatedAt.Time.Equal(c.ExpectedUpdatedAt) || d.LeaseOwner.Valid || d.LeaseUntil.Valid {
		return nil, ports.ErrDeliveryCommandConflict
	}
	next, e := q.ReplayDeadIntegrationDelivery(ctx, db.ReplayDeadIntegrationDeliveryParams{ID: id, LeaseToken: c.ExpectedToken, Attempts: int32(c.ExpectedAttempts), UpdatedAt: pgtype.Timestamptz{Time: c.ExpectedUpdatedAt, Valid: true}})
	if e != nil {
		return nil, e
	}
	out = &ports.DeliveryReplayReceipt{ID: id, EventID: next.EventID, State: next.State, LeaseToken: next.LeaseToken}
	raw, _ := json.Marshal(out)
	if _, e = q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: s.ids.NewUUID(), ActorType: string(a.Type), ActorID: a.ID, Action: "integration.delivery_replayed", TargetType: "outbox_delivery", TargetID: id.String(), Payload: raw}); e != nil {
		return nil, e
	}
	if e = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "outbox_delivery", AggregateID: id.String()}); e != nil {
		return nil, e
	}
	seq, e := q.GetNextAggregateSeq(ctx, db.GetNextAggregateSeqParams{AggregateType: "outbox_delivery", AggregateID: id.String()})
	if e != nil {
		return nil, e
	}
	if _, e = q.InsertOutboxEvent(ctx, db.InsertOutboxEventParams{ID: s.ids.NewUUID(), AggregateType: "outbox_delivery", AggregateID: id.String(), EventType: "integration.delivery_replayed", Seq: int64(seq), Payload: raw}); e != nil {
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
