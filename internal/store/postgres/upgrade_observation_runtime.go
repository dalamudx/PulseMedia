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

type MovieUpgradeObservationRuntimeStore struct {
	store *UpgradePreparationStore
	mu    sync.Mutex
	after uuid.UUID
}

var _ ports.MovieUpgradeObservationRuntime = (*MovieUpgradeObservationRuntimeStore)(nil)

func NewMovieUpgradeObservationRuntimeStore(store *UpgradePreparationStore) (*MovieUpgradeObservationRuntimeStore, error) {
	if store == nil {
		return nil, ports.ErrUnavailable
	}
	return &MovieUpgradeObservationRuntimeStore{store: store}, nil
}

type upgradeObservationHint struct{ operation, task, actor uuid.UUID }

// The cursor is only a fairness hint. PostgreSQL operation locks and all-state
// history checks provide the persistent, cross-instance once-only guarantee.
func (r *MovieUpgradeObservationRuntimeStore) ScheduleMovieUpgradeObservations(ctx context.Context) (int, error) {
	r.mu.Lock()
	after := r.after
	r.mu.Unlock()
	rows, err := r.store.pool.Query(ctx, `SELECT r.operation_id,p.task_id,p.actor_id FROM movie_upgrade_send_result r JOIN movie_upgrade_preparation p USING(operation_id) WHERE r.status='unknown' AND r.operation_id>$1 AND NOT EXISTS(SELECT 1 FROM movie_upgrade_observation o WHERE o.operation_id=r.operation_id) AND NOT EXISTS(SELECT 1 FROM job j WHERE j.kind='movie_upgrade_observation' AND (j.payload->>'operation_id'=r.operation_id::text OR j.dedupe_key='movie-upgrade-observation:'||r.operation_id::text)) ORDER BY r.operation_id LIMIT 25`, after)
	if err != nil {
		return 0, upgradeObservationError(ctx, err)
	}
	var hints []upgradeObservationHint
	for rows.Next() {
		var h upgradeObservationHint
		if err = rows.Scan(&h.operation, &h.task, &h.actor); err != nil {
			rows.Close()
			return 0, upgradeObservationError(ctx, err)
		}
		hints = append(hints, h)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, upgradeObservationError(ctx, err)
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
		if err = ctx.Err(); err != nil {
			return count, err
		}
		r.mu.Lock()
		r.after = h.operation
		r.mu.Unlock()
		added, e := r.scheduleOne(ctx, h)
		if e != nil {
			if errors.Is(e, ports.ErrForbidden) || errors.Is(e, ports.ErrUnauthorized) || errors.Is(e, ports.ErrNotFound) || errors.Is(e, ports.ErrMovieUpgradeObservationReview) || errors.Is(e, ports.ErrUpgradeReservationConflict) {
				continue
			}
			return count, e
		}
		if added {
			count++
		}
	}
	return count, nil
}

func (r *MovieUpgradeObservationRuntimeStore) originalActor(ctx context.Context, tx pgx.Tx, h upgradeObservationHint) (*ports.Actor, error) {
	var role string
	err := tx.QueryRow(ctx, `SELECT u.role FROM movie_upgrade_preparation p JOIN app_user u ON u.id=p.actor_id WHERE p.operation_id=$1 AND p.task_id=$2 AND p.actor_id=$3`, h.operation, h.task, h.actor).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ports.ErrForbidden
	}
	if err != nil {
		return nil, err
	}
	if role != ports.RoleAdmin && role != ports.RoleMember {
		return nil, ports.ErrForbidden
	}
	return &ports.Actor{Type: ports.ActorTypeUser, ID: h.actor.String(), Role: role}, nil
}

func (r *MovieUpgradeObservationRuntimeStore) scheduleOne(ctx context.Context, h upgradeObservationHint) (added bool, err error) {
	work, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	defer func() { err = upgradeObservationError(ctx, err) }()
	tx, err := r.store.pool.Begin(work)
	if err != nil {
		return false, err
	}
	defer upgradeRollback(work, tx)
	actor, err := r.originalActor(work, tx, h)
	if err != nil {
		return false, err
	}
	session := &movieUpgradeObservationSession{base: &movieUpgradeSendSession{store: r.store, operation: h.operation}}
	_, _, verify, err := session.load(work, tx, actor)
	if err != nil {
		return false, err
	}
	var eligible bool
	err = tx.QueryRow(work, `SELECT NOT EXISTS(SELECT 1 FROM movie_upgrade_observation WHERE operation_id=$1) AND NOT EXISTS(SELECT 1 FROM movie_upgrade_observation_permit WHERE operation_id=$1) AND NOT EXISTS(SELECT 1 FROM job WHERE kind='movie_upgrade_observation' AND (payload->>'operation_id'=$1::uuid::text OR dedupe_key='movie-upgrade-observation:'||$1::uuid::text))`, h.operation).Scan(&eligible)
	if err != nil || !eligible {
		return false, err
	}
	id := r.store.ids.NewUUID()
	payload, err := json.Marshal(map[string]string{"operation_id": h.operation.String(), "task_id": h.task.String(), "actor_id": h.actor.String()})
	if err != nil {
		return false, err
	}
	key := "movie-upgrade-observation:" + h.operation.String()
	var now time.Time
	if err = tx.QueryRow(work, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return false, err
	}
	var expected json.RawMessage
	err = tx.QueryRow(work, `SELECT jsonb_build_object('id',$1::uuid,'kind','movie_upgrade_observation','dedupe_key',$2::text,'priority',0,'state','queued','payload',$3::jsonb,'result',NULL,'error',NULL,'attempts',0,'max_attempts',1,'not_before',$4::timestamptz,'lease_owner',NULL,'lease_token',0,'lease_until',NULL,'created_at',$4::timestamptz,'updated_at',$4::timestamptz,'finished_at',NULL)`, id, key, payload, now).Scan(&expected)
	if err != nil {
		return false, err
	}
	if _, err = tx.Exec(work, `INSERT INTO job(id,kind,dedupe_key,state,payload,max_attempts,not_before,created_at,updated_at) VALUES($1,'movie_upgrade_observation',$2,'queued',$3,1,$4,$4,$4)`, id, key, payload, now); err != nil {
		return false, err
	}
	check := func() error {
		if _, _, e := r.store.authorizePreparation(work, tx, actor, h.operation); e != nil {
			return e
		}
		if e := verify(); e != nil {
			return e
		}
		var actual json.RawMessage
		if e := tx.QueryRow(work, `SELECT to_jsonb(j) FROM job j WHERE id=$1 FOR SHARE`, id).Scan(&actual); e != nil {
			return e
		}
		if !preparationSameJSON(expected, actual) {
			return preparationConflict()
		}
		var n int
		if e := tx.QueryRow(work, `SELECT count(*) FROM job WHERE kind='movie_upgrade_observation' AND (payload->>'operation_id'=$1::uuid::text OR dedupe_key='movie-upgrade-observation:'||$1::uuid::text)`, h.operation).Scan(&n); e != nil {
			return e
		}
		if n != 1 {
			return preparationConflict()
		}
		return nil
	}
	if err = check(); err != nil {
		return false, err
	}
	if _, err = tx.Exec(work, `SET CONSTRAINTS ALL IMMEDIATE`); err != nil {
		return false, err
	}
	if err = check(); err != nil {
		return false, err
	}
	if err = tx.Commit(work); err != nil {
		return false, err
	}
	return true, nil
}

func (r *MovieUpgradeObservationRuntimeStore) MovieUpgradeObservationActor(ctx context.Context, req ports.MovieUpgradeObservationJobRequest, actorID uuid.UUID) (actor *ports.Actor, err error) {
	if actorID == uuid.Nil || req.OperationID == uuid.Nil || req.TaskID == uuid.Nil {
		return nil, preparationConflict()
	}
	work, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	defer func() { err = upgradeObservationError(ctx, err) }()
	tx, err := r.store.pool.Begin(work)
	if err != nil {
		return nil, err
	}
	defer upgradeRollback(work, tx)
	actor, err = r.originalActor(work, tx, upgradeObservationHint{req.OperationID, req.TaskID, actorID})
	if err != nil {
		return nil, err
	}
	session := &movieUpgradeObservationSession{base: &movieUpgradeSendSession{store: r.store, operation: req.OperationID}, job: &movieUpgradeObservationJob{request: req}}
	_, _, verify, err := session.load(work, tx, actor)
	if err != nil {
		return nil, err
	}
	if err = verify(); err != nil {
		return nil, err
	}
	if err = tx.Commit(work); err != nil {
		return nil, err
	}
	return actor, nil
}
