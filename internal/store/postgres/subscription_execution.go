package postgres

import (
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/domain/subscription"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type SubscriptionExecutionStore struct{ pool *pgxpool.Pool }

func NewSubscriptionExecutionStore(pool *pgxpool.Pool) *SubscriptionExecutionStore {
	return &SubscriptionExecutionStore{pool: pool}
}

var _ ports.SubscriptionExecutionRepository = (*SubscriptionExecutionStore)(nil)

func (s *SubscriptionExecutionStore) ReadSubscriptionExecution(ctx context.Context, a *ports.Actor, id uuid.UUID) (out *ports.SubscriptionExecution, err error) {
	defer func() { err = subscriptionIntentError(ctx, err) }()
	tx, e := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if e != nil {
		return nil, e
	}
	defer creationRollback(ctx, tx)
	actor := creationClone(a)
	q := db.New(tx)
	if e = lockSubscriptionReadAccount(ctx, q, actor); e != nil {
		return nil, e
	}
	snap, e := readSubscriptionStatus(ctx, q, actor, id, nil)
	if e != nil {
		return nil, e
	}
	status := subscription.DeriveStatus(snap)
	out = &ports.SubscriptionExecution{SubscriptionID: id, ObservedAt: snap.ObservedAt, Rounds: []ports.SubscriptionExecutionRound{}}
	if snap.State != "active" {
		out.Waiting = "subscription_inactive"
		out.NextStep = "check_subscription_state"
	} else if len(status.Blockers) > 0 {
		out.Waiting = "prerequisites"
		out.NextStep = "check_wanted_status"
	}
	raw, e := q.ReadSubscriptionExecutionFacts(ctx, id)
	if e != nil {
		return nil, e
	}
	if json.Unmarshal(raw, &out.Rounds) != nil {
		return nil, ports.ErrUnavailable
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return out, nil
}
