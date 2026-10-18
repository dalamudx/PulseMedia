package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type UpgradePreparationStore struct {
	pool              *pgxpool.Pool
	ids               ports.IDGen
	verifier          ports.MovieUpgradeMetainfoVerifier
	subscriptionScope uuid.UUID // Optional public route scope; zero preserves internal callers.
}

func NewUpgradePreparationStore(
	pool *pgxpool.Pool,
	ids ports.IDGen,
	verifier ports.MovieUpgradeMetainfoVerifier,
) (*UpgradePreparationStore, error) {
	if pool == nil || ids == nil || verifier == nil {
		return nil, errors.New("missing upgrade preparation dependency")
	}
	return &UpgradePreparationStore{
		pool: pool, ids: ids, verifier: verifier,
	}, nil
}

var _ ports.MovieUpgradePreparationRepository = (*UpgradePreparationStore)(nil)

func preparationConflict() error {
	return ports.ErrUpgradeReservationConflict
}

// Permission checks also run for historical receipt replay.
func (s *UpgradePreparationStore) authorizePreparation(
	ctx context.Context, tx pgx.Tx, supplied *ports.Actor, operation uuid.UUID,
) (*ports.Actor, uuid.UUID, error) {
	a := supplied
	if err := episodeReviewActor(a); err != nil {
		return nil, uuid.Nil, err
	}
	if err := lockAcquisitionUser(ctx, db.New(tx), a); err != nil {
		return nil, uuid.Nil, err
	}
	actorID, err := uuid.Parse(a.ID)
	if err != nil {
		return nil, uuid.Nil, ports.ErrUnauthorized
	}
	var subID, ownerID uuid.UUID
	err = tx.QueryRow(ctx, `
SELECT subscription_id,actor_id
FROM movie_upgrade_authority WHERE operation_id=$1`,
		operation).Scan(&subID, &ownerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, uuid.Nil, ports.ErrNotFound
	}
	if err != nil {
		return nil, uuid.Nil, err
	}
	if _, _, err = lockUpgradeCommand(ctx, tx, a, subID); err != nil {
		return nil, uuid.Nil, err
	}
	if s.subscriptionScope != uuid.Nil && s.subscriptionScope != subID {
		return nil, uuid.Nil, ports.ErrNotFound
	}
	if ownerID != actorID {
		return nil, uuid.Nil, ports.ErrForbidden
	}
	return a, subID, nil
}

func (s *UpgradePreparationStore) ClaimMovieUpgradePreparation(
	ctx context.Context, supplied *ports.Actor,
	c ports.ClaimMovieUpgradePreparation,
) (out *ports.MovieUpgradePreparationLease, err error) {
	if c.OperationID == uuid.Nil || c.Owner == uuid.Nil {
		return nil, preparationConflict()
	}
	defer func() { err = upgradeError(ctx, err) }()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer upgradeRollback(ctx, tx)

	a, _, err := s.authorizePreparation(ctx, tx, supplied, c.OperationID)
	if err != nil {
		return nil, err
	}
	b, err := s.loadPreparationBasis(ctx, tx, a, c.OperationID)
	if err != nil {
		return nil, err
	}
	if err = b.check(ctx, tx); err != nil {
		return nil, err
	}

	token := s.ids.NewUUID()
	if token == uuid.Nil {
		return nil, preparationConflict()
	}
	var row json.RawMessage
	var valid bool
	err = tx.QueryRow(ctx, `
WITH t AS (SELECT clock_timestamp() AS now),
written AS (
 INSERT INTO movie_upgrade_preparation_lease AS l
  (operation_id,actor_id,owner,token,state,claimed_at,lease_until)
 SELECT $1,$2,$3,$4,'leased',t.now,t.now+interval '60 seconds' FROM t
 ON CONFLICT(operation_id) DO UPDATE SET
  owner=EXCLUDED.owner,token=EXCLUDED.token,state='leased',
  claimed_at=EXCLUDED.claimed_at,lease_until=EXCLUDED.lease_until
 WHERE l.state='leased' AND l.lease_until<=EXCLUDED.claimed_at
 RETURNING *
)
SELECT to_jsonb(w),w.operation_id=$1 AND w.actor_id=$2 AND w.owner=$3
 AND w.token=$4 AND w.state='leased' AND w.claimed_at=t.now
 AND w.lease_until=t.now+interval '60 seconds' FROM written w CROSS JOIN t`,
		c.OperationID, uuid.MustParse(a.ID), c.Owner, token).Scan(&row, &valid)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, preparationConflict()
	}
	if err != nil {
		return nil, err
	}
	lease, err := decodePreparationLease(row)
	if err != nil {
		return nil, err
	}
	if !valid || lease.OperationID != c.OperationID || lease.ActorID != uuid.MustParse(a.ID) ||
		lease.Owner != c.Owner || lease.Token != token || lease.State != "leased" ||
		!lease.LeaseUntil.Equal(lease.ClaimedAt.Add(preparationLeaseDuration)) {
		return nil, preparationConflict()
	}
	b.rows["movie_upgrade_preparation_lease"] = row
	if err = b.check(ctx, tx); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE"); err != nil {
		return nil, err
	}
	if err = b.check(ctx, tx); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &ports.MovieUpgradePreparationLease{
		OperationID: c.OperationID, Owner: c.Owner, Token: token,
		LeaseUntil: lease.LeaseUntil.Format("2006-01-02T15:04:05.999999999Z07:00"),
	}, nil
}
