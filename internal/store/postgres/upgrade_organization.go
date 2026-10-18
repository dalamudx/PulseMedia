package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const upgradeOrganizationAction = "upgrade.organization-authorize"
const upgradeOrganizationStatus = "organization_authorized_not_executable"

type movieUpgradeOrganizationRepository struct{ store *UpgradePreparationStore }

func NewMovieUpgradeOrganizationRepository(s *UpgradePreparationStore) (ports.MovieUpgradeOrganizationRepository, error) {
	if s == nil {
		return nil, ports.ErrUnavailable
	}
	return &movieUpgradeOrganizationRepository{s}, nil
}

var _ ports.MovieUpgradeOrganizationRepository = (*movieUpgradeOrganizationRepository)(nil)

func (r *movieUpgradeOrganizationRepository) ReadMovieUpgradeOrganization(ctx context.Context, a *ports.Actor, op uuid.UUID) (out *ports.MovieUpgradeOrganizationReview, err error) {
	if err = episodeReviewActor(a); err != nil {
		return nil, err
	}
	if op == uuid.Nil {
		return nil, preparationConflict()
	}
	defer func() { err = upgradeError(ctx, err) }()
	tx, e := r.store.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer upgradeRollback(ctx, tx)
	a, _, e = r.store.authorizePreparation(ctx, tx, a, op)
	if e != nil {
		return nil, e
	}
	b, e := r.store.organizationBasis(ctx, tx, a, op, uuid.Nil)
	if e != nil {
		return nil, e
	}
	if e = b.verify(); e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return &b.review, nil
}
func (r *movieUpgradeOrganizationRepository) AuthorizeMovieUpgradeOrganization(ctx context.Context, a *ports.Actor, c ports.AuthorizeMovieUpgradeOrganization, key string) (out *ports.MovieUpgradeOrganizationAuthorization, err error) {
	if err = episodeReviewActor(a); err != nil {
		return nil, err
	}
	if !c.Confirmed || c.OperationID == uuid.Nil || c.ObservationID == uuid.Nil || !completeTVStamp(c.InputStamp) || !upgradeKey(key) {
		return nil, preparationConflict()
	}
	defer func() { err = upgradeError(ctx, err) }()
	tx, e := r.store.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer upgradeRollback(ctx, tx)
	a, sub, e := r.store.authorizePreparation(ctx, tx, a, c.OperationID)
	if e != nil {
		return nil, e
	}
	key = upgradeOrganizationAction + ":" + key
	if e = db.New(tx).LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "upgrade.command", AggregateID: a.ID + ":" + key}); e != nil {
		return nil, e
	}
	if out, e = r.recoverOrganization(ctx, tx, a, sub, c.OperationID, key, &c); e != nil {
		return nil, e
	} else if out != nil {
		if e = tx.Commit(ctx); e != nil {
			return nil, e
		}
		return out, nil
	}
	b, e := r.store.organizationBasis(ctx, tx, a, c.OperationID, c.ObservationID)
	if e != nil {
		return nil, e
	}
	var exists bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM movie_upgrade_organization_authorization WHERE operation_id=$1)`, c.OperationID).Scan(&exists); e != nil {
		return nil, e
	}
	if exists {
		return nil, preparationConflict()
	}
	if b.review.InputStamp != c.InputStamp {
		return nil, preparationConflict()
	}
	fields, e := json.Marshal(map[string]any{"id": r.store.ids.NewUUID(), "operation_id": c.OperationID, "observation_id": c.ObservationID,
		"task_id": b.review.TaskID, "actor_id": a.ID, "subscription_id": sub, "root_id": b.review.RootID, "unit_id": b.review.UnitID,
		"old_file_id": b.review.CurrentFileID, "old_fact_id": b.review.CurrentFactID, "request_key": key, "snapshot": b.raw,
		"confirmed": true, "status": upgradeOrganizationStatus, "execution_allowed": false})
	if e != nil {
		return nil, e
	}
	var expected, actual json.RawMessage
	if e = tx.QueryRow(ctx, `SELECT $1::jsonb||jsonb_build_object('authorized_at',clock_timestamp(),'input_sha256',sha256(convert_to(($1::jsonb->'snapshot')::text,'UTF8')))`, fields).Scan(&expected); e != nil {
		return nil, e
	}
	if e = tx.QueryRow(ctx, `INSERT INTO movie_upgrade_organization_authorization SELECT * FROM jsonb_populate_record(NULL::movie_upgrade_organization_authorization,$1::jsonb) RETURNING to_jsonb(movie_upgrade_organization_authorization)`, expected).Scan(&actual); e != nil {
		return nil, e
	}
	if !preparationSameJSON(expected, actual) {
		return nil, preparationConflict()
	}
	out = &ports.MovieUpgradeOrganizationAuthorization{}
	if e = tx.QueryRow(ctx, `SELECT id,operation_id,observation_id,task_id,status,authorized_at,execution_allowed FROM movie_upgrade_organization_authorization WHERE operation_id=$1`, c.OperationID).Scan(&out.ID, &out.OperationID, &out.ObservationID, &out.TaskID, &out.Status, &out.AuthorizedAt, &out.ExecutionAllowed); e != nil {
		return nil, e
	}
	own := ownedUpgradeRows{}
	if e = own.remember(ctx, tx, "movie_upgrade_organization_authorization", out.ID); e != nil {
		return nil, e
	}
	check := func() error {
		if e := b.verify(); e != nil {
			return e
		}
		return own.verify(ctx, tx)
	}
	hash := coverageDigest([]any{"upgrade.organization-authorize.v1", c})
	ledger := UpgradeReservationStore{pool: r.store.pool, ids: r.store.ids}
	if e = ledger.upgradeSuccess(ctx, tx, a, sub, key, hash, upgradeOrganizationAction, out.ID, out, own, check); e != nil {
		return nil, e
	}
	if e = check(); e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return out, nil
}
func (r *movieUpgradeOrganizationRepository) ReadMovieUpgradeOrganizationReceipt(ctx context.Context, a *ports.Actor, op uuid.UUID, key string) (out *ports.MovieUpgradeOrganizationAuthorization, err error) {
	if err = episodeReviewActor(a); err != nil {
		return nil, err
	}
	if op == uuid.Nil || !upgradeKey(key) {
		return nil, preparationConflict()
	}
	defer func() { err = upgradeError(ctx, err) }()
	tx, e := r.store.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer upgradeRollback(ctx, tx)
	a, sub, e := r.store.authorizePreparation(ctx, tx, a, op)
	if e != nil {
		return nil, e
	}
	out, e = r.recoverOrganization(ctx, tx, a, sub, op, upgradeOrganizationAction+":"+key, nil)
	if e != nil {
		return nil, e
	}
	if out == nil {
		return nil, ports.ErrNotFound
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return out, nil
}
func (r *movieUpgradeOrganizationRepository) recoverOrganization(ctx context.Context, tx pgx.Tx, a *ports.Actor, sub, op uuid.UUID, key string, supplied *ports.AuthorizeMovieUpgradeOrganization) (*ports.MovieUpgradeOrganizationAuthorization, error) {
	var expected ports.MovieUpgradeOrganizationAuthorization
	var stamp string
	e := tx.QueryRow(ctx, `SELECT id,operation_id,observation_id,task_id,status,authorized_at,execution_allowed,encode(input_sha256,'hex') FROM movie_upgrade_organization_authorization WHERE operation_id=$1 AND request_key=$2 AND actor_id=$3`, op, key, a.ID).Scan(&expected.ID, &expected.OperationID, &expected.ObservationID, &expected.TaskID, &expected.Status, &expected.AuthorizedAt, &expected.ExecutionAllowed, &stamp)
	if errors.Is(e, pgx.ErrNoRows) {
		var found bool
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM idempotency_record WHERE actor_type=$1 AND actor_id=$2 AND idempotency_key=$3)`, string(a.Type), a.ID, key).Scan(&found); e != nil {
			return nil, e
		}
		if found {
			return nil, preparationConflict()
		}
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	original := ports.AuthorizeMovieUpgradeOrganization{OperationID: op, ObservationID: expected.ObservationID, InputStamp: stamp, Confirmed: true}
	if supplied != nil && *supplied != original {
		return nil, preparationConflict()
	}
	if expected.ID == uuid.Nil || expected.TaskID == uuid.Nil || expected.Status != upgradeOrganizationStatus || expected.ExecutionAllowed {
		return nil, preparationConflict()
	}
	hash := coverageDigest([]any{"upgrade.organization-authorize.v1", original})
	var out ports.MovieUpgradeOrganizationAuthorization
	found, e := upgradeReceipt(ctx, db.New(tx), a, key, hash, &out)
	if e != nil {
		return nil, e
	}
	if !found {
		return nil, preparationConflict()
	}
	body, e := json.Marshal(out)
	if e != nil {
		return nil, e
	}
	want, e := json.Marshal(expected)
	if e != nil {
		return nil, e
	}
	if !preparationSameJSON(body, want) {
		return nil, preparationConflict()
	}
	if e = validateOrganizationLedger(ctx, tx, a, sub, key, hash, upgradeOrganizationAction, out.ID, body); e != nil {
		return nil, e
	}
	return &out, nil
}
