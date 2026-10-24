package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	rule "github.com/dalamudx/PulseMedia/internal/domain/seeding"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SeedingExecutionStore owns the physical session until every native call joins.
// Session loss without a recorded release remains blocked, even if its lock is gone.
type SeedingExecutionStore struct {
	pool *pgxpool.Pool
	ids  ports.IDGen
}

func NewSeedingExecutionStore(pool *pgxpool.Pool, ids ports.IDGen) *SeedingExecutionStore {
	return &SeedingExecutionStore{pool: pool, ids: ids}
}

var _ ports.SeedingExecutionRepository = (*SeedingExecutionStore)(nil)

type seedingExecutionPermit struct {
	store                          *SeedingExecutionStore
	conn                           *pgxpool.Conn
	lease                          ports.JobLease
	dispatch                       db.SeedingRemovalDispatch
	config                         db.SeedingConfiguration
	task                           db.DownloadTask
	snapshot                       ports.SeedingExecution
	mu                             sync.Mutex
	held, opened, closed, terminal bool
}

func seedingExecutionError(err error) error {
	if errors.Is(err, ports.ErrSeedingLeaseLost) {
		return ports.ErrSeedingLeaseLost
	}
	return seedingConfigError(err)
}
func seedingLease(l ports.JobLease) db.GetAuthorizedSearchJobParams {
	return db.GetAuthorizedSearchJobParams{ID: l.ID, Kind: "seeding_removal", LeaseOwner: pgtype.Text{String: l.Owner, Valid: true}, LeaseToken: l.Token}
}
func seedingTarget(t db.DownloadTask) ports.SeedingRemovalTarget {
	return ports.SeedingRemovalTarget{InfoHash: t.InfoHash, OperationTag: "pulsemedia:op:" + t.OperationID.String(), SavePath: t.SavePath, TotalBytes: t.TotalBytes}
}
func seedingPermitTarget(p db.SeedingRemovalPermit) ports.SeedingRemovalTarget {
	return ports.SeedingRemovalTarget{InfoHash: p.InfoHash, OperationTag: p.OperationTag, SavePath: p.SavePath, TotalBytes: p.TotalBytes}
}
func (s *SeedingExecutionStore) Acquire(ctx context.Context, l ports.JobLease) (out ports.SeedingExecutionPermit, err error) {
	defer func() { err = seedingExecutionError(err) }()
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil || s.pool == nil || s.ids == nil {
		return nil, ports.ErrUnavailable
	}
	if l.ID == uuid.Nil || l.Owner == "" || l.Token < 1 {
		return nil, ports.ErrRejected
	}
	q := db.New(s.pool)
	if _, err = q.GetAuthorizedSearchJob(ctx, seedingLease(l)); err != nil {
		return nil, ports.ErrSeedingLeaseLost
	}
	d, err := q.GetSeedingRemovalDispatch(ctx, l.ID)
	if err != nil {
		return nil, err
	}
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	p := &seedingExecutionPermit{store: s, conn: conn, lease: l, dispatch: d}
	defer func() {
		if err != nil {
			p.Close()
		}
	}()
	q = db.New(conn)
	p.held, err = q.TrySeedingExecutionGate(ctx, d.ClientID)
	if err != nil {
		return nil, err
	}
	if !p.held {
		return nil, ports.ErrUnavailable
	}
	// A free advisory lock does not prove that a vanished backend's network call
	// stopped. The unreleased row intentionally prevents automatic replacement.
	if _, err = q.UnreleasedSeedingExecution(ctx, d.ClientID); err == nil {
		return nil, ports.ErrUnavailable
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer managementRollback(ctx, tx)
	q = db.New(tx)
	permanent, e := q.GetSeedingRemovalPermit(ctx, d.TaskID)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return nil, e
	}
	p.snapshot.ObserveOnly = e == nil
	if err = p.authority(ctx, q); err != nil {
		return nil, err
	}
	p.task, err = q.GetSubmissionTask(ctx, d.TaskID)
	if err != nil {
		return nil, err
	}
	p.snapshot.TaskID = d.TaskID
	p.snapshot.ClientID = d.ClientID
	p.snapshot.ClientStamp = d.ClientStamp
	p.snapshot.Target = seedingTarget(p.task)
	if p.snapshot.ObserveOnly {
		if permanent.JobID != l.ID || permanent.ClientStamp != d.ClientStamp || permanent.ClientID != d.ClientID || permanent.OwnerID != d.OwnerID {
			return nil, ports.ErrRejected
		}
		p.snapshot.Target = seedingPermitTarget(permanent)
	} else {
		view, e := seedingConfigView(p.config)
		if e != nil {
			return nil, e
		}
		p.snapshot.Rule = view.Rule
		proof, e := q.ReadOrdinarySeedingProtection(ctx, db.ReadOrdinarySeedingProtectionParams{TaskID: d.TaskID, ClientStamp: d.ClientStamp})
		if e != nil {
			return nil, e
		}
		p.snapshot.Protection = rule.Protection{Owned: proof.Owned, Imported: proof.Imported, PendingWork: proof.PendingWork}
	}
	if p.task.ClientID != d.ClientID || seedingTarget(p.task) != p.snapshot.Target {
		return nil, ports.ErrRejected
	}
	if _, err = q.LockJobForWrite(ctx, l.ID); err != nil {
		return nil, err
	}
	if _, err = q.GetAuthorizedSearchJob(ctx, seedingLease(l)); err != nil {
		return nil, ports.ErrSeedingLeaseLost
	}
	schedule, err := q.GetSeedingRemovalSchedule(ctx, d.TaskID)
	if err != nil {
		return nil, err
	}
	if !schedule.ActiveJobID.Valid || schedule.ActiveJobID.Bytes != l.ID || schedule.ConfigVersion != d.ConfigVersion || (schedule.State != "queued" && schedule.State != "running") {
		return nil, ports.ErrSeedingLeaseLost
	}
	n, err := q.StartSeedingRemovalSchedule(ctx, db.StartSeedingRemovalScheduleParams{TaskID: d.TaskID, ActiveJobID: pgtype.UUID{Bytes: l.ID, Valid: true}, ConfigVersion: d.ConfigVersion})
	if err != nil {
		return nil, err
	}
	if n != 1 {
		return nil, ports.ErrSeedingLeaseLost
	}
	if _, err = q.OpenSeedingExecution(ctx, db.OpenSeedingExecutionParams{JobID: l.ID, LeaseToken: l.Token, LeaseOwner: l.Owner, ClientID: d.ClientID, ObserveOnly: p.snapshot.ObserveOnly}); err != nil {
		return nil, err
	}
	if err = p.validate(ctx, q); err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	p.opened = true
	return p, nil
}

// authority uses the same account/configuration lock order as configuration saves.
func (p *seedingExecutionPermit) authority(ctx context.Context, q *db.Queries) error {
	if err := seedingRuntimeGuard(ctx, q); err != nil {
		return err
	}
	a := &ports.Actor{Type: ports.ActorTypeUser, ID: p.dispatch.OwnerID.String(), Role: ports.RoleAdmin}
	if err := settingUser(ctx, q, a, true); err != nil {
		return err
	}
	if err := q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: seedingConfigAction, AggregateID: p.dispatch.ClientID.String()}); err != nil {
		return err
	}
	if _, err := q.LockDownloadClientForEdit(ctx, p.dispatch.ClientID); err != nil {
		return err
	}
	c, err := q.LockSeedingConfig(ctx, p.dispatch.ClientID)
	if err != nil {
		return err
	}
	stamp, err := q.DownloadGlobalRatesBinding(ctx, p.dispatch.ClientID)
	if err != nil {
		return err
	}
	if c.OwnerID != p.dispatch.OwnerID || c.ClientStamp != p.dispatch.ClientStamp || stamp != p.dispatch.ClientStamp || (!p.snapshot.ObserveOnly && (!c.Enabled || c.Version != p.dispatch.ConfigVersion)) {
		return ports.ErrSeedingConfigConflict
	}
	p.config = c
	return nil
}
func (p *seedingExecutionPermit) Snapshot() ports.SeedingExecution {
	p.mu.Lock()
	defer p.mu.Unlock()
	v := p.snapshot
	if v.Rule.MinSeedingSeconds != nil {
		n := *v.Rule.MinSeedingSeconds
		v.Rule.MinSeedingSeconds = &n
	}
	if v.Rule.MinShareRatio != nil {
		n := *v.Rule.MinShareRatio
		v.Rule.MinShareRatio = &n
	}
	if v.Rule.LowFreeBytes != nil {
		n := *v.Rule.LowFreeBytes
		v.Rule.LowFreeBytes = &n
	}
	return v
}
func (p *seedingExecutionPermit) validate(ctx context.Context, q *db.Queries) error {
	if p.closed || p.terminal || !p.held || p.conn.Conn().IsClosed() {
		return ports.ErrSeedingLeaseLost
	}
	if err := p.authority(ctx, q); err != nil {
		return err
	}
	if err := q.LockDownloadAcquisition(ctx, p.task.OperationID); err != nil {
		return err
	}
	task, err := q.GetSubmissionTask(ctx, p.dispatch.TaskID)
	if err != nil {
		return err
	}
	if task.OperationID != p.task.OperationID || task.ClientID != p.dispatch.ClientID || seedingTarget(task) != p.snapshot.Target {
		return ports.ErrRejected
	}
	if _, err = q.LockJobForWrite(ctx, p.lease.ID); err != nil {
		return err
	}
	job, err := q.GetAuthorizedSearchJob(ctx, seedingLease(p.lease))
	if err != nil {
		return ports.ErrSeedingLeaseLost
	}
	var payload seedingJobPayload
	if statsDecode(job.Payload, &payload) != nil || payload != (seedingJobPayload{p.dispatch.TaskID, p.dispatch.ClientID, p.dispatch.ConfigVersion}) || job.MaxAttempts != 3 {
		return ports.ErrRejected
	}
	d, err := q.GetSeedingRemovalDispatch(ctx, p.lease.ID)
	if err != nil {
		return err
	}
	if d != p.dispatch {
		return ports.ErrRejected
	}
	schedule, err := q.GetSeedingRemovalSchedule(ctx, p.dispatch.TaskID)
	if err != nil {
		return err
	}
	if !schedule.ActiveJobID.Valid || schedule.ActiveJobID.Bytes != p.lease.ID || schedule.ConfigVersion != p.dispatch.ConfigVersion || schedule.State != "running" {
		return ports.ErrSeedingLeaseLost
	}
	e, err := q.GetSeedingExecution(ctx, db.GetSeedingExecutionParams{JobID: p.lease.ID, LeaseToken: p.lease.Token})
	if err != nil {
		return err
	}
	if e.LeaseOwner != p.lease.Owner || e.ClientID != p.dispatch.ClientID || uint32(e.BackendPid) != p.conn.Conn().PgConn().PID() || e.ObserveOnly != p.snapshot.ObserveOnly || e.ReleasedAt.Valid || e.Result.Valid {
		return ports.ErrSeedingLeaseLost
	}
	held, err := q.CheckSeedingExecutionGate(ctx, p.dispatch.ClientID)
	if err != nil {
		return err
	}
	if !held {
		return ports.ErrSeedingLeaseLost
	}
	permanent, err := q.GetSeedingRemovalPermit(ctx, p.dispatch.TaskID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if err == nil {
		if permanent.JobID != p.lease.ID || permanent.OwnerID != p.dispatch.OwnerID || permanent.ConfigVersion != p.dispatch.ConfigVersion || permanent.ClientStamp != p.dispatch.ClientStamp || seedingPermitTarget(permanent) != p.snapshot.Target || (!p.snapshot.ObserveOnly && (permanent.LeaseOwner != p.lease.Owner || permanent.LeaseToken != p.lease.Token)) {
			return ports.ErrRejected
		}
	} else if p.snapshot.ObserveOnly {
		return ports.ErrRejected
	}
	if !p.snapshot.ObserveOnly {
		proof, e := q.ReadOrdinarySeedingProtection(ctx, db.ReadOrdinarySeedingProtectionParams{TaskID: p.dispatch.TaskID, ClientStamp: p.dispatch.ClientStamp})
		if e != nil {
			return e
		}
		if (rule.Protection{Owned: proof.Owned, Imported: proof.Imported, PendingWork: proof.PendingWork}) != p.snapshot.Protection {
			return ports.ErrRejected
		}
	}
	return nil
}

// Once I/O may be active, cancellation must not close its physical-lock session.
// Queries use the retained session; caller cancellation is checked before commit.
func (p *seedingExecutionPermit) transaction(ctx context.Context, fn func(context.Context, *db.Queries) error) (err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	defer func() { err = seedingExecutionError(err) }()
	if err = ctx.Err(); err != nil {
		return err
	}
	if p.closed || p.terminal {
		return ports.ErrSeedingLeaseLost
	}
	dbctx := context.WithoutCancel(ctx)
	tx, err := p.conn.Begin(dbctx)
	if err != nil {
		return err
	}
	defer managementRollback(dbctx, tx)
	q := db.New(tx)
	if err = p.validate(dbctx, q); err != nil {
		return err
	}
	if fn != nil {
		if err = fn(dbctx, q); err != nil {
			return err
		}
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	return tx.Commit(dbctx)
}
func (p *seedingExecutionPermit) Check(ctx context.Context) error { return p.transaction(ctx, nil) }
func (p *seedingExecutionPermit) Reserve(ctx context.Context) error {
	return p.transaction(ctx, func(c context.Context, q *db.Queries) error {
		_, err := q.ReserveSeedingExecutionRequest(c, db.ReserveSeedingExecutionRequestParams{JobID: p.lease.ID, LeaseToken: p.lease.Token, LeaseOwner: p.lease.Owner})
		if errors.Is(err, pgx.ErrNoRows) {
			return ports.ErrRejected
		}
		return err
	})
}
func (p *seedingExecutionPermit) Prepare(ctx context.Context, o ports.SeedingRemoteTorrent) error {
	return p.transaction(ctx, func(c context.Context, q *db.Queries) error {
		if p.snapshot.ObserveOnly || (ports.SeedingRemovalTarget{InfoHash: o.InfoHash, OperationTag: o.OperationTag, SavePath: o.SavePath, TotalBytes: o.TotalBytes}) != p.snapshot.Target {
			return ports.ErrRejected
		}
		decision, err := rule.Evaluate(&o.Facts, p.snapshot.Protection, p.snapshot.Rule)
		if err != nil || !decision.Eligible {
			return ports.ErrRejected
		}
		if _, err = q.GetSeedingRemovalPermit(c, p.dispatch.TaskID); err == nil {
			return ports.ErrRejected
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		e, err := q.GetSeedingExecution(c, db.GetSeedingExecutionParams{JobID: p.lease.ID, LeaseToken: p.lease.Token})
		if err != nil {
			return err
		}
		if !e.StartedAt.Valid || e.Requests < 2 || (p.snapshot.Rule.LowFreeBytes != nil && e.Requests != 3) {
			return ports.ErrRejected
		}
		freePath := pgtype.Text{}
		if o.Facts.FreeSpaceMatchesDownloadPath {
			freePath = pgtype.Text{String: o.SavePath, Valid: true}
		}
		_, err = q.CreateSeedingRemovalPermit(c, db.CreateSeedingRemovalPermitParams{TaskID: p.dispatch.TaskID, OperationID: p.task.OperationID, ClientID: p.dispatch.ClientID, ClientStamp: p.dispatch.ClientStamp, ConfigVersion: p.dispatch.ConfigVersion, OwnerID: p.dispatch.OwnerID, JobID: p.lease.ID, LeaseOwner: p.lease.Owner, LeaseToken: p.lease.Token, InfoHash: o.InfoHash, OperationTag: o.OperationTag, SavePath: o.SavePath, TotalBytes: o.TotalBytes, SeedingSeconds: seedingInt(o.Facts.SeedingSeconds), ShareRatio: seedingFloat(o.Facts.ShareRatio), InfiniteShareRatio: o.Facts.InfiniteShareRatio, FreeBytes: seedingInt(o.Facts.FreeBytes), FreeSpacePath: freePath, ObservedAt: e.StartedAt})
		if err != nil {
			return err
		}
		if err = seedingRuntimeEvent(c, q, p.store.ids, p.dispatch.TaskID, p.lease.ID, p.dispatch.ConfigVersion, "seeding.removal.prepared"); err != nil {
			return err
		}
		return p.validate(c, q)
	})
}
func (p *seedingExecutionPermit) Finish(ctx context.Context, result ports.SeedingExecutionResult, failure error) error {
	err := p.transaction(ctx, func(c context.Context, q *db.Queries) error {
		job, err := q.GetAuthorizedSearchJob(c, seedingLease(p.lease))
		if err != nil {
			return ports.ErrSeedingLeaseLost
		}
		delay := 10 * time.Second
		if limited, ok := ports.IsRateLimited(failure); ok && limited.RetryAfter > delay {
			delay = min(limited.RetryAfter, time.Hour)
		}
		delaySeconds := int32((delay + time.Second - 1) / time.Second)
		jobState, scheduleState := "succeeded", "idle"
		switch result {
		case ports.SeedingExecutionIneligible:
		case ports.SeedingExecutionAbsent, ports.SeedingExecutionAcknowledged:
			scheduleState = "retired"
		case ports.SeedingExecutionFailed:
			jobState = "dead"
		case ports.SeedingExecutionPresent:
			jobState = "dead"
			scheduleState = "review_required"
		case ports.SeedingExecutionUnknown:
			jobState = "dead"
			scheduleState = "review_required"
			if job.Attempts < job.MaxAttempts {
				jobState = "queued"
				scheduleState = "queued"
			}
		default:
			return ports.ErrRejected
		}
		n, err := q.FinishSeedingExecution(c, db.FinishSeedingExecutionParams{JobID: p.lease.ID, LeaseToken: p.lease.Token, LeaseOwner: p.lease.Owner, Result: pgtype.Text{String: string(result), Valid: true}})
		if err != nil {
			return err
		}
		if n != 1 {
			return ports.ErrSeedingLeaseLost
		}
		body, _ := json.Marshal(struct {
			TaskID      uuid.UUID                    `json:"task_id"`
			Outcome     ports.SeedingExecutionResult `json:"outcome"`
			ObserveOnly bool                         `json:"observe_only"`
		}{p.dispatch.TaskID, result, p.snapshot.ObserveOnly})
		safeError := pgtype.Text{}
		if failure != nil {
			safeError = pgtype.Text{String: "seeding native observation unavailable", Valid: true}
		}
		n, err = q.FinishSeedingRemovalJob(c, db.FinishSeedingRemovalJobParams{ID: p.lease.ID, LeaseOwner: pgtype.Text{String: p.lease.Owner, Valid: true}, LeaseToken: p.lease.Token, State: jobState, Result: body, Error: safeError, DelaySeconds: delaySeconds})
		if err != nil {
			return err
		}
		if n != 1 {
			return ports.ErrSeedingLeaseLost
		}
		n, err = q.FinishSeedingRemovalSchedule(c, db.FinishSeedingRemovalScheduleParams{TaskID: p.dispatch.TaskID, ActiveJobID: pgtype.UUID{Bytes: p.lease.ID, Valid: true}, State: scheduleState, IntervalSeconds: max(p.config.IntervalSeconds, delaySeconds)})
		if err != nil {
			return err
		}
		if n != 1 {
			return ports.ErrSeedingLeaseLost
		}
		payload, _ := json.Marshal(struct {
			JobID      uuid.UUID                    `json:"job_id"`
			LeaseToken int64                        `json:"lease_token"`
			Outcome    ports.SeedingExecutionResult `json:"outcome"`
		}{p.lease.ID, p.lease.Token, result})
		if _, err = q.InsertAuditLog(c, db.InsertAuditLogParams{ID: p.store.ids.NewUUID(), ActorType: string(ports.ActorTypeSystem), ActorID: "seeding-worker", Action: "seeding.removal.finished", TargetType: "download_task", TargetID: p.dispatch.TaskID.String(), Payload: payload}); err != nil {
			return err
		}
		aggregate := db.LockOutboxAggregateParams{AggregateType: "seeding.removal", AggregateID: p.dispatch.TaskID.String()}
		if err = q.LockOutboxAggregate(c, aggregate); err != nil {
			return err
		}
		seq, err := q.GetNextAggregateSeq(c, db.GetNextAggregateSeqParams(aggregate))
		if err != nil {
			return err
		}
		_, err = q.InsertOutboxEvent(c, db.InsertOutboxEventParams{ID: p.store.ids.NewUUID(), AggregateType: aggregate.AggregateType, AggregateID: aggregate.AggregateID, EventType: "seeding.removal.finished", Payload: payload, Seq: int64(seq)})
		return err
	})
	if err == nil {
		p.mu.Lock()
		p.terminal = true
		p.mu.Unlock()
	}
	return err
}
func (p *seedingExecutionPermit) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	p.closed = true
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	discard := func() { raw := p.conn.Hijack(); _ = raw.Close(ctx) }
	if p.opened {
		n, err := db.New(p.conn).ReleaseSeedingExecution(ctx, db.ReleaseSeedingExecutionParams{JobID: p.lease.ID, LeaseToken: p.lease.Token, LeaseOwner: p.lease.Owner})
		if err != nil || n != 1 {
			discard()
			return
		}
	}
	if p.held {
		released, err := db.New(p.conn).ReleaseSeedingExecutionGate(ctx, p.dispatch.ClientID)
		if err != nil || !released {
			discard()
			return
		}
	}
	p.conn.Release()
}
