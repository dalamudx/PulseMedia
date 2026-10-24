package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"sync"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type UntrackedDiscoveryStore struct {
	pool      *pgxpool.Pool
	ids       ports.IDGen
	clock     ports.Clock
	cursorKey uuid.UUID
	dueMu     sync.Mutex
	dueAfter  uuid.UUID
}

var (
	_ ports.UntrackedDiscoveryScheduler  = (*UntrackedDiscoveryStore)(nil)
	_ ports.UntrackedDiscoveryRepository = (*UntrackedDiscoveryStore)(nil)
	_ ports.UntrackedDiscoveryReader     = (*UntrackedDiscoveryStore)(nil)
)

func NewUntrackedDiscoveryStore(pool *pgxpool.Pool, ids ports.IDGen, clock ports.Clock) *UntrackedDiscoveryStore {
	s := &UntrackedDiscoveryStore{pool: pool, ids: ids, clock: clock}
	if ids != nil {
		s.cursorKey = ids.NewUUID()
	}
	return s
}

func discoveryGetState(ctx context.Context, q *db.Queries, id uuid.UUID, lock bool) (untrackedDiscoveryState, error) {
	var raw []byte
	var err error
	if lock {
		raw, err = q.LockUntrackedDiscoveryState(ctx, id)
	} else {
		raw, err = q.GetUntrackedDiscoveryState(ctx, id)
	}
	if err != nil {
		return untrackedDiscoveryState{}, err
	}
	return discoveryState(raw)
}

func discoveryActiveJob(ctx context.Context, q *db.Queries, d untrackedDiscoveryState) (*db.Job, error) {
	if d.ActiveJobID == nil {
		return nil, nil
	}
	if _, err := q.LockJobForWrite(ctx, *d.ActiveJobID); err != nil {
		return nil, err
	}
	j, err := q.GetJobByID(ctx, *d.ActiveJobID)
	if err != nil {
		return nil, err
	}
	p, err := discoveryPayload(j.Payload)
	if err != nil {
		return nil, err
	}
	if j.Kind != ports.UntrackedDiscoveryJobKind || j.MaxAttempts != 3 || j.Attempts < 0 || j.Attempts > 3 || p.ScopeID != d.ScopeID || p.ScopeVersion != d.ScheduledScopeVersion || p.Sequence != d.ActiveSequence {
		return nil, ports.ErrUntrackedDiscoveryStale
	}
	return &j, nil
}

func (s *UntrackedDiscoveryStore) newJob(ctx context.Context, q *db.Queries, d untrackedDiscoveryState) (uuid.UUID, error) {
	if d.RequestedSequence <= d.CompletedSequence || d.RequestedSequence <= 0 {
		return uuid.Nil, ports.ErrUntrackedDiscoveryStale
	}
	id := s.ids.NewUUID()
	if id == uuid.Nil {
		return id, ports.ErrUnavailable
	}
	payload, err := json.Marshal(untrackedDiscoveryPayload{ScopeID: d.ScopeID, ScopeVersion: d.RequestedVersion, Sequence: d.RequestedSequence})
	if err != nil {
		return uuid.Nil, err
	}
	if err = q.CreateUntrackedDiscoveryJob(ctx, db.CreateUntrackedDiscoveryJobParams{ID: id, Payload: payload}); err != nil {
		return uuid.Nil, err
	}
	if err = q.SetUntrackedDiscoveryActive(ctx, db.SetUntrackedDiscoveryActiveParams{ScopeID: d.ScopeID, JobID: id}); err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

// Seals include scheduling state and the complete active job, not telemetry dates.
func discoveryScheduleSeal(ctx context.Context, q *db.Queries, id uuid.UUID) (untrackedDiscoveryState, *db.Job, error) {
	d, err := discoveryGetState(ctx, q, id, false)
	if err != nil {
		return d, nil, err
	}
	j, err := discoveryActiveJob(ctx, q, d)
	return d, j, err
}

func (s *UntrackedDiscoveryStore) schedule(ctx context.Context, id uuid.UUID, version int64, event bool) (*ports.UntrackedDiscoveryScheduleResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer managementRollback(ctx, tx)
	q := db.New(tx)
	owner, err := q.UntrackedDiscoveryScopeOwner(ctx, id)
	if err != nil {
		return nil, err
	}
	c, err := s.authority(ctx, tx, id, owner, true)
	if err != nil {
		return nil, err
	}
	if event && c.Version != version {
		return nil, ports.ErrUntrackedDiscoveryStale
	}
	if err = q.EnsureUntrackedDiscoveryState(ctx, db.EnsureUntrackedDiscoveryStateParams{ScopeID: id, Version: c.Version}); err != nil {
		return nil, err
	}
	d, err := discoveryGetState(ctx, q, id, true)
	if err != nil {
		return nil, err
	}
	if event || d.RequestedVersion != c.Version {
		if err = q.RequestUntrackedDiscoverySequence(ctx, db.RequestUntrackedDiscoverySequenceParams{ScopeID: id, Version: c.Version}); err != nil {
			return nil, err
		}
		d, err = discoveryGetState(ctx, q, id, false)
		if err != nil {
			return nil, err
		}
	}
	j, err := discoveryActiveJob(ctx, q, d)
	if err != nil {
		return nil, err
	}
	out := &ports.UntrackedDiscoveryScheduleResult{}
	if j != nil && j.State == "running" {
		n, e := q.RecoverUntrackedDiscoveryJob(ctx, j.ID)
		if e != nil {
			return nil, e
		}
		if n == 1 {
			j, err = discoveryActiveJob(ctx, q, d)
			if err != nil {
				return nil, err
			}
			out.Queued = j.State == "queued"
		}
	}
	if j != nil && (j.State == "queued" || j.State == "running") {
		out.JobID = j.ID
	} else {
		if j != nil {
			if err = q.FinishUntrackedDiscoveryState(ctx, id); err != nil {
				return nil, err
			}
			d, err = discoveryGetState(ctx, q, id, false)
			if err != nil {
				return nil, err
			}
		}
		now, e := q.UntrackedDiscoveryClock(ctx)
		if e != nil {
			return nil, e
		}
		if d.RequestedSequence > d.CompletedSequence || !now.Time.Before(d.NextSweepAt) {
			if d.RequestedSequence == d.CompletedSequence {
				if err = q.RequestUntrackedDiscoverySequence(ctx, db.RequestUntrackedDiscoverySequenceParams{ScopeID: id, Version: c.Version}); err != nil {
					return nil, err
				}
				d, err = discoveryGetState(ctx, q, id, false)
				if err != nil {
					return nil, err
				}
			}
			out.JobID, err = s.newJob(ctx, q, d)
			if err != nil {
				return nil, err
			}
			out.Queued = true
		}
	}
	before, beforeJob, err := discoveryScheduleSeal(ctx, q, id)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`); err != nil {
		return nil, err
	}
	if err = s.recheck(ctx, tx, *c); err != nil {
		return nil, err
	}
	after, afterJob, err := discoveryScheduleSeal(ctx, q, id)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(beforeJob, afterJob) {
		return nil, ports.ErrUntrackedDiscoveryStale
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *UntrackedDiscoveryStore) RequestUntrackedDiscovery(ctx context.Context, id uuid.UUID, version int64) (out *ports.UntrackedDiscoveryScheduleResult, err error) {
	defer func() { err = discoveryError(ctx, err) }()
	if !s.ready() {
		return nil, ports.ErrUnavailable
	}
	if id == uuid.Nil || version < 1 {
		return nil, ports.ErrRejected
	}
	return s.schedule(ctx, id, version, true)
}

func (s *UntrackedDiscoveryStore) QueueDueUntrackedDiscoveries(ctx context.Context, limit int) (count int, err error) {
	defer func() { err = discoveryError(ctx, err) }()
	if !s.ready() {
		return 0, ports.ErrUnavailable
	}
	n, err := discoveryLimit(limit)
	if err != nil {
		return 0, err
	}
	if err = ctx.Err(); err != nil {
		return 0, err
	}
	// Another scheduler is already advancing this instance; do not wait while
	// holding any business lock or make cancellation wait for its transaction.
	if !s.dueMu.TryLock() {
		return 0, nil
	}
	defer s.dueMu.Unlock()
	q := db.New(s.pool)
	ids, err := q.ListUntrackedDiscoveryDue(ctx, db.ListUntrackedDiscoveryDueParams{AfterID: s.dueAfter, PageSize: int32(n)})
	if err != nil {
		return 0, err
	}
	if len(ids) == 0 && s.dueAfter != uuid.Nil {
		s.dueAfter = uuid.Nil
		ids, err = q.ListUntrackedDiscoveryDue(ctx, db.ListUntrackedDiscoveryDueParams{AfterID: s.dueAfter, PageSize: int32(n)})
		if err != nil {
			return 0, err
		}
	}
	for _, id := range ids {
		// Rejected scopes must not starve later valid work. The request/sequence
		// state is durable; only this bounded scheduling traversal is local.
		s.dueAfter = id
		r, e := s.schedule(ctx, id, 0, false)
		if e != nil {
			if ctx.Err() != nil {
				return count, ctx.Err()
			}
			if discoverySkippable(discoveryError(ctx, e)) {
				continue
			}
			return count, e
		}
		if r.Queued {
			count++
		}
	}
	return count, nil
}

func (s *UntrackedDiscoveryStore) capture(in ports.UntrackedDiscoveryCapture) (*untrackedDiscoveryCapture, error) {
	c, ok := in.(*untrackedDiscoveryCapture)
	if !ok || c == nil || c.store != s || !s.ready() {
		return nil, ports.ErrRejected
	}
	return c, nil
}

func (s *UntrackedDiscoveryStore) execution(ctx context.Context, tx pgx.Tx, c *untrackedDiscoveryCapture) (untrackedDiscoveryState, db.Job, error) {
	x := c.snapshot
	q := db.New(tx)
	current, err := s.authority(ctx, tx, x.Scope.ID, x.Scope.OwnerID, true)
	if err != nil {
		return untrackedDiscoveryState{}, db.Job{}, err
	}
	if !untrackedSame(*current, x.Scope) {
		return untrackedDiscoveryState{}, db.Job{}, ports.ErrUntrackedDiscoveryStale
	}
	d, err := discoveryGetState(ctx, q, x.Scope.ID, true)
	if err != nil {
		return d, db.Job{}, err
	}
	if d.ActiveJobID == nil || *d.ActiveJobID != x.Lease.ID || d.ActiveSequence != x.Sequence || d.ScheduledScopeVersion != x.Scope.Version || d.RequestedVersion != x.Scope.Version {
		return d, db.Job{}, ports.ErrUntrackedDiscoveryStale
	}
	if _, err = q.LockJobForWrite(ctx, x.Lease.ID); err != nil {
		return d, db.Job{}, err
	}
	j, err := q.GetAuthorizedSearchJob(ctx, checkinLease(x.Lease, ports.UntrackedDiscoveryJobKind))
	if err != nil {
		return d, j, err
	}
	p, err := discoveryPayload(j.Payload)
	if err != nil {
		return d, j, err
	}
	if j.MaxAttempts != 3 || j.Attempts < 1 || j.Attempts > 3 || p.ScopeID != x.Scope.ID || p.ScopeVersion != x.Scope.Version || p.Sequence != x.Sequence {
		return d, j, ports.ErrUntrackedDiscoveryStale
	}
	return d, j, nil
}

func (s *UntrackedDiscoveryStore) LoadUntrackedDiscovery(ctx context.Context, lease ports.JobLease) (out ports.UntrackedDiscoveryCapture, err error) {
	defer func() { err = discoveryError(ctx, err) }()
	if !s.ready() {
		return nil, ports.ErrUnavailable
	}
	if lease.ID == uuid.Nil || lease.Owner == "" || lease.Token < 1 {
		return nil, ports.ErrRejected
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer managementRollback(ctx, tx)
	q := db.New(tx)
	// Read payload only with valid running authority, before taking business locks.
	j, err := q.GetAuthorizedSearchJob(ctx, checkinLease(lease, ports.UntrackedDiscoveryJobKind))
	if err != nil {
		return nil, err
	}
	p, err := discoveryPayload(j.Payload)
	if err != nil {
		return nil, err
	}
	owner, err := q.UntrackedDiscoveryScopeOwner(ctx, p.ScopeID)
	if err != nil {
		return nil, err
	}
	scope, err := s.authority(ctx, tx, p.ScopeID, owner, true)
	if err != nil {
		return nil, err
	}
	if scope.Version != p.ScopeVersion {
		return nil, ports.ErrUntrackedDiscoveryStale
	}
	c := &untrackedDiscoveryCapture{store: s, snapshot: ports.UntrackedDiscoverySnapshot{Lease: lease, Scope: *scope, Sequence: p.Sequence}}
	if _, _, err = s.execution(ctx, tx, c); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return c, nil
}

func (s *UntrackedDiscoveryStore) ValidateUntrackedDiscovery(ctx context.Context, in ports.UntrackedDiscoveryCapture) (err error) {
	defer func() { err = discoveryError(ctx, err) }()
	c, err := s.capture(in)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer managementRollback(ctx, tx)
	if _, _, err = s.execution(ctx, tx, c); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *UntrackedDiscoveryStore) RecordUntrackedDiscovery(ctx context.Context, in ports.UntrackedDiscoveryCapture, observations []ports.UntrackedFileObservation) (err error) {
	defer func() { err = discoveryError(ctx, err) }()
	c, err := s.capture(in)
	if err != nil {
		return err
	}
	if len(observations) > 128 {
		return ports.ErrRejected
	}
	seen := make(map[string]struct{}, len(observations))
	batch := append([]ports.UntrackedFileObservation(nil), observations...)
	for _, o := range batch {
		if !discoveryObservationValid(o) {
			return ports.ErrRejected
		}
		if _, ok := seen[o.RelativePath]; ok {
			return ports.ErrRejected
		}
		seen[o.RelativePath] = struct{}{}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer managementRollback(ctx, tx)
	q := db.New(tx)
	beforeState, beforeJob, err := s.execution(ctx, tx, c)
	if err != nil {
		return err
	}
	x := c.snapshot
	for _, o := range batch {
		if err = q.RecordUntrackedDiscoveryFile(ctx, db.RecordUntrackedDiscoveryFileParams{ID: s.ids.NewUUID(), ScopeID: x.Scope.ID, ScopeVersion: x.Scope.Version, RelativePath: o.RelativePath, Device: o.Identity.Device, Inode: o.Identity.Inode, Size: o.Identity.Size, Mtime: o.Identity.Mtime, JobID: x.Lease.ID, Token: x.Lease.Token}); err != nil {
			return err
		}
	}
	paths := make([]string, len(batch))
	for i, o := range batch {
		paths[i] = o.RelativePath
	}
	sealParams := db.UntrackedDiscoveryBatchSealParams{ScopeID: x.Scope.ID, JobID: x.Lease.ID, Token: x.Lease.Token, Paths: paths}
	before, err := q.UntrackedDiscoveryBatchSeal(ctx, sealParams)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`); err != nil {
		return err
	}
	afterState, afterJob, err := s.execution(ctx, tx, c)
	if err != nil {
		return err
	}
	after, err := q.UntrackedDiscoveryBatchSeal(ctx, sealParams)
	if err != nil {
		return err
	}
	if !bytes.Equal(before, after) || !reflect.DeepEqual(beforeState, afterState) || !reflect.DeepEqual(beforeJob, afterJob) {
		return ports.ErrUntrackedDiscoveryStale
	}
	return tx.Commit(ctx)
}

func (s *UntrackedDiscoveryStore) CompleteUntrackedDiscovery(ctx context.Context, in ports.UntrackedDiscoveryCapture) (err error) {
	defer func() { err = discoveryError(ctx, err) }()
	c, err := s.capture(in)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer managementRollback(ctx, tx)
	q := db.New(tx)
	d, _, err := s.execution(ctx, tx, c)
	if err != nil {
		return err
	}
	x := c.snapshot
	n, err := q.FinishUntrackedDiscoveryJob(ctx, db.FinishUntrackedDiscoveryJobParams{ID: x.Lease.ID, Owner: x.Lease.Owner, Token: x.Lease.Token})
	if err != nil {
		return err
	}
	if n != 1 {
		return ports.ErrUntrackedDiscoveryStale
	}
	if err = q.FinishUntrackedDiscoveryState(ctx, x.Scope.ID); err != nil {
		return err
	}
	if d.RequestedSequence > x.Sequence {
		d, err = discoveryGetState(ctx, q, x.Scope.ID, false)
		if err != nil {
			return err
		}
		if _, err = s.newJob(ctx, q, d); err != nil {
			return err
		}
	}
	before, beforeJob, err := discoveryScheduleSeal(ctx, q, x.Scope.ID)
	if err != nil {
		return err
	}
	finished, err := q.GetJobByID(ctx, x.Lease.ID)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`); err != nil {
		return err
	}
	if err = s.recheck(ctx, tx, x.Scope); err != nil {
		return err
	}
	after, afterJob, err := discoveryScheduleSeal(ctx, q, x.Scope.ID)
	if err != nil {
		return err
	}
	final, err := q.GetJobByID(ctx, x.Lease.ID)
	if err != nil {
		return err
	}
	valid, err := q.CheckUntrackedDiscoveryFinishedLease(ctx, db.CheckUntrackedDiscoveryFinishedLeaseParams{ID: x.Lease.ID, Owner: x.Lease.Owner, Token: x.Lease.Token, Payload: finished.Payload})
	if err != nil {
		return err
	}
	if !valid || !reflect.DeepEqual(finished, final) || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(beforeJob, afterJob) {
		return ports.ErrUntrackedDiscoveryStale
	}
	return tx.Commit(ctx)
}
