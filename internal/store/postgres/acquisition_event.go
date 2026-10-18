package postgres

import (
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
)

// Same transactional recipient snapshot and aggregate sequence as Outbox.PublishNotification.
func (s *AcquisitionStore) publishAcquisitionNotification(ctx context.Context, q *db.Queries, kind, id, event string, payload any) (*db.OutboxEvent, error) {
	channels, e := q.ListActiveNotificationChannels(ctx)
	if e != nil {
		return nil, e
	}
	if e = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: kind, AggregateID: id}); e != nil {
		return nil, e
	}
	seq, e := q.GetNextAggregateSeq(ctx, db.GetNextAggregateSeqParams{AggregateType: kind, AggregateID: id})
	if e != nil {
		return nil, e
	}
	raw, e := json.Marshal(payload)
	if e != nil {
		return nil, e
	}
	r, e := q.InsertOutboxEvent(ctx, db.InsertOutboxEventParams{ID: s.idgen.NewUUID(), AggregateType: kind, AggregateID: id, EventType: event, Payload: raw, Seq: int64(seq)})
	if e != nil {
		return nil, e
	}
	for _, channel := range channels {
		if _, e = q.InsertOutboxDelivery(ctx, db.InsertOutboxDeliveryParams{ID: s.idgen.NewUUID(), EventID: r.ID, Consumer: "notify:" + channel.ID.String(), AggregateType: kind, AggregateID: id, Seq: int64(seq), MaxAttempts: 5}); e != nil {
			return nil, e
		}
	}
	return &r, nil
}
