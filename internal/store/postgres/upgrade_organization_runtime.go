package postgres

import (
	"context"
	"sync"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type MovieUpgradeOrganizationRuntimeStore struct {
	store *UpgradePreparationStore
	mu    sync.Mutex
	after uuid.UUID
}

func NewMovieUpgradeOrganizationRuntimeStore(s *UpgradePreparationStore) (*MovieUpgradeOrganizationRuntimeStore, error) {
	if s == nil {
		return nil, ports.ErrUnavailable
	}
	return &MovieUpgradeOrganizationRuntimeStore{store: s}, nil
}
func (r *MovieUpgradeOrganizationRuntimeStore) MovieUpgradeOrganizationClaim(ctx context.Context, l ports.JobLease) (actor *ports.Actor, err error) {
	if l.ID == uuid.Nil || l.Owner == "" || l.Token <= 0 {
		return nil, preparationConflict()
	}
	defer func() { err = upgradeError(ctx, err) }()
	work, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, e := r.store.pool.Begin(work)
	if e != nil {
		return nil, e
	}
	defer upgradeRollback(work, tx)
	var op, aid uuid.UUID
	if e = tx.QueryRow(work, `SELECT operation_id,actor_id FROM movie_upgrade_organization_job WHERE job_id=$1`, l.ID).Scan(&op, &aid); e != nil {
		return nil, e
	}
	actor = &ports.Actor{Type: ports.ActorTypeUser, ID: aid.String(), Role: ports.RoleAdmin}
	if _, _, e = r.store.authorizePreparation(work, tx, actor, op); e != nil {
		return nil, e
	}
	var id uuid.UUID
	if e = tx.QueryRow(work, `SELECT id FROM job WHERE id=$1 FOR UPDATE`, l.ID).Scan(&id); e != nil {
		return nil, e
	}
	var valid bool
	e = tx.QueryRow(work, `SELECT pulse_upgrade_organization_job_running(j,b) AND j.lease_owner=$2 AND j.lease_token=$3 AND j.lease_until>clock_timestamp() FROM job j JOIN movie_upgrade_organization_job b ON b.job_id=j.id WHERE j.id=$1`, l.ID, l.Owner, l.Token).Scan(&valid)
	if e != nil {
		return nil, e
	}
	if !valid {
		return nil, preparationConflict()
	}
	if e = tx.Commit(work); e != nil {
		return nil, e
	}
	return actor, nil
}

// The immutable linked result is the durable pending work. An interrupted
// ingestion remains discoverable; no physical execution or expired lease is replayed.
func (r *MovieUpgradeOrganizationRuntimeStore) PendingMovieUpgradeOrganizationIngestion(ctx context.Context) ([]ports.MovieUpgradeOrganizationIngestionWork, error) {
	r.mu.Lock()
	after := r.after
	r.mu.Unlock()
	rows, e := r.store.pool.Query(ctx, `SELECT b.job_id,b.actor_id FROM movie_upgrade_organization_job b JOIN movie_upgrade_organization_execution_result result USING(job_id) WHERE b.job_id>$1 AND result.status='linked_pending_ingestion' AND NOT EXISTS(SELECT 1 FROM movie_upgrade_organization_ingestion i WHERE i.job_id=b.job_id) ORDER BY b.job_id LIMIT 25`, after)
	if e != nil {
		return nil, upgradeError(ctx, e)
	}
	defer rows.Close()
	var out []ports.MovieUpgradeOrganizationIngestionWork
	for rows.Next() {
		var job, actor uuid.UUID
		if e = rows.Scan(&job, &actor); e != nil {
			return nil, e
		}
		out = append(out, ports.MovieUpgradeOrganizationIngestionWork{JobID: job, Actor: ports.Actor{ID: actor.String(), Type: ports.ActorTypeUser, Role: ports.RoleAdmin}})
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	r.mu.Lock()
	if r.after == after {
		if len(out) == 0 {
			r.after = uuid.Nil
		} else {
			r.after = out[len(out)-1].JobID
		}
	}
	r.mu.Unlock()
	return out, nil
}

var _ ports.MovieUpgradeOrganizationRuntime = (*MovieUpgradeOrganizationRuntimeStore)(nil)
