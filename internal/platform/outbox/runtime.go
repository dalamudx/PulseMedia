package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"sort"
)

// PublishImported snapshots independent recipients inside the importing UoW.
// Later configuration changes must not silently retarget historical events.
func (o *Outbox) PublishImported(ctx context.Context, q *db.Queries, unit string, payload any) (*db.OutboxEvent, error) {
	servers, err := q.ListMediaServers(ctx)
	if err != nil {
		return nil, err
	}
	channels, err := q.ListActiveNotificationChannels(ctx)
	if err != nil {
		return nil, err
	}
	consumers := make([]string, 0, len(servers))
	for _, s := range servers {
		consumers = append(consumers, "mediaserver.refresh:"+s.ID.String())
	}
	event, err := o.Publish(ctx, q, "media", unit, "media.imported", payload, consumers)
	if err != nil {
		return nil, err
	}
	var ids struct {
		FileID uuid.UUID `json:"file_id"`
		UnitID uuid.UUID `json:"unit_id"`
	}
	if json.Unmarshal(event.Payload, &ids) != nil || ids.UnitID == uuid.Nil || ids.UnitID.String() != unit || ids.FileID == uuid.Nil {
		return nil, errors.New("invalid imported metadata source")
	}
	// Metadata is a separate system continuation, even without external recipients.
	// Keep its enqueue in the importing transaction; network/filesystem work is later.
	if _, err = q.EnqueueMediaSidecarJob(ctx, db.EnqueueMediaSidecarJobParams{
		JobID: o.idgen.NewUUID(), FileID: ids.FileID, UnitID: ids.UnitID, SourceEventID: event.ID,
	}); err != nil {
		return nil, err
	}
	if _, err = q.LockMediaSubtitleEnqueue(ctx, ids.FileID); err != nil {
		return nil, err
	}
	if err = q.EnqueueMediaSubtitleJob(ctx, db.EnqueueMediaSubtitleJobParams{
		JobID: o.idgen.NewUUID(), FileID: ids.FileID, UnitID: ids.UnitID, SourceEventID: event.ID,
	}); err != nil {
		return nil, err
	}
	if o.webPushVersion != "" {
		if err = o.publishImportedWebPush(ctx, q, event, ids.FileID, ids.UnitID); err != nil {
			return nil, err
		}
	}
	if len(channels) == 0 {
		return event, nil
	}
	input, err := q.GetImportedNotificationInput(ctx, db.GetImportedNotificationInputParams{FileID: ids.FileID, UnitID: ids.UnitID})
	if err != nil {
		return nil, err
	}
	sort.Slice(channels, func(i, j int) bool { return channels[i].ID.String() < channels[j].ID.String() })
	for _, c := range channels {
		lock, _ := json.Marshal([]uuid.UUID{input.MediaID, c.ID})
		if err = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "notification.batch.membership", AggregateID: string(lock)}); err != nil {
			return nil, err
		}
		batch, e := q.LockOpenNotificationBatch(ctx, db.LockOpenNotificationBatchParams{MediaID: input.MediaID, ChannelID: c.ID})
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return nil, e
		}
		if e == nil {
			expired, x := q.NotificationBatchExpired(ctx, batch.ID)
			if x != nil {
				return nil, x
			}
			if expired {
				if x = q.ReadyNotificationBatch(ctx, batch.ID); x != nil {
					return nil, x
				}
				e = pgx.ErrNoRows
			}
		}
		if errors.Is(e, pgx.ErrNoRows) {
			batch, e = q.CreateNotificationBatch(ctx, db.CreateNotificationBatchParams{ID: o.idgen.NewUUID(), MediaID: input.MediaID, ChannelID: c.ID})
			if e != nil {
				return nil, e
			}
		}
		if err = q.AppendNotificationMember(ctx, db.AppendNotificationMemberParams{BatchID: batch.ID, ChannelID: c.ID, SourceEventID: event.ID, UnitID: input.UnitID, RootID: input.RootID, FileID: input.FileID}); err != nil {
			return nil, err
		}
		if err = q.AdvanceNotificationBatch(ctx, batch.ID); err != nil {
			return nil, err
		}
	}
	return event, nil
}

// PublishNotification records only actual configured notification recipients.
// Internal download work already has a job; scheduler rebuilds from wanted state.
func (o *Outbox) PublishNotification(ctx context.Context, q *db.Queries, kind, id, event string, payload any) (*db.OutboxEvent, error) {
	channels, err := q.ListActiveNotificationChannels(ctx)
	if err != nil {
		return nil, err
	}
	consumers := make([]string, 0, len(channels))
	for _, c := range channels {
		consumers = append(consumers, "notify:"+c.ID.String())
	}
	return o.Publish(ctx, q, kind, id, event, payload, consumers)
}

// LoadOwned validates before external I/O. Row locking precedes the DB-clock check;
// waiting for a lock must not resurrect a lease. No transaction spans the send.
func (o *Outbox) LoadOwned(ctx context.Context, id uuid.UUID, consumer, owner string, token int64) (*db.OutboxDelivery, *db.OutboxEvent, error) {
	tx, err := o.pool.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := o.queries.WithTx(tx)
	if _, err = q.LockOutboxDelivery(ctx, id); err != nil {
		return nil, nil, ErrStaleDeliveryLease
	}
	d, err := q.GuardOutboxDelivery(ctx, db.GuardOutboxDeliveryParams{ID: id, Consumer: consumer, Owner: pgtype.Text{String: owner, Valid: true}, Token: token})
	if err != nil {
		return nil, nil, ErrStaleDeliveryLease
	}
	e, err := q.GetOutboxEventByID(ctx, d.EventID)
	if err != nil {
		return nil, nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, nil, err
	}
	return &d, &e, nil
}
