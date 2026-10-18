package postgres

import (
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
)

// Preserve the legacy notification recipient snapshot, sequence and delivery format
// inside the caller's subscription transaction.
func (t *subscriptionIntentTx) publish(ctx context.Context, id uuid.UUID, event string, payload any) error {
	channels, e := t.q.ListActiveNotificationChannels(ctx)
	if e != nil {
		return e
	}
	key := id.String()
	if e = t.q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "subscription", AggregateID: key}); e != nil {
		return e
	}
	seq, e := t.q.GetNextAggregateSeq(ctx, db.GetNextAggregateSeqParams{AggregateType: "subscription", AggregateID: key})
	if e != nil {
		return e
	}
	raw, e := json.Marshal(payload)
	if e != nil {
		return e
	}
	row, e := t.q.InsertOutboxEvent(ctx, db.InsertOutboxEventParams{ID: t.ids.NewUUID(), AggregateType: "subscription", AggregateID: key, EventType: event, Payload: raw, Seq: int64(seq)})
	if e != nil {
		return e
	}
	for _, channel := range channels {
		if _, e = t.q.InsertOutboxDelivery(ctx, db.InsertOutboxDeliveryParams{ID: t.ids.NewUUID(), EventID: row.ID, Consumer: "notify:" + channel.ID.String(), AggregateType: "subscription", AggregateID: key, Seq: int64(seq), MaxAttempts: 5}); e != nil {
			return e
		}
	}
	return nil
}
