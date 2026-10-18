package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type movieUpgradeNativeJobSendRepository struct{ native *movieUpgradeNativeRepository }

func NewMovieUpgradeNativeJobSendRepository(s *UpgradePreparationStore, d ports.MovieUpgradeNativeSendDriver, v *MovieUpgradeNativeDispatchVerifier) (ports.MovieUpgradeNativeJobSendRepository, error) {
	r, e := NewMovieUpgradeNativeSendRepository(s, d, v)
	if e != nil {
		return nil, e
	}
	return &movieUpgradeNativeJobSendRepository{native: r.(*movieUpgradeNativeRepository)}, nil
}
func (r *movieUpgradeNativeJobSendRepository) SendMovieUpgradeJob(ctx context.Context, a *ports.Actor, req ports.MovieUpgradeSendJobRequest, proof ports.MovieUpgradeMetainfoProof) (*ports.MovieUpgradeSendResultReceipt, error) {
	if e := episodeReviewActor(a); e != nil {
		return nil, e
	}
	if req.Lease.ID == uuid.Nil || req.Lease.Owner == "" || len(req.Lease.Owner) > 130 || req.Lease.Token <= 0 || req.OperationID == uuid.Nil || req.TaskID == uuid.Nil || req.CommandID == uuid.Nil {
		return nil, preparationConflict()
	}
	return r.native.sendMovieUpgrade(ctx, a, req.OperationID, req.Lease.ID, proof, "job:"+req.CommandID.String(), &movieUpgradeSendJob{request: req})
}

type movieUpgradeSendJob struct {
	request                             ports.MovieUpgradeSendJobRequest
	actor                               *ports.Actor
	command, claimed, capture, terminal json.RawMessage
	returning                           bool
}

func (s *movieUpgradeSendSession) lockSendJob(ctx context.Context, tx pgx.Tx, a *ports.Actor) error {
	return s.lockBoundSendJob(ctx, tx, a, false)
}
func (s *movieUpgradeSendSession) lockSendResultJob(ctx context.Context, tx pgx.Tx, a *ports.Actor) error {
	return s.lockBoundSendJob(ctx, tx, a, true)
}
func (s *movieUpgradeSendSession) lockBoundSendJob(ctx context.Context, tx pgx.Tx, a *ports.Actor, historicalReturn bool) error {
	if s.sendJob != nil {
		return s.sendJob.lock(ctx, tx, s, a)
	}
	var installed bool
	if e := tx.QueryRow(ctx, `SELECT to_regclass('movie_upgrade_send_job') IS NOT NULL`).Scan(&installed); e != nil {
		return e
	}
	if !installed {
		return nil
	}
	if !historicalReturn {
		return preparationConflict()
	}
	var exists bool
	if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM movie_upgrade_send_job WHERE operation_id=$1)`, s.operation).Scan(&exists); e != nil {
		return e
	}
	if exists {
		return preparationConflict()
	}
	return nil
}
func (g *movieUpgradeSendJob) lock(ctx context.Context, tx pgx.Tx, s *movieUpgradeSendSession, a *ports.Actor) error {
	var actual, command json.RawMessage
	var valid bool
	e := tx.QueryRow(ctx, `SELECT to_jsonb(j),to_jsonb(b),b.operation_id=$2 AND b.task_id=$3 AND b.command_id=$4 AND b.actor_id=$5::uuid AND b.send_key=$6 AND j.state='running' AND j.lease_owner=$7 AND j.lease_token=$8 AND j.attempts=1 AND j.max_attempts=1 AND j.result IS NULL AND j.error IS NULL AND j.lease_until>clock_timestamp() AND pulse_is_upgrade_send_job(j) FROM movie_upgrade_send_job b JOIN job j ON j.id=b.job_id WHERE b.job_id=$1 FOR UPDATE OF j`, g.request.Lease.ID, s.operation, g.request.TaskID, g.request.CommandID, a.ID, "job:"+g.request.CommandID.String(), g.request.Lease.Owner, g.request.Lease.Token).Scan(&actual, &command, &valid)
	if errors.Is(e, pgx.ErrNoRows) {
		return preparationConflict()
	}
	if e != nil {
		return e
	}
	if !valid || (len(g.request.ExpectedClaim) > 0 && !preparationSameJSON(g.request.ExpectedClaim, actual)) {
		return preparationConflict()
	}
	g.actor = a
	if len(g.claimed) == 0 {
		var existed bool
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM movie_upgrade_send_job_claim WHERE job_id=$1)`, g.request.Lease.ID).Scan(&existed); e != nil {
			return e
		}
		if existed {
			return ports.ErrMovieUpgradeSendObservation
		}
		var expected, captured json.RawMessage
		e = tx.QueryRow(ctx, `SELECT jsonb_build_object('job_id',$1::uuid,'claimed_job',$2::jsonb,'session_nonce',$3::uuid,'backend_pid',pg_backend_pid(),'backend_start',(SELECT backend_start FROM pg_stat_activity WHERE pid=pg_backend_pid()),'captured_at',clock_timestamp())`, g.request.Lease.ID, actual, s.nonce).Scan(&expected)
		if e != nil {
			return e
		}
		if e = tx.QueryRow(ctx, `INSERT INTO movie_upgrade_send_job_claim SELECT * FROM jsonb_populate_record(NULL::movie_upgrade_send_job_claim,$1::jsonb) RETURNING to_jsonb(movie_upgrade_send_job_claim)`, expected).Scan(&captured); e != nil {
			return e
		}
		if !preparationSameJSON(expected, captured) {
			return preparationConflict()
		}
		g.claimed, g.command, g.capture = actual, command, expected
	} else if !preparationSameJSON(g.claimed, actual) || !preparationSameJSON(g.command, command) {
		return preparationConflict()
	}
	if g.returning {
		return nil
	}
	return g.check(ctx, tx, s)
}
func (g *movieUpgradeSendJob) check(ctx context.Context, tx pgx.Tx, s *movieUpgradeSendSession) error {
	var same bool
	if e := tx.QueryRow(ctx, `SELECT to_jsonb(b)=$2::jsonb AND to_jsonb(c)=$3::jsonb FROM movie_upgrade_send_job b JOIN movie_upgrade_send_job_claim c USING(job_id) WHERE b.job_id=$1`, g.request.Lease.ID, g.command, g.capture).Scan(&same); e != nil {
		return e
	}
	if !same {
		return preparationConflict()
	}
	_, e := tx.Exec(ctx, `SELECT pulse_assert_upgrade_send_job($1,$2)`, g.request.Lease.ID, len(g.terminal) > 0)
	return sendJobError(ctx, e)
}
func (g *movieUpgradeSendJob) finish(ctx context.Context, tx pgx.Tx) error {
	var expected, actual json.RawMessage
	if e := tx.QueryRow(ctx, `SELECT pulse_upgrade_send_job_terminal($1)`, g.request.Lease.ID).Scan(&expected); e != nil {
		return e
	}
	if len(expected) == 0 || string(expected) == "null" {
		return preparationConflict()
	}
	e := tx.QueryRow(ctx, `UPDATE job j SET state='succeeded',result=x.result,lease_until=NULL,updated_at=x.updated_at,finished_at=x.finished_at FROM jsonb_populate_record(NULL::job,$2::jsonb) x WHERE j.id=$1 AND to_jsonb(j)=$3::jsonb RETURNING to_jsonb(j)`, g.request.Lease.ID, expected, g.claimed).Scan(&actual)
	if e != nil {
		return e
	}
	if !preparationSameJSON(expected, actual) {
		return preparationConflict()
	}
	g.terminal = expected
	return nil
}
func sendJobError(ctx context.Context, e error) error {
	var p *pgconn.PgError
	if errors.As(e, &p) && p.Code == "23514" && (p.ConstraintName == "movie_upgrade_send_job_lease" || p.ConstraintName == "movie_upgrade_send_job_frozen" || p.ConstraintName == "movie_upgrade_send_job_claim" || p.ConstraintName == "movie_upgrade_send_job_command") {
		return preparationConflict()
	}
	return e
}

// Called while the native session gate and shared execution budget are held.
func (s *movieUpgradeSendSession) checkMovieUpgradeSendJobPermit(ctx context.Context) error {
	if s.sendJob == nil {
		var installed bool
		if e := s.conn.QueryRow(ctx, `SELECT to_regclass('movie_upgrade_send_job') IS NOT NULL`).Scan(&installed); e != nil {
			return e
		}
		if installed {
			return preparationConflict()
		}
		return nil
	}
	tx, e := s.conn.Begin(ctx)
	if e != nil {
		return e
	}
	defer upgradeRollback(ctx, tx)
	a := s.sendJob.actor
	if a == nil {
		return preparationConflict()
	}
	a, _, e = s.store.authorizePreparation(ctx, tx, a, s.operation)
	if e != nil {
		return e
	}
	b, e := s.store.loadMovieUpgradeBasis(ctx, tx, a, s.operation, true)
	if e != nil {
		return e
	}
	if e = bindPreparedUpgradeBasis(ctx, tx, b); e != nil {
		return e
	}
	if e = s.lockSendJob(ctx, tx, a); e != nil {
		return e
	}
	if e = b.check(ctx, tx); e != nil {
		return e
	}
	if e = s.sendJob.check(ctx, tx, s); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

var _ ports.MovieUpgradeNativeJobSendRepository = (*movieUpgradeNativeJobSendRepository)(nil)
