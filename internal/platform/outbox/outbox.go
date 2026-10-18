package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
)

var (
	ErrStaleDeliveryLease = ports.ErrStaleDeliveryLease
)

type Outbox struct {
	pool    *pgxpool.Pool
	idgen   ports.IDGen
	clock   ports.Clock
	queries *db.Queries
}

func New(pool *pgxpool.Pool, idgen ports.IDGen, clock ports.Clock) *Outbox {
	return &Outbox{
		pool:    pool,
		idgen:   idgen,
		clock:   clock,
		queries: postgres.Queries(pool),
	}
}

// Publish atomically assigns the next monotonic seq for the aggregate and inserts event + deliveries inside the calling transaction.
func (o *Outbox) Publish(ctx context.Context, qtx *db.Queries, aggregateType, aggregateID, eventType string, payload any, consumers []string) (*db.OutboxEvent, error) {
	if err := qtx.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: aggregateType, AggregateID: aggregateID}); err != nil {
		return nil, err
	}
	nextSeq, err := qtx.GetNextAggregateSeq(ctx, db.GetNextAggregateSeqParams{
		AggregateType: aggregateType,
		AggregateID:   aggregateID,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get next aggregate seq: %w", err)
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal event payload: %w", err)
	}

	eventID := o.idgen.NewUUID()
	evt, err := qtx.InsertOutboxEvent(ctx, db.InsertOutboxEventParams{
		ID:            eventID,
		AggregateType: aggregateType,
		AggregateID:   aggregateID,
		EventType:     eventType,
		Payload:       payloadBytes,
		Seq:           int64(nextSeq),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to insert outbox event: %w", err)
	}

	for _, consumer := range consumers {
		deliveryID := o.idgen.NewUUID()
		_, err := qtx.InsertOutboxDelivery(ctx, db.InsertOutboxDeliveryParams{
			ID:            deliveryID,
			EventID:       eventID,
			Consumer:      consumer,
			AggregateType: aggregateType,
			AggregateID:   aggregateID,
			Seq:           int64(nextSeq),
			MaxAttempts:   5,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to insert outbox delivery for consumer %s: %w", consumer, err)
		}
	}

	return &evt, nil
}

// Claim claims the next ordered pending delivery for a consumer, strictly ensuring no uncompleted predecessors exist.
func (o *Outbox) Claim(ctx context.Context, consumer, owner string, leaseDuration time.Duration) (*db.OutboxDelivery, error) {
	leaseMs := int32(leaseDuration.Milliseconds())
	if leaseMs <= 0 {
		leaseMs = 30000
	}

	delivery, err := o.queries.ClaimOutboxDelivery(ctx, db.ClaimOutboxDeliveryParams{
		Column1: consumer,
		Column2: owner,
		Column3: leaseMs,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil // No deliveries ready or preceding deliveries still in-flight
		}
		return nil, fmt.Errorf("failed to claim outbox delivery: %w", err)
	}
	return &delivery, nil
}

// Finish marks an outbox delivery as successfully delivered.
func (o *Outbox) Finish(ctx context.Context, deliveryID uuid.UUID, owner string, leaseToken int64) error {
	tx, err := o.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := o.queries.WithTx(tx)
	if _, err := q.LockOutboxDelivery(ctx, deliveryID); err != nil {
		return ErrStaleDeliveryLease
	}
	_, err = q.FinishOutboxDelivery(ctx, db.FinishOutboxDeliveryParams{
		Column1: deliveryID,
		Column2: owner,
		Column3: leaseToken,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrStaleDeliveryLease
		}
		return fmt.Errorf("failed to finish outbox delivery: %w", err)
	}
	return tx.Commit(ctx)
}

// Fail records delivery failure, scheduling retry or marking dead.
func (o *Outbox) Fail(ctx context.Context, deliveryID uuid.UUID, owner string, leaseToken int64, errMsg string, retryAfter time.Duration) error {
	retryMs := int32(retryAfter.Milliseconds())
	if retryMs <= 0 {
		retryMs = 5000
	}

	tx, err := o.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := o.queries.WithTx(tx)
	if _, err := q.LockOutboxDelivery(ctx, deliveryID); err != nil {
		return ErrStaleDeliveryLease
	}
	_, err = q.FailOutboxDelivery(ctx, db.FailOutboxDeliveryParams{
		Column1: deliveryID,
		Column2: owner,
		Column3: leaseToken,
		Column4: errMsg,
		Column5: retryMs,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrStaleDeliveryLease
		}
		return fmt.Errorf("failed to fail outbox delivery: %w", err)
	}
	return tx.Commit(ctx)
}
