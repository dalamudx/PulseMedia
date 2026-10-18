package postgres

import (
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"time"
)

type movieUpgradeOrganizationJobRepository struct{ store *UpgradePreparationStore }

func NewMovieUpgradeOrganizationJobRepository(s *UpgradePreparationStore) (ports.MovieUpgradeOrganizationJobRepository, error) {
	if s == nil {
		return nil, preparationConflict()
	}
	return &movieUpgradeOrganizationJobRepository{store: s}, nil
}
func organizationJobReceipt(raw json.RawMessage) (*ports.MovieUpgradeOrganizationJobReceipt, error) {
	var v struct {
		CommandID     uuid.UUID `json:"command_id"`
		JobID         uuid.UUID `json:"job_id"`
		OperationID   uuid.UUID `json:"operation_id"`
		PreparationID uuid.UUID `json:"preparation_id"`
		At            time.Time `json:"commanded_at"`
	}
	if json.Unmarshal(raw, &v) != nil || v.CommandID == uuid.Nil || v.JobID == uuid.Nil {
		return nil, preparationConflict()
	}
	return &ports.MovieUpgradeOrganizationJobReceipt{CommandID: v.CommandID, JobID: v.JobID, OperationID: v.OperationID, PreparationID: v.PreparationID, QueuedAt: v.At, Status: "queued", ExecutionAllowed: false}, nil
}
func (r *movieUpgradeOrganizationJobRepository) recover(ctx context.Context, tx pgx.Tx, a *ports.Actor, sub, op uuid.UUID, key string, supplied *ports.QueueMovieUpgradeOrganization) (*ports.MovieUpgradeOrganizationJobReceipt, error) {
	var raw json.RawMessage
	if e := tx.QueryRow(ctx, `SELECT to_jsonb(b) FROM movie_upgrade_organization_job b WHERE operation_id=$1 AND actor_id=$2::uuid AND request_key=$3 FOR SHARE`, op, a.ID, "upgrade.organization-queue:"+key).Scan(&raw); e != nil {
		if e == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, e
	}
	out, e := organizationJobReceipt(raw)
	if e != nil {
		return nil, e
	}
	c := ports.QueueMovieUpgradeOrganization{OperationID: op, PreparationID: out.PreparationID, Confirmed: true}
	if supplied != nil && *supplied != c {
		return nil, preparationConflict()
	}
	hash := coverageDigest([]any{"upgrade.organization-queue.v1", c})
	own, e := readUpgradeObservationLedger(ctx, tx, op, a, sub, key, hash, "upgrade.organization-queue", out, true)
	if e != nil {
		return nil, e
	}
	if _, _, e = r.store.authorizePreparation(ctx, tx, a, op); e != nil {
		return nil, e
	}
	if e = own.verify(ctx, tx); e != nil {
		return nil, e
	}
	return out, nil
}
func (r *movieUpgradeOrganizationJobRepository) ReadMovieUpgradeOrganizationJobReceipt(ctx context.Context, a *ports.Actor, op uuid.UUID, key string) (out *ports.MovieUpgradeOrganizationJobReceipt, err error) {
	if e := episodeReviewActor(a); e != nil {
		return nil, e
	}
	if op == uuid.Nil || !upgradeKey(key) || len(key) > 100 {
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
	a, sub, e := r.store.authorizePreparation(work, tx, a, op)
	if e != nil {
		return nil, e
	}
	out, e = r.recover(work, tx, a, sub, op, key, nil)
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(work); e != nil {
		return nil, e
	}
	return out, nil
}
func (r *movieUpgradeOrganizationJobRepository) basis(ctx context.Context, tx pgx.Tx, a *ports.Actor, op, prep uuid.UUID) (json.RawMessage, func() error, error) {
	var raw json.RawMessage
	var key string
	var auth uuid.UUID
	e := tx.QueryRow(ctx, `SELECT to_jsonb(p),request_key,authorization_id FROM movie_upgrade_organization_preparation p WHERE id=$1 AND operation_id=$2 AND actor_id=$3::uuid FOR SHARE`, prep, op, a.ID).Scan(&raw, &key, &auth)
	if e != nil {
		return nil, nil, e
	}
	_, sub, e := r.store.authorizePreparation(ctx, tx, a, op)
	if e != nil {
		return nil, nil, e
	}
	physical := &movieUpgradeOrganizationPreparationRepository{store: r.store}
	old, e := physical.recover(ctx, tx, a, sub, op, key, nil)
	if e != nil {
		return nil, nil, e
	}
	if old == nil || old.ID != prep {
		return nil, nil, preparationConflict()
	}
	input, e := physical.inputs(ctx, tx, a, ports.PrepareMovieUpgradeOrganization{OperationID: op, AuthorizationID: auth})
	if e != nil {
		return nil, nil, e
	}
	var snap struct {
		Snapshot json.RawMessage `json:"snapshot"`
	}
	var fields struct {
		Basis         json.RawMessage `json:"basis"`
		Authorization json.RawMessage `json:"authorization"`
	}
	if json.Unmarshal(raw, &snap) != nil || json.Unmarshal(snap.Snapshot, &fields) != nil || !preparationSameJSON(fields.Basis, input.basis.raw) || !preparationSameJSON(fields.Authorization, input.authorization) {
		return nil, nil, preparationConflict()
	}
	verify := func() error {
		if e := input.basis.verify(); e != nil {
			return e
		}
		recovered, e := physical.recover(ctx, tx, a, sub, op, key, nil)
		if e != nil {
			return e
		}
		if recovered == nil || *recovered != *old {
			return preparationConflict()
		}
		var same bool
		if e = tx.QueryRow(ctx, `SELECT to_jsonb(p)=$2::jsonb FROM movie_upgrade_organization_preparation p WHERE id=$1`, prep, raw).Scan(&same); e != nil {
			return e
		}
		if !same {
			return preparationConflict()
		}
		_, e = tx.Exec(ctx, `SELECT pulse_assert_upgrade_organization_preparation($1)`, op)
		return e
	}
	if e = verify(); e != nil {
		return nil, nil, e
	}
	return raw, verify, nil
}
func (r *movieUpgradeOrganizationJobRepository) QueueMovieUpgradeOrganization(ctx context.Context, a *ports.Actor, c ports.QueueMovieUpgradeOrganization, key string) (out *ports.MovieUpgradeOrganizationJobReceipt, err error) {
	if e := episodeReviewActor(a); e != nil {
		return nil, e
	}
	if !c.Confirmed || c.OperationID == uuid.Nil || c.PreparationID == uuid.Nil || !upgradeKey(key) || len(key) > 100 {
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
	a, sub, e := r.store.authorizePreparation(work, tx, a, c.OperationID)
	if e != nil {
		return nil, e
	}
	if e = db.New(tx).LockDownloadAcquisition(work, c.OperationID); e != nil {
		return nil, e
	}
	if out, e = r.recover(work, tx, a, sub, c.OperationID, key, &c); e != nil {
		return nil, e
	} else if out != nil {
		if e = tx.Commit(work); e != nil {
			return nil, e
		}
		return out, nil
	}
	prepared, verify, e := r.basis(work, tx, a, c.OperationID, c.PreparationID)
	if e != nil {
		return nil, e
	}
	jid, cid := r.store.ids.NewUUID(), r.store.ids.NewUUID()
	var job, binding json.RawMessage
	e = tx.QueryRow(work, `SELECT to_jsonb(jsonb_populate_record(NULL::job,jsonb_build_object('id',$1::uuid,'kind',$2::text,'dedupe_key',$3::text,'state','queued','priority',0,'payload',jsonb_build_object('organization_job_id',$1::uuid),'attempts',0,'max_attempts',1,'lease_token',0,'created_at',n,'updated_at',n,'not_before',n))) FROM(SELECT clock_timestamp() n)t`, jid, ports.MovieUpgradeOrganizationJobKind, "movie-upgrade-organization:"+c.OperationID.String()).Scan(&job)
	if e != nil {
		return nil, e
	}
	e = tx.QueryRow(work, `SELECT jsonb_build_object('job_id',$1::uuid,'command_id',$2::uuid,'operation_id',$3::uuid,'preparation_id',$4::uuid,'actor_id',$5::uuid,'request_key',$6::text,'preparation_snapshot',$7::jsonb,'queued_job',$8::jsonb,'commanded_at',($8::jsonb->>'created_at')::timestamptz)`, jid, cid, c.OperationID, c.PreparationID, a.ID, "upgrade.organization-queue:"+key, prepared, job).Scan(&binding)
	if e != nil {
		return nil, e
	}
	for _, v := range []struct {
		table string
		row   json.RawMessage
	}{{"movie_upgrade_organization_job", binding}, {"job", job}} {
		var actual json.RawMessage
		if e = tx.QueryRow(work, "INSERT INTO "+v.table+" SELECT * FROM jsonb_populate_record(NULL::"+v.table+",$1::jsonb) RETURNING to_jsonb("+v.table+")", v.row).Scan(&actual); e != nil {
			return nil, e
		}
		if !preparationSameJSON(actual, v.row) {
			return nil, preparationConflict()
		}
	}
	out, e = organizationJobReceipt(binding)
	if e != nil {
		return nil, e
	}
	hash := coverageDigest([]any{"upgrade.organization-queue.v1", c})
	own, e := readUpgradeObservationLedger(work, tx, c.OperationID, a, sub, key, hash, "upgrade.organization-queue", out, false)
	if e != nil {
		return nil, e
	}
	check := func() error {
		if e := verify(); e != nil {
			return e
		}
		var same bool
		if e := tx.QueryRow(work, `SELECT to_jsonb(b)=$2::jsonb AND to_jsonb(j)=$3::jsonb FROM movie_upgrade_organization_job b JOIN job j ON j.id=b.job_id WHERE b.job_id=$1`, jid, binding, job).Scan(&same); e != nil {
			return e
		}
		if !same {
			return preparationConflict()
		}
		return own.verify(work, tx)
	}
	ledger := &UpgradeReservationStore{pool: r.store.pool, ids: r.store.ids}
	if e = ledger.upgradeSuccess(work, tx, a, sub, "upgrade.organization-queue:"+key, hash, "upgrade.organization-queue", c.OperationID, out, own, check); e != nil {
		return nil, e
	}
	if _, e = r.recover(work, tx, a, sub, c.OperationID, key, &c); e != nil {
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

var _ ports.MovieUpgradeOrganizationJobRepository = (*movieUpgradeOrganizationJobRepository)(nil)
