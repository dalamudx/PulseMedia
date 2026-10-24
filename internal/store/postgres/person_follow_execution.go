package postgres

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PersonFollowExecution keeps the physical session until the caller's I/O has
// returned. Its release evidence is independent of logical lease validity.
type PersonFollowExecution struct {
	mu          sync.Mutex
	store       *PersonFollowSyncStore
	conn        *pgxpool.Conn
	lease       ports.JobLease
	configID    uuid.UUID
	runID       uuid.UUID
	ownerID     uuid.UUID
	executionID uuid.UUID
	pid         int32
	work        ports.PersonFollowSyncWork
	closed      bool
}

func personFollowUnlockConnection(conn *pgxpool.Conn, config uuid.UUID) {
	cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	unlocked, err := db.New(conn).UnlockPersonFollowPhysicalLock(cleanup, config)
	if err != nil || !unlocked {
		_ = conn.Hijack().Close(cleanup)
		return
	}
	conn.Release()
}
func (s *PersonFollowSyncStore) BeginExecution(ctx context.Context, lease ports.JobLease) (result *PersonFollowExecution, err error) {
	defer func() { err = personFollowError(ctx, err) }()
	if lease.ID == uuid.Nil || lease.Owner == "" || lease.Token <= 0 {
		return nil, ports.ErrRejected
	}
	if s == nil || s.pool == nil || s.ids == nil {
		return nil, ports.ErrUnavailable
	}
	bounded, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	hint, e := db.New(s.pool).GetPersonFollowDispatch(bounded, lease.ID)
	if e != nil {
		return nil, e
	}
	conn, e := s.pool.Acquire(bounded)
	if e != nil {
		return nil, e
	}
	held, e := db.New(conn).TryPersonFollowPhysicalLock(bounded, hint.ConfigID)
	if e != nil || !held {
		if e != nil {
			cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
			_ = conn.Hijack().Close(cleanup)
			stop()
		} else {
			conn.Release()
		}
		if e == nil {
			e = ports.ErrPersonFollowConflict
		}
		return nil, e
	}
	keep := false
	defer func() {
		if !keep {
			personFollowUnlockConnection(conn, hint.ConfigID)
		}
	}()
	pid, e := db.New(conn).PersonFollowBackendPID(bounded)
	if e != nil {
		return nil, e
	}
	execution := &PersonFollowExecution{store: s, conn: conn, lease: lease, configID: hint.ConfigID, runID: hint.RunID, ownerID: hint.OwnerID, pid: pid}
	tx, e := conn.Begin(bounded)
	if e != nil {
		return nil, e
	}
	defer managementRollback(bounded, tx)
	q := db.New(tx)
	work, e := execution.authorize(bounded, q)
	if e != nil {
		return nil, e
	}
	unreleased, e := q.PersonFollowUnreleasedExecution(bounded, lease.ID)
	if e != nil {
		return nil, e
	}
	if unreleased {
		return nil, ports.ErrPersonFollowConflict
	}
	id := s.ids.NewUUID()
	if e = q.InsertPersonFollowExecution(bounded, db.InsertPersonFollowExecutionParams{ID: id, JobID: lease.ID, RunID: hint.RunID, LeaseOwner: lease.Owner, LeaseToken: lease.Token, BackendPid: pid}); e != nil {
		return nil, e
	}
	if e = q.StartPersonFollowRun(bounded, hint.RunID); e != nil {
		return nil, e
	}
	if e = personFollowExecutionLease(bounded, q, lease); e != nil {
		return nil, e
	}
	if e = tx.Commit(bounded); e != nil {
		return nil, e
	}
	execution.executionID = id
	work.ExecutionID = id
	execution.work = work
	keep = true
	return execution, nil
}
func personFollowExecutionLease(ctx context.Context, q *db.Queries, lease ports.JobLease) error {
	valid, err := q.PersonFollowLeaseValid(ctx, db.PersonFollowLeaseValidParams{ID: lease.ID, Owner: lease.Owner, Token: lease.Token})
	if err != nil {
		return err
	}
	if !valid.Valid || !valid.Bool {
		return ports.ErrPersonFollowConflict
	}
	return nil
}
func (p *PersonFollowExecution) authorize(ctx context.Context, q *db.Queries) (ports.PersonFollowSyncWork, error) {
	var work ports.PersonFollowSyncWork
	owner, err := q.LockPersonFollowOwner(ctx, p.ownerID)
	if err != nil {
		return work, err
	}
	if !owner.IsActive || (owner.Role != ports.RoleMember && owner.Role != ports.RoleAdmin) {
		return work, ports.ErrForbidden
	}
	isolated, err := q.GuardDoubanSyncIsolation(ctx)
	if err != nil {
		return work, err
	}
	if isolated {
		return work, ports.ErrUnavailable
	}
	config, err := q.LockPersonFollowConfig(ctx, p.configID)
	if err != nil {
		return work, err
	}
	run, err := q.LockPersonFollowRun(ctx, p.runID)
	if err != nil {
		return work, err
	}
	job, err := q.LockPersonFollowJob(ctx, p.lease.ID)
	if err != nil {
		return work, err
	}
	page, err := q.LockPersonFollowPage(ctx, p.lease.ID)
	if err != nil {
		return work, err
	}
	invalid := ports.ErrPersonFollowConflict
	if run.ConfigID != config.ID || run.OwnerID != p.ownerID || config.OwnerID != p.ownerID || !config.Enabled || config.Version != run.ConfigVersion || (run.Status != "queued" && run.Status != "running") || page.RunID != run.ID || page.Finished || job.Kind != ports.PersonFollowSyncJobKind || job.State != "running" || !job.LeaseOwner.Valid || job.LeaseOwner.String != p.lease.Owner || job.LeaseToken != p.lease.Token {
		return work, invalid
	}
	var payload personFollowPayload
	if !creationStrictJSON(job.Payload, &payload) || payload.Version != 1 || payload.RunID != run.ID || payload.Start != int(page.PageStart) {
		return work, invalid
	}
	if err = personFollowExecutionLease(ctx, q, p.lease); err != nil {
		return work, err
	}
	held, err := q.PersonFollowPhysicalLockHeld(ctx, db.PersonFollowPhysicalLockHeldParams{ConfigID: config.ID, BackendPid: p.pid})
	if err != nil {
		return work, err
	}
	if !held {
		return work, invalid
	}
	current, err := personFollowSnapshot(ctx, q, config)
	if err != nil {
		return work, err
	}
	var frozen ports.PersonFollowConfig
	if !creationStrictJSON(run.ConfigSnapshot, &frozen) || !personFollowSameConfig(current, frozen) {
		return work, invalid
	}
	history, err := q.ReadPersonFollowVersion(ctx, db.ReadPersonFollowVersionParams{ConfigID: config.ID, Version: config.Version})
	if err != nil {
		return work, err
	}
	if !creationStrictJSON(history.Snapshot, &current) || !personFollowSameConfig(current, frozen) {
		return work, invalid
	}
	actor := ports.Actor{Type: ports.ActorTypeUser, ID: p.ownerID.String(), Role: owner.Role}
	if err = personFollowTargets(ctx, q, frozen.Settings); err != nil {
		return work, err
	}
	if p.executionID != uuid.Nil {
		evidence, e := q.ReadPersonFollowExecution(ctx, p.executionID)
		if e != nil {
			return work, e
		}
		if evidence.JobID != p.lease.ID || evidence.RunID != p.runID || evidence.LeaseOwner != p.lease.Owner || evidence.LeaseToken != p.lease.Token || evidence.BackendPid != p.pid || evidence.ReleasedAt.Valid {
			return work, invalid
		}
		if actor.Role != p.work.Actor.Role {
			return work, ports.ErrForbidden
		}
	}
	work = ports.PersonFollowSyncWork{RunID: run.ID, ExecutionID: p.executionID, Config: frozen, Actor: actor, Start: int(page.PageStart)}
	return work, nil
}
func (p *PersonFollowExecution) Snapshot() ports.PersonFollowSyncWork {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := p.work
	result.Config.Settings = result.Config.Settings.Clone()
	return result
}
func (p *PersonFollowExecution) Check(ctx context.Context) (err error) {
	defer func() { err = personFollowError(ctx, err) }()
	if p == nil {
		return ports.ErrUnavailable
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return ports.ErrPersonFollowConflict
	}
	tx, e := p.conn.Begin(ctx)
	if e != nil {
		return e
	}
	defer managementRollback(ctx, tx)
	q := db.New(tx)
	if _, e = p.authorize(ctx, q); e != nil {
		return e
	}
	if e = personFollowExecutionLease(ctx, q, p.lease); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (p *PersonFollowExecution) Close() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	p.closed = true
	cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	count, err := db.New(p.conn).ReleasePersonFollowExecution(cleanup, p.executionID)
	if err != nil || count != 1 {
		_ = p.conn.Hijack().Close(cleanup)
		return
	}
	personFollowUnlockConnection(p.conn, p.configID)
}

// Used by recovery before another lease may execute the same persisted page.
func (s *PersonFollowSyncStore) ExecutionReleased(ctx context.Context, job uuid.UUID) (bool, error) {
	if s == nil || s.pool == nil || job == uuid.Nil {
		return false, ports.ErrUnavailable
	}
	row, err := db.New(s.pool).GetPersonFollowDispatch(ctx, job)
	if err != nil {
		return false, personFollowError(ctx, err)
	}
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return false, personFollowError(ctx, err)
	}
	held, err := db.New(conn).TryPersonFollowPhysicalLock(ctx, row.ConfigID)
	if err != nil || !held {
		if err != nil {
			cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
			_ = conn.Hijack().Close(cleanup)
			stop()
		} else {
			conn.Release()
		}
		return false, personFollowError(ctx, err)
	}
	defer personFollowUnlockConnection(conn, row.ConfigID)
	unreleased, err := db.New(conn).PersonFollowUnreleasedExecution(ctx, job)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ports.ErrUnavailable
	}
	if err != nil {
		return false, personFollowError(ctx, err)
	}
	return !unreleased, nil
}
