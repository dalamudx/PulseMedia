package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type MovieUpgradeTrackingRuntimeStore struct {
	store *UpgradePreparationStore
	mu    sync.Mutex
	after uuid.UUID
}

func NewMovieUpgradeTrackingRuntimeStore(s *UpgradePreparationStore) (*MovieUpgradeTrackingRuntimeStore, error) {
	if s == nil {
		return nil, ports.ErrUnavailable
	}
	return &MovieUpgradeTrackingRuntimeStore{store: s}, nil
}

type upgradeTrackingHint struct {
	operation, task, actor uuid.UUID
	prior                  *uuid.UUID
}

const upgradeTrackingTip = `LEFT JOIN LATERAL(SELECT o.observation_id,r.outcome,r.recorded_at FROM movie_upgrade_tracking_observation o LEFT JOIN movie_upgrade_tracking_result r USING(observation_id) WHERE o.operation_id=p.operation_id AND NOT EXISTS(SELECT 1 FROM movie_upgrade_tracking_observation child WHERE child.prior_observation_id=o.observation_id)) tip ON true`
const upgradeTrackingReady = `(tip.observation_id IS NULL OR (tip.outcome='tracking' AND tip.recorded_at<=clock_timestamp()-interval '30 seconds')) AND NOT EXISTS(SELECT 1 FROM movie_upgrade_tracking_job b WHERE b.operation_id=p.operation_id AND b.prior_observation_id IS NOT DISTINCT FROM tip.observation_id)`

func (r *MovieUpgradeTrackingRuntimeStore) ScheduleMovieUpgradeTracking(ctx context.Context) (int, error) {
	r.mu.Lock()
	after := r.after
	r.mu.Unlock()
	rows, e := r.store.pool.Query(ctx, `SELECT p.operation_id,p.task_id,p.actor_id,tip.observation_id FROM movie_upgrade_preparation p JOIN movie_upgrade_send_result sent USING(operation_id) `+upgradeTrackingTip+` WHERE sent.status='confirmed' AND p.operation_id>$1 AND `+upgradeTrackingReady+` ORDER BY p.operation_id LIMIT 25`, after)
	if e != nil {
		return 0, upgradeObservationError(ctx, e)
	}
	var hints []upgradeTrackingHint
	for rows.Next() {
		var h upgradeTrackingHint
		if e = rows.Scan(&h.operation, &h.task, &h.actor, &h.prior); e != nil {
			rows.Close()
			return 0, e
		}
		hints = append(hints, h)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return 0, e
	}
	if len(hints) == 0 {
		r.mu.Lock()
		if r.after == after {
			r.after = uuid.Nil
		}
		r.mu.Unlock()
		return 0, nil
	}
	count := 0
	for _, h := range hints {
		if e = ctx.Err(); e != nil {
			return count, e
		}
		r.mu.Lock()
		r.after = h.operation
		r.mu.Unlock()
		added, err := r.scheduleOne(ctx, h)
		if err != nil {
			if errors.Is(err, ports.ErrForbidden) || errors.Is(err, ports.ErrUnauthorized) || errors.Is(err, ports.ErrNotFound) || errors.Is(err, ports.ErrMovieUpgradeTrackingReview) || errors.Is(err, ports.ErrUpgradeReservationConflict) {
				continue
			}
			return count, err
		}
		if added {
			count++
		}
	}
	return count, nil
}
func (r *MovieUpgradeTrackingRuntimeStore) actor(ctx context.Context, tx pgx.Tx, h upgradeTrackingHint) (*ports.Actor, error) {
	return (&MovieUpgradeObservationRuntimeStore{store: r.store}).originalActor(ctx, tx, upgradeObservationHint{operation: h.operation, task: h.task, actor: h.actor})
}
func (r *MovieUpgradeTrackingRuntimeStore) scheduleOneWatched(ctx context.Context, h upgradeTrackingHint, watch *movieUpgradeWatchCapture) (added bool, err error) {
	work, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	defer func() {
		if watch != nil {
			err = movieUpgradeWatchError(ctx, err)
		} else {
			err = upgradeObservationError(ctx, err)
		}
	}()
	tx, err := r.store.pool.Begin(work)
	if err != nil {
		return false, err
	}
	defer upgradeRollback(work, tx)
	if err = r.lockMovieUpgradeWatchIsolation(work, tx, watch); err != nil {
		return false, err
	}
	actor, err := r.actor(work, tx, h)
	if err != nil {
		return false, err
	}
	s := &movieUpgradeTrackingSession{base: &movieUpgradeSendSession{store: r.store, operation: h.operation}}
	_, sub, verify, err := s.load(work, tx, actor)
	if err != nil {
		return false, err
	}
	if err = r.validateMovieUpgradeWatchCapture(work, tx, watch, uuid.Nil); err != nil {
		return false, err
	}
	// Never wait for a physical session while holding account/business locks.
	var held bool
	if err = tx.QueryRow(work, `SELECT pg_try_advisory_xact_lock(('x'||substr(encode(sha256(convert_to('pulsemedia.movie-upgrade.physical.v1:'||$1::uuid::text,'UTF8')),'hex'),1,16))::bit(64)::bigint)`, h.operation).Scan(&held); err != nil || !held {
		return false, err
	}
	var ready bool
	if err = tx.QueryRow(work, `SELECT `+upgradeTrackingReady+` AND tip.observation_id IS NOT DISTINCT FROM $2::uuid FROM movie_upgrade_preparation p `+upgradeTrackingTip+` WHERE p.operation_id=$1`, h.operation, h.prior).Scan(&ready); err != nil || !ready {
		return false, err
	}
	id := r.store.ids.NewUUID()
	suffix := "first"
	if h.prior != nil {
		suffix = h.prior.String()
	}
	dedupe := "movie-upgrade-tracking:" + h.operation.String() + ":" + suffix
	payload, err := json.Marshal(map[string]uuid.UUID{"tracking_job_id": id})
	if err != nil {
		return false, err
	}
	var expected, actual json.RawMessage
	err = tx.QueryRow(work, `WITH t AS(SELECT clock_timestamp() now) SELECT to_jsonb(jsonb_populate_record(NULL::job,jsonb_build_object('id',$1::uuid,'kind','movie_upgrade_tracking','dedupe_key',$2::text,'priority',0,'state','queued','payload',$3::jsonb,'attempts',0,'max_attempts',1,'lease_token',0,'not_before',now,'created_at',now,'updated_at',now))) FROM t`, id, dedupe, payload).Scan(&expected)
	if err != nil {
		return false, err
	}
	if err = tx.QueryRow(work, `INSERT INTO job SELECT * FROM jsonb_populate_record(NULL::job,$1::jsonb) RETURNING to_jsonb(job)`, expected).Scan(&actual); err != nil {
		return false, err
	}
	if !preparationSameJSON(expected, actual) {
		return false, preparationConflict()
	}
	var binding json.RawMessage
	if err = tx.QueryRow(work, `INSERT INTO movie_upgrade_tracking_job(job_id,operation_id,task_id,actor_id,prior_observation_id,queued_job) VALUES($1,$2,$3,$4,$5,$6) RETURNING to_jsonb(movie_upgrade_tracking_job)`, id, h.operation, h.task, h.actor, h.prior, expected).Scan(&binding); err != nil {
		return false, err
	}
	own := ownedUpgradeRows{}
	check := func() error {
		if _, _, e := r.store.authorizePreparation(work, tx, actor, h.operation); e != nil {
			return e
		}
		if e := verify(); e != nil {
			return e
		}
		if e := own.verify(work, tx); e != nil {
			return e
		}
		var same bool
		if e := tx.QueryRow(work, `SELECT to_jsonb(j)=$2::jsonb AND to_jsonb(b)=$3::jsonb FROM job j JOIN movie_upgrade_tracking_job b ON b.job_id=j.id WHERE j.id=$1 FOR SHARE OF j`, id, expected, binding).Scan(&same); e != nil {
			return e
		}
		if !same {
			return preparationConflict()
		}
		if e := r.validateMovieUpgradeWatchCapture(work, tx, watch, id); e != nil {
			return e
		}
		return nil
	}
	ledger := &UpgradeReservationStore{pool: r.store.pool, ids: r.store.ids}
	receipt := map[string]any{"job_id": id, "operation_id": h.operation, "task_id": h.task, "status": "queued", "execution_allowed": false}
	if err = ledger.upgradeSuccess(work, tx, actor, sub, "upgrade.tracking-job:"+id.String(), coverageDigest(json.RawMessage(binding)), "upgrade.tracking-job-queue", id, receipt, own, check); err != nil {
		return false, err
	}
	if watch != nil {
		if _, err = tx.Exec(work, "SET CONSTRAINTS ALL IMMEDIATE"); err != nil {
			return false, err
		}
	}
	if err = check(); err != nil {
		return false, err
	}
	if err = tx.Commit(work); err != nil {
		return false, err
	}
	return true, nil
}
func (r *MovieUpgradeTrackingRuntimeStore) MovieUpgradeTrackingClaim(ctx context.Context, lease ports.JobLease) (out *ports.Actor, req ports.MovieUpgradeTrackingJobRequest, err error) {
	defer func() { err = upgradeObservationError(ctx, err) }()
	if lease.ID == uuid.Nil || lease.Owner == "" || lease.Token <= 0 {
		return nil, req, preparationConflict()
	}
	tx, err := r.store.pool.Begin(ctx)
	if err != nil {
		return nil, req, err
	}
	defer upgradeRollback(ctx, tx)
	var h upgradeTrackingHint
	if err = tx.QueryRow(ctx, `SELECT operation_id,task_id,actor_id FROM movie_upgrade_tracking_job WHERE job_id=$1`, lease.ID).Scan(&h.operation, &h.task, &h.actor); err != nil {
		return nil, req, err
	}
	actor, err := r.actor(ctx, tx, h)
	if err != nil {
		return nil, req, err
	}
	req = ports.MovieUpgradeTrackingJobRequest{OperationID: h.operation, TaskID: h.task, Lease: lease}
	s := &movieUpgradeTrackingSession{base: &movieUpgradeSendSession{store: r.store, operation: h.operation}, job: &movieUpgradeTrackingJob{request: req}}
	if _, _, verify, e := s.load(ctx, tx, actor); e != nil {
		return nil, req, e
	} else if e = verify(); e != nil {
		return nil, req, e
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, req, err
	}
	return actor, req, nil
}
