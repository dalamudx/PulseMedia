package postgres

import (
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type movieUpgradeJobObservationRepository struct {
	native *movieUpgradeNativeObservationRepository
}

// This constructor enables the job-fenced path, without registering a scheduler or worker.
func NewMovieUpgradeNativeJobObservationRepository(s *UpgradePreparationStore, d ports.MovieUpgradeNativeObservationDriver, v *MovieUpgradeNativeQueryVerifier) (ports.MovieUpgradeNativeJobObservationRepository, error) {
	r, e := NewMovieUpgradeNativeObservationRepository(s, d, v)
	if e != nil {
		return nil, e
	}
	return &movieUpgradeJobObservationRepository{r.(*movieUpgradeNativeObservationRepository)}, nil
}
func (r *movieUpgradeJobObservationRepository) ObserveMovieUpgradeJob(ctx context.Context, a *ports.Actor, req ports.MovieUpgradeObservationJobRequest) (out *ports.MovieUpgradeObservationResult, err error) {
	defer func() { err = upgradeObservationError(ctx, err) }()
	if e := episodeReviewActor(a); e != nil {
		return nil, e
	}
	if req.Lease.ID == uuid.Nil || req.Lease.Owner == "" || len(req.Lease.Owner) > 130 || req.Lease.Token <= 0 || req.OperationID == uuid.Nil || req.TaskID == uuid.Nil || !upgradeKey(req.AdmissionKey) || !upgradeKey(req.ResultKey) {
		return nil, preparationConflict()
	}
	return r.native.observeMovieUpgrade(ctx, a, req.OperationID, req.Lease.ID, req.AdmissionKey, req.ResultKey, &movieUpgradeObservationJob{request: req})
}

type movieUpgradeObservationJob struct {
	request                    ports.MovieUpgradeObservationJobRequest
	claimed, binding, terminal json.RawMessage
}

func (g *movieUpgradeObservationJob) lock(ctx context.Context, tx pgx.Tx, s *movieUpgradeObservationSession, a *ports.Actor) error {
	actor, e := uuid.Parse(a.ID)
	if e != nil {
		return preparationConflict()
	}
	var actual json.RawMessage
	var valid bool
	e = tx.QueryRow(ctx, `SELECT to_jsonb(j),j.kind=$2 AND j.dedupe_key=$3 AND j.payload=jsonb_build_object('operation_id',$4::uuid,'task_id',$5::uuid,'actor_id',$6::uuid) AND j.lease_owner=$7 AND j.lease_token=$8 AND j.attempts=1 AND j.max_attempts=1 AND j.result IS NULL AND j.error IS NULL AND j.state='running' AND j.lease_until>clock_timestamp() AND EXISTS(SELECT 1 FROM movie_upgrade_preparation p WHERE p.operation_id=$4 AND p.task_id=$5 AND p.actor_id=$6) FROM job j WHERE j.id=$1 FOR UPDATE`, g.request.Lease.ID, ports.MovieUpgradeObservationJobKind, "movie-upgrade-observation:"+s.base.operation.String(), s.base.operation, g.request.TaskID, actor, g.request.Lease.Owner, g.request.Lease.Token).Scan(&actual, &valid)
	if e == pgx.ErrNoRows {
		return preparationConflict()
	}
	if e != nil {
		return e
	}
	if !valid {
		return preparationConflict()
	}
	if len(g.claimed) == 0 {
		g.claimed = actual
	} else if !preparationSameJSON(actual, g.claimed) {
		return preparationConflict()
	}
	return nil
}
func (g *movieUpgradeObservationJob) admit(ctx context.Context, tx pgx.Tx, s *movieUpgradeObservationSession) error {
	var expected, actual json.RawMessage
	e := tx.QueryRow(ctx, `SELECT jsonb_build_object('job_id',$1::uuid,'observation_id',observation_id,'operation_id',operation_id,'task_id',task_id,'actor_id',actor_id,'admission_key',$3::text,'result_key',$4::text,'claimed_job',$5::jsonb,'original_sha256',original_sha256,'protected_sha256',protected_sha256) FROM movie_upgrade_observation WHERE observation_id=$2`, g.request.Lease.ID, s.receipt.ObservationID, g.request.AdmissionKey, g.request.ResultKey, g.claimed).Scan(&expected)
	if e != nil {
		return e
	}
	e = tx.QueryRow(ctx, `INSERT INTO movie_upgrade_observation_job SELECT * FROM jsonb_populate_record(NULL::movie_upgrade_observation_job,$1::jsonb) RETURNING to_jsonb(movie_upgrade_observation_job)`, expected).Scan(&actual)
	if e != nil {
		return e
	}
	if !preparationSameJSON(actual, expected) {
		return preparationConflict()
	}
	g.binding = expected
	return nil
}
func (g *movieUpgradeObservationJob) check(ctx context.Context, tx pgx.Tx, s *movieUpgradeObservationSession) error {
	want := g.claimed
	if len(g.terminal) > 0 {
		want = g.terminal
	}
	var same bool
	if e := tx.QueryRow(ctx, `SELECT to_jsonb(j)=$2::jsonb FROM job j WHERE id=$1 FOR UPDATE`, g.request.Lease.ID, want).Scan(&same); e != nil {
		return e
	}
	if !same {
		return preparationConflict()
	}
	if len(g.binding) == 0 {
		if e := tx.QueryRow(ctx, `SELECT state='running' AND lease_owner=$2 AND lease_token=$3 AND lease_until>clock_timestamp() FROM job WHERE id=$1`, g.request.Lease.ID, g.request.Lease.Owner, g.request.Lease.Token).Scan(&same); e != nil {
			return e
		}
		if !same {
			return preparationConflict()
		}
		return nil
	}
	var actual json.RawMessage
	if e := tx.QueryRow(ctx, `SELECT to_jsonb(b) FROM movie_upgrade_observation_job b WHERE job_id=$1`, g.request.Lease.ID).Scan(&actual); e != nil {
		return e
	}
	if !preparationSameJSON(actual, g.binding) {
		return preparationConflict()
	}
	_, e := tx.Exec(ctx, `SELECT pulse_assert_upgrade_observation_job($1,$2)`, s.receipt.ObservationID, len(g.terminal) > 0)
	return e
}
func (g *movieUpgradeObservationJob) finish(ctx context.Context, tx pgx.Tx, s *movieUpgradeObservationSession) error {
	var expected, actual json.RawMessage
	if e := tx.QueryRow(ctx, `SELECT pulse_upgrade_observation_terminal_job($1)`, s.receipt.ObservationID).Scan(&expected); e != nil {
		return e
	}
	if len(expected) == 0 {
		return preparationConflict()
	}
	e := tx.QueryRow(ctx, `UPDATE job j SET state='succeeded',result=$2::jsonb->'result',lease_until=NULL,updated_at=($2::jsonb->>'updated_at')::timestamptz,finished_at=($2::jsonb->>'finished_at')::timestamptz WHERE j.id=$1 AND to_jsonb(j)=$3::jsonb RETURNING to_jsonb(j)`, g.request.Lease.ID, expected, g.claimed).Scan(&actual)
	if e != nil {
		return e
	}
	if !preparationSameJSON(actual, expected) {
		return preparationConflict()
	}
	g.terminal = expected
	return nil
}
