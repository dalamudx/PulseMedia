package outbox

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// ReturnDisabledNotification is only for an owned delivery before Send starts.
// Disabling a channel never cancels or releases a physical Send already running.
func (o *Outbox) ReturnDisabledNotification(ctx context.Context, id uuid.UUID, consumer, owner string, token int64) error {
	tx, e := o.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := db.New(tx)
	if _, e = q.LockOutboxDelivery(ctx, id); e != nil {
		if errors.Is(e, pgx.ErrNoRows) {
			return ErrStaleDeliveryLease
		}
		return e
	}
	if _, e = q.ReturnDisabledNotificationDelivery(ctx, db.ReturnDisabledNotificationDeliveryParams{ID: id, Consumer: consumer, Owner: pgtype.Text{String: owner, Valid: true}, Token: token}); e != nil {
		if errors.Is(e, pgx.ErrNoRows) {
			return ErrStaleDeliveryLease
		}
		return e
	}
	return tx.Commit(ctx)
}
