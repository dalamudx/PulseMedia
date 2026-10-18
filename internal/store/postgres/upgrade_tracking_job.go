package postgres

import (
	"context"
	"encoding/json"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type movieUpgradeNativeJobTrackingRepository struct {
	native *movieUpgradeNativeTrackingRepository
}

func NewMovieUpgradeNativeJobTrackingRepository(s *UpgradePreparationStore, d ports.MovieUpgradeNativeTrackingDriver, v *MovieUpgradeTrackingQueryVerifier) (ports.MovieUpgradeNativeJobTrackingRepository, error) {
	r, e := NewMovieUpgradeNativeTrackingRepository(s, d, v)
	if e != nil {
		return nil, e
	}
	return &movieUpgradeNativeJobTrackingRepository{r.(*movieUpgradeNativeTrackingRepository)}, nil
}
func (r *movieUpgradeNativeJobTrackingRepository) TrackMovieUpgradeJob(ctx context.Context, a *ports.Actor, req ports.MovieUpgradeTrackingJobRequest) (*ports.MovieUpgradeTrackingResult, error) {
	if req.OperationID == uuid.Nil || req.TaskID == uuid.Nil || req.Lease.ID == uuid.Nil || req.Lease.Owner == "" || len(req.Lease.Owner) > 130 || req.Lease.Token <= 0 {
		return nil, preparationConflict()
	}
	return r.native.trackMovieUpgrade(ctx, a, req.OperationID, req.Lease.ID, "upgrade-track-admit:"+req.Lease.ID.String(), "upgrade-track-result:"+req.Lease.ID.String(), &movieUpgradeTrackingJob{request: req})
}

type movieUpgradeTrackingJob struct {
	request                            ports.MovieUpgradeTrackingJobRequest
	queued, claimed, binding, terminal json.RawMessage
}

func (g *movieUpgradeTrackingJob) lock(ctx context.Context, tx pgx.Tx, s *movieUpgradeTrackingSession, a *ports.Actor) error {
	actor, e := uuid.Parse(a.ID)
	if e != nil {
		return preparationConflict()
	}
	var queued, actual json.RawMessage
	var valid bool
	e = tx.QueryRow(ctx, `SELECT to_jsonb(b),to_jsonb(j),b.operation_id=$2 AND b.task_id=$3 AND b.actor_id=$4 AND j.state='running' AND j.lease_owner=$5 AND j.lease_token=$6 AND j.lease_until>clock_timestamp() AND pulse_upgrade_tracking_job_running(j,b) FROM movie_upgrade_tracking_job b JOIN job j ON j.id=b.job_id WHERE b.job_id=$1 FOR UPDATE OF j`, g.request.Lease.ID, s.base.operation, g.request.TaskID, actor, g.request.Lease.Owner, g.request.Lease.Token).Scan(&queued, &actual, &valid)
	if e != nil {
		return e
	}
	if !valid {
		return preparationConflict()
	}
	if len(g.claimed) == 0 {
		g.claimed, g.queued = actual, queued
	} else if !preparationSameJSON(g.claimed, actual) || !preparationSameJSON(g.queued, queued) {
		return preparationConflict()
	}
	return nil
}
func (g *movieUpgradeTrackingJob) capture(ctx context.Context, tx pgx.Tx, s *movieUpgradeTrackingSession) error {
	var expected, actual json.RawMessage
	e := tx.QueryRow(ctx, `SELECT jsonb_build_object('job_id',$1::uuid,'observation_id',$2::uuid,'binding_sha256',sha256(convert_to($3::jsonb::text,'UTF8')),'admission_key',$4::text,'result_key',$5::text,'claimed_job',$6::jsonb)`, g.request.Lease.ID, s.binding.ObservationID, g.queued, "upgrade-track-admit:"+g.request.Lease.ID.String(), "upgrade-track-result:"+g.request.Lease.ID.String(), g.claimed).Scan(&expected)
	if e != nil {
		return e
	}
	e = tx.QueryRow(ctx, `INSERT INTO movie_upgrade_tracking_job_claim SELECT * FROM jsonb_populate_record(NULL::movie_upgrade_tracking_job_claim,$1::jsonb) RETURNING to_jsonb(movie_upgrade_tracking_job_claim)`, expected).Scan(&actual)
	if e != nil {
		return e
	}
	if !preparationSameJSON(expected, actual) {
		return preparationConflict()
	}
	g.binding = expected
	return nil
}
func (g *movieUpgradeTrackingJob) check(ctx context.Context, tx pgx.Tx) error {
	want := g.claimed
	if len(g.terminal) > 0 {
		want = g.terminal
	}
	var same bool
	if e := tx.QueryRow(ctx, `SELECT to_jsonb(j)=$2::jsonb AND (to_jsonb(b)=$3::jsonb) AND ($4::jsonb->>'lease_until')::timestamptz>clock_timestamp() FROM job j JOIN movie_upgrade_tracking_job b ON b.job_id=j.id WHERE j.id=$1 FOR UPDATE OF j`, g.request.Lease.ID, want, g.queued, g.claimed).Scan(&same); e != nil {
		return e
	}
	if !same {
		return preparationConflict()
	}
	if len(g.binding) > 0 {
		if e := tx.QueryRow(ctx, `SELECT to_jsonb(c)=$2::jsonb FROM movie_upgrade_tracking_job_claim c WHERE job_id=$1`, g.request.Lease.ID, g.binding).Scan(&same); e != nil {
			return e
		}
		if !same {
			return preparationConflict()
		}
	}
	return nil
}
func (g *movieUpgradeTrackingJob) finish(ctx context.Context, tx pgx.Tx, s *movieUpgradeTrackingSession) error {
	var expected, actual json.RawMessage
	if e := tx.QueryRow(ctx, `SELECT pulse_upgrade_tracking_terminal_job($1)`, s.binding.ObservationID).Scan(&expected); e != nil {
		return e
	}
	if len(expected) == 0 {
		return preparationConflict()
	}
	e := tx.QueryRow(ctx, `UPDATE job j SET state='succeeded',result=$2::jsonb->'result',lease_until=NULL,updated_at=($2::jsonb->>'updated_at')::timestamptz,finished_at=($2::jsonb->>'finished_at')::timestamptz WHERE id=$1 AND to_jsonb(j)=$3::jsonb RETURNING to_jsonb(j)`, g.request.Lease.ID, expected, g.claimed).Scan(&actual)
	if e != nil {
		return e
	}
	if !preparationSameJSON(expected, actual) {
		return preparationConflict()
	}
	g.terminal = expected
	return nil
}
