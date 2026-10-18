package postgres

import (
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"strings"
	"time"
)

func organizationExecutionResult(permit, result json.RawMessage) (*ports.MovieUpgradeOrganizationExecutionResult, error) {
	var p struct {
		ID        uuid.UUID `json:"permit_id"`
		Job       uuid.UUID `json:"job_id"`
		Operation uuid.UUID `json:"operation_id"`
		Snapshot  struct {
			Command struct {
				Prep uuid.UUID `json:"preparation_id"`
			} `json:"command"`
		} `json:"snapshot"`
	}
	var v struct {
		Status string    `json:"status"`
		At     time.Time `json:"returned_at"`
	}
	if json.Unmarshal(permit, &p) != nil || json.Unmarshal(result, &v) != nil || p.ID == uuid.Nil || p.Job == uuid.Nil || v.At.IsZero() {
		return nil, preparationConflict()
	}
	return &ports.MovieUpgradeOrganizationExecutionResult{PermitID: p.ID, JobID: p.Job, OperationID: p.Operation, PreparationID: p.Snapshot.Command.Prep, Status: v.Status, ReturnedAt: v.At, ExecutionAllowed: false}, nil
}
func (s *organizationExecution) resultLedger(ctx context.Context, tx pgx.Tx, b *organizationExecutionBasis, permit, result json.RawMessage, existing bool) (ownedUpgradeRows, error) {
	out, e := organizationExecutionResult(permit, result)
	if e != nil {
		return nil, e
	}
	return readUpgradeObservationLedger(ctx, tx, s.base.operation, b.actor, b.sub, s.lease.ID.String(), coverageDigest([]any{"upgrade.organization-physical-result.v1", permit, result}), "upgrade.organization-physical-result", out, existing)
}
func (s *organizationExecution) record(ctx context.Context, evidence json.RawMessage, status string) (*ports.MovieUpgradeOrganizationExecutionResult, error) {
	tx, e := s.base.conn.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer upgradeRollback(ctx, tx)
	b, e := s.load(ctx, tx)
	if e != nil {
		return nil, e
	}
	permitOwn, e := s.permitLedger(ctx, tx, b, s.permit, true)
	if e != nil {
		return nil, e
	}
	var fields organizationPhysicalPermit
	if json.Unmarshal(s.permit, &fields) != nil {
		return nil, preparationConflict()
	}
	var expected json.RawMessage
	e = tx.QueryRow(ctx, `SELECT jsonb_build_object('permit_id',$1::uuid,'job_id',$2::uuid,'status',$3::text,'evidence',$4::jsonb,'returned_at',n,'finished_job',$5::jsonb||jsonb_build_object('state','succeeded','lease_owner',NULL,'lease_until',NULL,'result',jsonb_build_object('organization_permit_id',$1::uuid,'status',$3::text),'updated_at',n,'finished_at',n)) FROM(SELECT clock_timestamp() n)t`, fields.ID, s.lease.ID, status, evidence, b.job).Scan(&expected)
	if e != nil {
		return nil, e
	}
	var actual json.RawMessage
	e = tx.QueryRow(ctx, `INSERT INTO movie_upgrade_organization_execution_result SELECT * FROM jsonb_populate_record(NULL::movie_upgrade_organization_execution_result,$1::jsonb) RETURNING to_jsonb(movie_upgrade_organization_execution_result)`, expected).Scan(&actual)
	if e != nil {
		return nil, e
	}
	if !preparationSameJSON(actual, expected) {
		return nil, preparationConflict()
	}
	var result struct {
		Finished json.RawMessage `json:"finished_job"`
	}
	if json.Unmarshal(expected, &result) != nil {
		return nil, preparationConflict()
	}
	var finished json.RawMessage
	e = tx.QueryRow(ctx, `WITH expected AS(SELECT * FROM jsonb_populate_record(NULL::job,$2::jsonb)) UPDATE job j SET state=expected.state,lease_owner=expected.lease_owner,lease_until=expected.lease_until,result=expected.result,updated_at=expected.updated_at,finished_at=expected.finished_at FROM expected WHERE j.id=$1 RETURNING to_jsonb(j)`, s.lease.ID, result.Finished).Scan(&finished)
	if e != nil {
		return nil, e
	}
	if !preparationSameJSON(finished, result.Finished) {
		return nil, preparationConflict()
	}
	own, e := s.resultLedger(ctx, tx, b, s.permit, expected, false)
	if e != nil {
		return nil, e
	}
	out, e := organizationExecutionResult(s.permit, expected)
	if e != nil {
		return nil, e
	}
	ledger := &UpgradeReservationStore{pool: s.repo.store.pool, ids: s.repo.store.ids}
	check := func() error {
		if e := s.check(ctx, tx, b, result.Finished); e != nil {
			return e
		}
		if e := own.verify(ctx, tx); e != nil {
			return e
		}
		return s.checkPermit(ctx, tx, s.permit)
	}
	if e = ledger.upgradeSuccess(ctx, tx, b.actor, b.sub, "upgrade.organization-physical-result:"+s.lease.ID.String(), coverageDigest([]any{"upgrade.organization-physical-result.v1", s.permit, expected}), "upgrade.organization-physical-result", s.base.operation, out, own, check); e != nil {
		return nil, e
	}
	if _, e = s.resultLedger(ctx, tx, b, s.permit, expected, true); e != nil {
		return nil, e
	}
	if e = s.check(ctx, tx, b, result.Finished); e != nil {
		return nil, e
	}
	if e = permitOwn.verify(ctx, tx); e != nil {
		return nil, e
	}
	if e = own.verify(ctx, tx); e != nil {
		return nil, e
	}
	var same bool
	e = tx.QueryRow(ctx, `SELECT to_jsonb(r)=$2::jsonb FROM movie_upgrade_organization_execution_result r WHERE permit_id=$1`, fields.ID, expected).Scan(&same)
	if e != nil {
		return nil, e
	}
	if !same {
		return nil, preparationConflict()
	}
	// The database-time check is deliberately last, after all waiting locks and ledgers.
	if e = s.checkPermit(ctx, tx, s.permit); e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return organizationExecutionResult(s.permit, expected)
}

func (r *movieUpgradeOrganizationExecutionRepository) ReadMovieUpgradeOrganizationExecutionReceipt(ctx context.Context, a *ports.Actor, job uuid.UUID) (out *ports.MovieUpgradeOrganizationExecutionResult, err error) {
	if err = episodeReviewActor(a); err != nil {
		return nil, err
	}
	if job == uuid.Nil {
		return nil, preparationConflict()
	}
	defer func() { err = upgradeError(ctx, err) }()
	work, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	tx, e := r.store.pool.Begin(work)
	if e != nil {
		return nil, e
	}
	defer upgradeRollback(work, tx)
	var op uuid.UUID
	e = tx.QueryRow(work, `SELECT operation_id FROM movie_upgrade_organization_job WHERE job_id=$1`, job).Scan(&op)
	if e != nil {
		return nil, e
	}
	a, sub, e := r.store.authorizePreparation(work, tx, a, op)
	if e != nil {
		return nil, e
	}
	var permit, result json.RawMessage
	e = tx.QueryRow(work, `SELECT to_jsonb(p),to_jsonb(r) FROM movie_upgrade_organization_execution_permit p JOIN movie_upgrade_organization_execution_result r USING(permit_id) WHERE p.job_id=$1 FOR SHARE OF p,r`, job).Scan(&permit, &result)
	if e == pgx.ErrNoRows {
		if e = tx.Commit(work); e != nil {
			return nil, e
		}
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	b := &organizationExecutionBasis{actor: a, sub: sub}
	var prepKey, commandKey string
	var prep, auth uuid.UUID
	var currentJob json.RawMessage
	e = tx.QueryRow(work, `SELECT to_jsonb(b),to_jsonb(c),to_jsonb(p),to_jsonb(j),b.preparation_id,p.authorization_id,p.request_key,b.request_key FROM movie_upgrade_organization_job b JOIN movie_upgrade_organization_job_claim c USING(job_id) JOIN movie_upgrade_organization_preparation p ON p.id=b.preparation_id JOIN job j ON j.id=b.job_id WHERE b.job_id=$1 FOR SHARE OF b,c,p,j`, job).Scan(&b.command, &b.claim, &b.prepared, &currentJob, &prep, &auth, &prepKey, &commandKey)
	if e != nil {
		return nil, e
	}
	var p struct {
		Snapshot struct{ Command, Claim, Preparation json.RawMessage } `json:"snapshot"`
	}
	var c struct {
		Job struct {
			Owner string `json:"lease_owner"`
			Token int64  `json:"lease_token"`
		} `json:"claimed_job"`
	}
	if json.Unmarshal(permit, &p) != nil || json.Unmarshal(b.claim, &c) != nil || !preparationSameJSON(p.Snapshot.Command, b.command) || !preparationSameJSON(p.Snapshot.Claim, b.claim) || !preparationSameJSON(p.Snapshot.Preparation, b.prepared) {
		return nil, preparationConflict()
	}
	s := &organizationExecution{repo: r, base: &movieUpgradeSendSession{operation: op}, actor: a, lease: ports.JobLease{ID: job, Owner: c.Job.Owner, Token: c.Job.Token}}
	physical := movieUpgradeOrganizationPreparationRepository{store: r.store}
	old, e := physical.recover(work, tx, a, sub, op, prepKey, nil)
	if e != nil {
		return nil, e
	}
	if old == nil || old.ID != prep || old.AuthorizationID != auth {
		return nil, preparationConflict()
	}
	logical := movieUpgradeOrganizationJobRepository{store: r.store}
	queued, e := logical.recover(work, tx, a, sub, op, strings.TrimPrefix(commandKey, "upgrade.organization-queue:"), nil)
	if e != nil {
		return nil, e
	}
	if queued == nil || queued.JobID != job {
		return nil, preparationConflict()
	}
	var finished bool
	if e = tx.QueryRow(work, `SELECT pulse_upgrade_organization_execution_finished($1,$2::jsonb)`, job, currentJob).Scan(&finished); e != nil {
		return nil, e
	}
	if !finished {
		return nil, preparationConflict()
	}
	claimOwn, e := s.claimLedger(work, tx, b)
	if e != nil {
		return nil, e
	}
	permitOwn, e := s.permitLedger(work, tx, b, permit, true)
	if e != nil {
		return nil, e
	}
	resultOwn, e := s.resultLedger(work, tx, b, permit, result, true)
	if e != nil {
		return nil, e
	}
	for _, own := range []ownedUpgradeRows{claimOwn, permitOwn, resultOwn} {
		if e = own.verify(work, tx); e != nil {
			return nil, e
		}
	}
	if _, _, e = r.store.authorizePreparation(work, tx, a, op); e != nil {
		return nil, e
	}
	if e = tx.Commit(work); e != nil {
		return nil, e
	}
	return organizationExecutionResult(permit, result)
}
