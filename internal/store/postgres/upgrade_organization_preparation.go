package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"path/filepath"
	"time"
)

type movieUpgradeOrganizationPreparationRepository struct {
	store *UpgradePreparationStore
	files ports.RootFilesystem
}

func NewMovieUpgradeOrganizationPreparationRepository(s *UpgradePreparationStore, f ports.RootFilesystem) (ports.MovieUpgradeOrganizationPreparationRepository, error) {
	if s == nil || f == nil {
		return nil, errors.New("upgrade organization preparation requires store and filesystem")
	}
	return &movieUpgradeOrganizationPreparationRepository{store: s, files: f}, nil
}

const organizationPrepareAction = "upgrade.organization-prepare"

func (r *movieUpgradeOrganizationPreparationRepository) ReadMovieUpgradeOrganizationPreparationReceipt(ctx context.Context, a *ports.Actor, op uuid.UUID, key string) (out *ports.MovieUpgradeOrganizationPreparation, err error) {
	if e := episodeReviewActor(a); e != nil {
		return nil, e
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
	out, e = r.recover(ctx, tx, a, sub, op, organizationPrepareAction+":"+key, nil)
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
func (r *movieUpgradeOrganizationPreparationRepository) PrepareMovieUpgradeOrganization(ctx context.Context, a *ports.Actor, c ports.PrepareMovieUpgradeOrganization, key string) (out *ports.MovieUpgradeOrganizationPreparation, err error) {
	if e := episodeReviewActor(a); e != nil {
		return nil, e
	}
	if c.OperationID == uuid.Nil || c.AuthorizationID == uuid.Nil || !c.Confirmed || !upgradeKey(key) {
		return nil, preparationConflict()
	}
	defer func() { err = upgradeError(ctx, err) }()
	full := organizationPrepareAction + ":" + key
	tx, e := r.store.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer upgradeRollback(ctx, tx)
	a, sub, e := r.store.authorizePreparation(ctx, tx, a, c.OperationID)
	if e != nil {
		return nil, e
	}
	old, e := r.recover(ctx, tx, a, sub, c.OperationID, full, &c)
	if e != nil {
		return nil, e
	}
	if old != nil {
		if e = tx.Commit(ctx); e != nil {
			return nil, e
		}
		return old, nil
	}
	in, e := r.inputs(ctx, tx, a, c)
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	observed, release, e := r.observe(ctx, in)
	if e != nil {
		return nil, e
	}
	defer release()
	tx, e = r.store.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer upgradeRollback(ctx, tx)
	a, sub, e = r.store.authorizePreparation(ctx, tx, a, c.OperationID)
	if e != nil {
		return nil, e
	}
	old, e = r.recover(ctx, tx, a, sub, c.OperationID, full, &c)
	if e != nil {
		return nil, e
	}
	if old != nil {
		if e = tx.Commit(ctx); e != nil {
			return nil, e
		}
		return old, nil
	}
	current, e := r.inputs(ctx, tx, a, c)
	if e != nil {
		return nil, e
	}
	expectedPaths := upgradeOrganizationPhysical{RootPath: in.physical.RootPath, SourceRoot: in.physical.SourceRoot, SourceRel: in.physical.SourceRel, TargetRel: in.physical.TargetRel, OldRel: in.physical.OldRel, NamingVersion: in.physical.NamingVersion}
	if !preparationSameJSON(current.authorization, in.authorization) || !preparationSameJSON(current.basis.raw, in.basis.raw) || coverageDigest(current.physical) != coverageDigest(expectedPaths) {
		return nil, preparationConflict()
	}
	out, e = r.insert(ctx, tx, a, c, full, sub, current, in.physical, observed)
	if e != nil {
		return nil, e
	}
	if e = current.basis.verify(); e != nil {
		return nil, e
	}
	if _, e = r.recover(ctx, tx, a, sub, c.OperationID, full, &c); e != nil {
		return nil, e
	}
	var valid bool
	if e = tx.QueryRow(ctx, `SELECT clock_timestamp()<$1::timestamptz+interval '30 seconds'`, observed).Scan(&valid); e != nil {
		return nil, e
	}
	if !valid {
		return nil, preparationConflict()
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return out, nil
}
func (r *movieUpgradeOrganizationPreparationRepository) insert(ctx context.Context, tx pgx.Tx, a *ports.Actor, c ports.PrepareMovieUpgradeOrganization, key string, sub uuid.UUID, in *upgradeOrganizationPlanInputs, p upgradeOrganizationPhysical, observed time.Time) (*ports.MovieUpgradeOrganizationPreparation, error) {
	id, planID, stepID := r.store.ids.NewUUID(), r.store.ids.NewUUID(), r.store.ids.NewUUID()
	planFields := map[string]any{"id": planID, "operation_id": c.OperationID, "root_id": in.basis.review.RootID, "state": "review_required", "requires_approval": true, "strategy": "hardlink", "execution_token": 0, "root_path": p.RootPath, "root_generation": in.generation, "root_device": p.Root.Device, "root_inode": p.Root.Inode, "old_file_id": in.basis.review.CurrentFileID}
	plan, e := organizationPlanRow(ctx, tx, "transfer_plan", planFields)
	if e != nil {
		return nil, e
	}
	evidence := map[string]any{"kind": "movie_upgrade_organization_prepared_v1", "preparation_id": id, "authorization_id": c.AuthorizationID, "input_stamp": in.basis.review.InputStamp}
	stepFields := map[string]any{"id": stepID, "plan_id": planID, "unit_id": in.basis.review.UnitID, "source_path": filepath.Join(p.SourceRoot, p.SourceRel), "target_path": filepath.Join(p.RootPath, p.TargetRel), "action": "hardlink", "state": "pending", "step_order": 1, "source_size": p.Source.Size, "source_mtime": p.Source.Mtime, "source_root": p.SourceRoot, "source_rel": p.SourceRel, "target_rel": p.TargetRel, "source_device": p.Source.Device, "source_inode": p.Source.Inode, "quality_profile": in.profile, "target_evidence": evidence}
	step, e := organizationPlanRow(ctx, tx, "transfer_step", stepFields)
	if e != nil {
		return nil, e
	}
	snapshot := map[string]any{"authorization": in.authorization, "basis": in.basis.raw, "physical": p}
	fields, _ := json.Marshal(map[string]any{"id": id, "operation_id": c.OperationID, "authorization_id": c.AuthorizationID, "task_id": in.basis.review.TaskID, "actor_id": a.ID, "plan_id": planID, "step_id": stepID, "request_key": key, "snapshot": snapshot, "plan_snapshot": plan, "step_snapshot": step, "observed_at": observed, "status": "organization_prepared_not_executable", "execution_allowed": false})
	var expected, actual json.RawMessage
	if e = tx.QueryRow(ctx, `SELECT to_jsonb(jsonb_populate_record(NULL::movie_upgrade_organization_preparation,$1::jsonb||jsonb_build_object('prepared_at',clock_timestamp())))`, fields).Scan(&expected); e != nil {
		return nil, e
	}
	if e = tx.QueryRow(ctx, `INSERT INTO movie_upgrade_organization_preparation SELECT * FROM jsonb_populate_record(NULL::movie_upgrade_organization_preparation,$1::jsonb) RETURNING to_jsonb(movie_upgrade_organization_preparation)`, expected).Scan(&actual); e != nil {
		return nil, e
	}
	if !preparationSameJSON(expected, actual) {
		return nil, preparationConflict()
	}
	out := &ports.MovieUpgradeOrganizationPreparation{ID: id, OperationID: c.OperationID, AuthorizationID: c.AuthorizationID, TaskID: in.basis.review.TaskID, PlanID: planID, StepID: stepID, Status: "organization_prepared_not_executable"}
	if e = tx.QueryRow(ctx, `SELECT prepared_at FROM movie_upgrade_organization_preparation WHERE id=$1`, id).Scan(&out.PreparedAt); e != nil {
		return nil, e
	}
	own := ownedUpgradeRows{}
	for table, rowID := range map[string]uuid.UUID{"movie_upgrade_organization_preparation": id, "transfer_plan": planID, "transfer_step": stepID} {
		if e = own.remember(ctx, tx, table, rowID); e != nil {
			return nil, e
		}
	}
	check := func() error {
		if e := in.basis.verify(); e != nil {
			return e
		}
		return own.verify(ctx, tx)
	}
	hash := coverageDigest([]any{"upgrade.organization-prepare.v1", c})
	ledger := UpgradeReservationStore{pool: r.store.pool, ids: r.store.ids}
	if e = ledger.upgradeSuccess(ctx, tx, a, sub, key, hash, organizationPrepareAction, id, out, own, check); e != nil {
		return nil, e
	}
	if e = check(); e != nil {
		return nil, e
	}
	return out, nil
}

// Closed table names only. Compare the full expected row before recording its
// baseline. PostgreSQL supplies timestamps and canonical typed JSON values.
func organizationPlanRow(ctx context.Context, tx pgx.Tx, table string, fields map[string]any) (json.RawMessage, error) {
	if table != "transfer_plan" && table != "transfer_step" {
		return nil, preparationConflict()
	}
	payload, _ := json.Marshal(fields)
	var expected, actual json.RawMessage
	if e := tx.QueryRow(ctx, `SELECT to_jsonb(jsonb_populate_record(NULL::`+table+`,$1::jsonb||jsonb_build_object('created_at',statement_timestamp(),'updated_at',statement_timestamp())))`, payload).Scan(&expected); e != nil {
		return nil, e
	}
	if e := tx.QueryRow(ctx, `INSERT INTO `+table+` SELECT * FROM jsonb_populate_record(NULL::`+table+`,$1::jsonb) RETURNING to_jsonb(`+table+`)`, expected).Scan(&actual); e != nil {
		return nil, e
	}
	if !preparationSameJSON(expected, actual) {
		return nil, preparationConflict()
	}
	return actual, nil
}
func (r *movieUpgradeOrganizationPreparationRepository) recover(ctx context.Context, tx pgx.Tx, a *ports.Actor, sub, op uuid.UUID, key string, supplied *ports.PrepareMovieUpgradeOrganization) (*ports.MovieUpgradeOrganizationPreparation, error) {
	if e := db.New(tx).LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "upgrade.command", AggregateID: a.ID + ":" + key}); e != nil {
		return nil, e
	}
	var raw json.RawMessage
	e := tx.QueryRow(ctx, `SELECT to_jsonb(x) FROM movie_upgrade_organization_preparation x WHERE actor_id=$1 AND request_key=$2`, a.ID, key).Scan(&raw)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return nil, e
	}
	if errors.Is(e, pgx.ErrNoRows) {
		var used bool
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM idempotency_record WHERE actor_type=$1 AND actor_id=$2 AND idempotency_key=$3)`, string(a.Type), a.ID, key).Scan(&used); e != nil {
			return nil, e
		}
		if used {
			return nil, preparationConflict()
		}
		return nil, nil
	}
	var p struct {
		ID               uuid.UUID `json:"id"`
		OperationID      uuid.UUID `json:"operation_id"`
		AuthorizationID  uuid.UUID `json:"authorization_id"`
		TaskID           uuid.UUID `json:"task_id"`
		PlanID           uuid.UUID `json:"plan_id"`
		StepID           uuid.UUID `json:"step_id"`
		PreparedAt       time.Time `json:"prepared_at"`
		Status           string    `json:"status"`
		ExecutionAllowed bool      `json:"execution_allowed"`
	}
	if json.Unmarshal(raw, &p) != nil || p.OperationID != op || p.Status != "organization_prepared_not_executable" || p.ExecutionAllowed {
		return nil, preparationConflict()
	}
	c := ports.PrepareMovieUpgradeOrganization{OperationID: op, AuthorizationID: p.AuthorizationID, Confirmed: true}
	if supplied != nil && *supplied != c {
		return nil, preparationConflict()
	}
	hash := coverageDigest([]any{"upgrade.organization-prepare.v1", c})
	var stored ports.MovieUpgradeOrganizationPreparation
	found, e := upgradeReceipt(ctx, db.New(tx), a, key, hash, &stored)
	if e != nil {
		return nil, e
	}
	if !found {
		return nil, preparationConflict()
	}
	out := &ports.MovieUpgradeOrganizationPreparation{ID: p.ID, OperationID: op, AuthorizationID: p.AuthorizationID, TaskID: p.TaskID, PlanID: p.PlanID, StepID: p.StepID, PreparedAt: p.PreparedAt, Status: p.Status}
	// The stored response preserves its original offset, while PostgreSQL JSON
	// may render the same instant in the database session timezone. Check the
	// instant exactly, then retain the original receipt representation for the
	// complete response and immutable ledger comparisons below.
	if !p.PreparedAt.Equal(stored.PreparedAt) {
		return nil, preparationConflict()
	}
	out.PreparedAt = stored.PreparedAt
	want, _ := json.Marshal(out)
	actual, _ := json.Marshal(stored)
	if !preparationSameJSON(want, actual) {
		return nil, preparationConflict()
	}
	if e = validateOrganizationLedger(ctx, tx, a, sub, key, hash, organizationPrepareAction, p.ID, want); e != nil {
		return nil, e
	}
	return out, nil
}
