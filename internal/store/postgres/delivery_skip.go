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
	"strings"
)

func (s *DeliveryManagementStore) SkipIntegrationDelivery(ctx context.Context, a *ports.Actor, id uuid.UUID, c ports.DeliverySkipCommand, key, hash string) (out *ports.DeliverySkipReceipt, err error) {
	if a == nil {
		return nil, ports.ErrUnauthorized
	}
	if !a.IsAdmin() {
		return nil, ports.ErrForbidden
	}
	if id == uuid.Nil || !c.AcceptDeliveryLoss || c.ExpectedToken < 0 || c.ExpectedAttempts < 0 || c.ExpectedAttempts > 2147483647 || c.ExpectedUpdatedAt.IsZero() || key == "" || len(key) > 160 || len(hash) != 64 || (c.Reason != "handled_elsewhere" && c.Reason != "recipient_retired" && c.Reason != "event_obsolete") {
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
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
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
		var r ports.DeliverySkipReceipt
		if old.RequestHash != hash || json.Unmarshal(old.ResponseBody, &r) != nil || r.ID != id {
			return nil, ports.ErrDeliveryCommandConflict
		}
		return &r, nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return nil, e
	}
	if (!strings.HasPrefix(d.Consumer, "notify:") && !strings.HasPrefix(d.Consumer, "mediaserver.refresh:")) || d.State != "dead" || d.LeaseToken != c.ExpectedToken || int(d.Attempts) != c.ExpectedAttempts || !d.UpdatedAt.Time.Equal(c.ExpectedUpdatedAt) || d.LeaseOwner.Valid || d.LeaseUntil.Valid || d.DeliveredAt.Valid {
		return nil, ports.ErrDeliveryCommandConflict
	}
	next, e := q.SkipDeadIntegrationDelivery(ctx, db.SkipDeadIntegrationDeliveryParams{ID: id, LeaseToken: c.ExpectedToken, Attempts: int32(c.ExpectedAttempts), UpdatedAt: pgtype.Timestamptz{Time: c.ExpectedUpdatedAt, Valid: true}, SkipReason: pgtype.Text{String: c.Reason, Valid: true}})
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, ports.ErrDeliveryCommandConflict
	}
	if e != nil {
		return nil, e
	}
	out = &ports.DeliverySkipReceipt{ID: id, EventID: next.EventID, State: next.State, LeaseToken: next.LeaseToken, Reason: c.Reason, SkippedAt: next.SkippedAt.Time}
	raw, _ := json.Marshal(out)
	if _, e = q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: s.ids.NewUUID(), ActorType: string(a.Type), ActorID: a.ID, Action: "integration.delivery_skipped", TargetType: "outbox_delivery", TargetID: id.String(), Payload: raw}); e != nil {
		return nil, e
	}
	if e = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "outbox_delivery", AggregateID: id.String()}); e != nil {
		return nil, e
	}
	seq, e := q.GetNextAggregateSeq(ctx, db.GetNextAggregateSeqParams{AggregateType: "outbox_delivery", AggregateID: id.String()})
	if e != nil {
		return nil, e
	}
	if _, e = q.InsertOutboxEvent(ctx, db.InsertOutboxEventParams{ID: s.ids.NewUUID(), AggregateType: "outbox_delivery", AggregateID: id.String(), EventType: "integration.delivery_skipped", Seq: int64(seq), Payload: raw}); e != nil {
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
