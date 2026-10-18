package postgres

import (
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"time"
)

func (r *movieUpgradeOrganizationJobRepository) CaptureMovieUpgradeOrganizationJob(ctx context.Context, a *ports.Actor, lease ports.JobLease) (out *ports.MovieUpgradeOrganizationJobClaim, err error) {
	if e := episodeReviewActor(a); e != nil {
		return nil, e
	}
	if lease.ID == uuid.Nil || lease.Owner == "" || len(lease.Owner) > 130 || lease.Token <= 0 {
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
	var op, prep, cid uuid.UUID
	if e = tx.QueryRow(work, `SELECT operation_id,preparation_id,command_id FROM movie_upgrade_organization_job WHERE job_id=$1`, lease.ID).Scan(&op, &prep, &cid); e != nil {
		return nil, e
	}
	a, sub, e := r.store.authorizePreparation(work, tx, a, op)
	if e != nil {
		return nil, e
	}
	if e = db.New(tx).LockDownloadAcquisition(work, op); e != nil {
		return nil, e
	}
	_, verify, e := r.basis(work, tx, a, op, prep)
	if e != nil {
		return nil, e
	}
	var job json.RawMessage
	var available bool
	e = tx.QueryRow(work, `SELECT to_jsonb(j),j.state='running' AND j.lease_owner=$2 AND j.lease_token=$3 AND j.lease_until>clock_timestamp() AND NOT EXISTS(SELECT 1 FROM movie_upgrade_organization_job_claim WHERE job_id=j.id) FROM job j WHERE id=$1 FOR UPDATE`, lease.ID, lease.Owner, lease.Token).Scan(&job, &available)
	if e != nil {
		return nil, e
	}
	if !available {
		return nil, preparationConflict()
	}
	var row json.RawMessage
	e = tx.QueryRow(work, `SELECT jsonb_build_object('job_id',$1::uuid,'claim_id',$2::uuid,'claimed_job',$3::jsonb,'captured_at',clock_timestamp())`, lease.ID, r.store.ids.NewUUID(), job).Scan(&row)
	if e != nil {
		return nil, e
	}
	var actual json.RawMessage
	if e = tx.QueryRow(work, `INSERT INTO movie_upgrade_organization_job_claim SELECT * FROM jsonb_populate_record(NULL::movie_upgrade_organization_job_claim,$1::jsonb) RETURNING to_jsonb(movie_upgrade_organization_job_claim)`, row).Scan(&actual); e != nil {
		return nil, e
	}
	if !preparationSameJSON(actual, row) {
		return nil, preparationConflict()
	}
	var fields struct {
		ID uuid.UUID `json:"claim_id"`
		At time.Time `json:"captured_at"`
	}
	if json.Unmarshal(row, &fields) != nil {
		return nil, preparationConflict()
	}
	out = &ports.MovieUpgradeOrganizationJobClaim{ClaimID: fields.ID, JobID: lease.ID, CommandID: cid, OperationID: op, PreparationID: prep, ClaimedAt: fields.At, Status: "claim_captured_not_executable", ExecutionAllowed: false}
	key := lease.ID.String()
	hash := coverageDigest([]any{"upgrade.organization-claim.v1", lease, row})
	own, e := readUpgradeObservationLedger(work, tx, op, a, sub, key, hash, "upgrade.organization-claim", out, false)
	if e != nil {
		return nil, e
	}
	check := func() error {
		if e := verify(); e != nil {
			return e
		}
		if e := own.verify(work, tx); e != nil {
			return e
		}
		var same bool
		if e := tx.QueryRow(work, `SELECT to_jsonb(c)=$2::jsonb AND to_jsonb(j)=$3::jsonb AND j.lease_until>clock_timestamp() FROM movie_upgrade_organization_job_claim c JOIN job j ON j.id=c.job_id WHERE c.job_id=$1`, lease.ID, row, job).Scan(&same); e != nil {
			return e
		}
		if !same {
			return preparationConflict()
		}
		_, e := tx.Exec(work, `SELECT pulse_assert_upgrade_organization_job($1,true)`, lease.ID)
		return e
	}
	ledger := &UpgradeReservationStore{pool: r.store.pool, ids: r.store.ids}
	if e = ledger.upgradeSuccess(work, tx, a, sub, "upgrade.organization-claim:"+key, hash, "upgrade.organization-claim", op, out, own, check); e != nil {
		return nil, e
	}
	recorded, e := readUpgradeObservationLedger(work, tx, op, a, sub, key, hash, "upgrade.organization-claim", out, true)
	if e != nil {
		return nil, e
	}
	if e = recorded.verify(work, tx); e != nil {
		return nil, e
	}
	if e = check(); e != nil {
		return nil, e
	}
	if e = tx.Commit(work); e != nil {
		return nil, e
	}
	return out, nil
}
