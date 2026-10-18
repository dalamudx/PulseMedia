package outbox

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// A failing batch is retried on the next bounded pass, not allowed to starve other media/recipients.
func (o *Outbox) SealDueNotificationBatches(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 32 {
		return 0, errors.New("invalid notification seal limit")
	}
	count := 0
	excluded := []uuid.UUID{}
	var failure error
	for attempt := 0; attempt < limit; attempt++ {
		id, sealed, e := o.sealNotificationBatch(ctx, excluded)
		if e != nil {
			if ctx.Err() != nil {
				return count, ctx.Err()
			}
			failure = errors.New("notification batch sealing incomplete")
			if id == uuid.Nil {
				return count, failure
			}
			excluded = append(excluded, id)
			continue
		}
		if !sealed {
			return count, failure
		}
		count++
	}
	return count, failure
}
func (o *Outbox) sealNotificationBatch(ctx context.Context, excluded []uuid.UUID) (id uuid.UUID, sealed bool, err error) {
	tx, e := o.pool.Begin(ctx)
	if e != nil {
		return id, false, e
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := db.New(tx)
	batch, e := q.LockDueNotificationBatch(ctx, excluded)
	if errors.Is(e, pgx.ErrNoRows) {
		return id, false, nil
	}
	if e != nil {
		return id, false, e
	}
	id = batch.ID
	members, e := q.ListNotificationBatchMembers(ctx, batch.ID)
	if e != nil {
		return id, false, e
	}
	if len(members) < 1 || len(members) != int(batch.MemberCount) || len(members) > 32 {
		return id, false, errors.New("notification batch evidence mismatch")
	}
	payload := ports.NotificationBatchPayload{Version: "notification.media.v1", MediaID: batch.MediaID, Members: []ports.NotificationBatchMember{}}
	for _, m := range members {
		payload.Members = append(payload.Members, ports.NotificationBatchMember{SourceEventID: m.SourceEventID, UnitID: m.UnitID, RootID: m.RootID, FileID: m.FileID})
	}
	event, e := o.Publish(ctx, q, "notification.media", batch.MediaID.String(), "notification.media_imported.v1", payload, []string{"notify:" + batch.ChannelID.String()})
	if e != nil {
		return id, false, e
	}
	if e = q.SealNotificationBatch(ctx, db.SealNotificationBatchParams{ID: batch.ID, EventID: pgtype.UUID{Bytes: event.ID, Valid: true}}); e != nil {
		return id, false, e
	}
	if e = tx.Commit(ctx); e != nil {
		return id, false, e
	}
	return id, true, nil
}
