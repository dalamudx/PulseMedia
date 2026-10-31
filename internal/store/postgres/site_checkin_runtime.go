package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type SiteCheckinStore struct {
	pool    *pgxpool.Pool
	ids     ports.IDGen
	decoder ports.CredentialDecoder
}

var _ ports.SiteCheckinRepository = (*SiteCheckinStore)(nil)

func NewSiteCheckinStore(pool *pgxpool.Pool, ids ports.IDGen, decoder ports.CredentialDecoder) *SiteCheckinStore {
	return &SiteCheckinStore{pool, ids, decoder}
}
func checkinAggregate(ctx context.Context, q *db.Queries, id uuid.UUID) error {
	return q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "site_checkin_config", AggregateID: id.String()})
}
func (s *SiteCheckinStore) material(ctx context.Context, q *db.Queries, id uuid.UUID) (ports.SiteCheckinExecution, error) {
	var out ports.SiteCheckinExecution
	if e := checkinAggregate(ctx, q, id); e != nil {
		return out, e
	}
	source, e := q.LockRSSIndexer(ctx, id)
	if e != nil {
		return out, e
	}
	row, e := q.GetSiteCheckinConfig(ctx, id)
	if e != nil {
		return out, e
	}
	c, e := checkinSnapshot(ctx, q, row)
	if e != nil {
		return out, e
	}
	if !c.Enabled || c.SourceConfigVersion != source.ConfigVersion {
		return out, ports.ErrEndpointVersionConflict
	}
	if s.decoder == nil {
		return out, ports.ErrUnavailable
	}
	resolved, modes, e := ResolveExecutableIndexerConfiguration(ctx, q, s.decoder, source)
	if e != nil {
		return out, e
	}
	if modes.Protocol != ports.IndexerProtocolNexusPHP || !statsURL(resolved.Url) {
		return out, ports.ErrRejected
	}
	cookie, e := s.decoder.DecryptSecret(source.ApiKeyEncrypted)
	if e != nil {
		return out, ports.ErrUnavailable
	}
	if strings.TrimSpace(string(cookie)) == "" {
		return out, ports.ErrRejected
	}
	return ports.SiteCheckinExecution{Config: *c, SourceURL: resolved.Url, Cookie: string(cookie)}, nil
}
func (s *SiteCheckinStore) EnqueueDue(ctx context.Context) error {
	if s == nil || s.pool == nil || s.ids == nil {
		return ports.ErrUnavailable
	}
	ids, e := db.New(s.pool).ListSiteCheckinSources(ctx)
	if e != nil {
		return e
	}
	for _, id := range ids {
		if e = s.enqueue(ctx, id); e != nil {
			return e
		}
	}
	return nil
}
func (s *SiteCheckinStore) enqueue(ctx context.Context, id uuid.UUID) error {
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer managementRollback(ctx, tx)
	q := db.New(tx)
	if e = statsRuntimeGuard(ctx, q); e != nil {
		return e
	}
	x, e := s.material(ctx, q, id)
	if e != nil {
		if errors.Is(e, ports.ErrRejected) || errors.Is(e, ports.ErrEndpointVersionConflict) || errors.Is(e, pgx.ErrNoRows) {
			return nil
		}
		return e
	}
	now, e := q.SiteCheckinClock(ctx)
	if e != nil {
		return e
	}
	c := x.Config
	cal, e := q.SiteCheckinCalendar(ctx, db.SiteCheckinCalendarParams{Instant: now, Zone: c.Timezone, Daily: c.DailyAt})
	if e != nil {
		return e
	}
	if !cal.Due {
		return nil
	}
	_, e = q.FindSiteCheckinIdentity(ctx, db.FindSiteCheckinIdentityParams{IndexerID: id, ExpectedAccountID: c.ExpectedAccountID, CalendarDay: cal.CalendarDay})
	if e == nil {
		return nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return e
	}
	binding, e := q.SiteCheckinBinding(ctx, id)
	if e != nil {
		return e
	}
	dayID, jobID := s.ids.NewUUID(), s.ids.NewUUID()
	payload, _ := json.Marshal(map[string]any{"day_id": dayID})
	if e = q.CreateSiteCheckinJob(ctx, db.CreateSiteCheckinJobParams{ID: jobID, Kind: ports.SiteCheckinJobKind, Payload: payload, MaxAttempts: 3}); e != nil {
		return e
	}
	raw, _ := json.Marshal(c)
	if e = q.CreateSiteCheckinDay(ctx, db.CreateSiteCheckinDayParams{ID: dayID, IndexerID: id, ExpectedAccountID: c.ExpectedAccountID, Profile: c.Profile, Timezone: c.Timezone, CalendarDay: cal.CalendarDay, DayStart: cal.DayStart, DayEnd: cal.DayEnd, OriginalJobID: jobID, ConfigSnapshot: raw, ConfigVersion: c.Version, SourceVersion: c.SourceConfigVersion, BindingDigest: binding}); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

type siteCheckinPermit struct {
	mu               sync.Mutex
	store            *SiteCheckinStore
	conn             *PhysicalConnection
	lease            ports.JobLease
	snapshot         ports.SiteCheckinExecution
	day              db.SiteCheckinDay
	execution        db.SiteCheckinExecution
	grant            *ports.SiteCheckinSendPermission
	closed, terminal bool
}

var _ ports.SiteCheckinPermit = (*siteCheckinPermit)(nil)

func (s *SiteCheckinStore) Acquire(caller context.Context, lease ports.JobLease) (ports.SiteCheckinPermit, error) {
	if s == nil || s.pool == nil || s.ids == nil {
		return nil, ports.ErrUnavailable
	}
	if e := caller.Err(); e != nil {
		return nil, e
	}
	d, e := db.New(s.pool).FindSiteCheckinDay(caller, lease.ID)
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	conn, e := AcquirePhysicalConnection(caller, s.pool)
	if e != nil {
		return nil, e
	}
	held, e := db.New(conn).TrySiteAccountStatsGate(caller, d.IndexerID)
	if e != nil || !held {
		conn.Release()
		if e != nil {
			return nil, e
		}
		return nil, ports.NewRateLimitError(2*time.Second, "source busy")
	}
	p := &siteCheckinPermit{store: s, conn: conn, lease: lease, day: d}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(caller), 10*time.Second)
	defer cancel()
	tx, e := conn.Begin(ctx)
	if e != nil {
		p.Close()
		return nil, e
	}
	defer managementRollback(ctx, tx)
	q := db.New(tx)
	e = statsRuntimeGuard(ctx, q)
	var job db.Job
	if e == nil {
		_, e = q.LockJobForWrite(ctx, lease.ID)
	}
	if e == nil {
		job, e = q.GetJobByID(ctx, lease.ID)
	}
	if e == nil {
		if job.Kind != ports.SiteCheckinJobKind && job.Kind != ports.SiteCheckinObservationJobKind {
			e = ports.ErrUnavailable
		}
	}
	if e == nil {
		_, e = q.GetAuthorizedSearchJob(ctx, checkinLease(lease, job.Kind))
	}
	if e == nil {
		p.snapshot, e = s.material(ctx, q, d.IndexerID)
	}
	if e == nil {
		p.day, e = q.GetSiteCheckinDay(ctx, d.ID)
	}
	if e == nil {
		mode := "send"
		if job.Kind == ports.SiteCheckinObservationJobKind {
			mode = "observe"
		}
		if (mode == "send" && (p.day.OriginalJobID != lease.ID || p.day.PreparedExecutionID.Valid || p.day.Status != "open" || job.MaxAttempts != 3)) || (mode == "observe" && (!p.day.ObservationJobID.Valid || p.day.ObservationJobID.Bytes != lease.ID || !p.day.PreparedExecutionID.Valid || job.MaxAttempts != 1)) {
			e = ports.ErrUnavailable
		}
		if e == nil {
			b, err := q.SiteCheckinBackend(ctx)
			e = err
			if e == nil {
				p.execution, e = q.CreateSiteCheckinExecution(ctx, db.CreateSiteCheckinExecutionParams{ID: s.ids.NewUUID(), DayID: d.ID, JobID: lease.ID, Owner: lease.Owner, Token: lease.Token, Nonce: s.ids.NewUUID(), BackendPid: b.Pid, BackendStart: b.Started, Mode: mode, ConfigVersion: d.ConfigVersion, SourceVersion: d.SourceVersion, SourceFingerprint: d.BindingDigest})
			}
		}
	}
	if e == nil {
		p.snapshot.DayID = d.ID
		p.snapshot.ExecutionID = p.execution.ID
		p.snapshot.CalendarDay = d.CalendarDay.Time.Format("2006-01-02")
		p.snapshot.ObservationOnly = p.execution.Mode == "observe"
		_, e = p.validate(ctx, q)
	}
	if e == nil {
		e = caller.Err()
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		managementRollback(ctx, tx)
		p.Close()
		return nil, e
	}
	return p, nil
}
func checkinLease(l ports.JobLease, kind string) db.GetAuthorizedSearchJobParams {
	return db.GetAuthorizedSearchJobParams{ID: l.ID, Kind: kind, LeaseOwner: pgtype.Text{String: l.Owner, Valid: true}, LeaseToken: l.Token}
}
func (p *siteCheckinPermit) Snapshot() ports.SiteCheckinExecution {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.snapshot
}
func (p *siteCheckinPermit) validate(ctx context.Context, q *db.Queries) (ports.SiteCheckinWindow, error) {
	var w ports.SiteCheckinWindow
	if p.closed || p.terminal || p.conn.Conn().IsClosed() {
		return w, ports.ErrUnavailable
	}
	if e := statsRuntimeGuard(ctx, q); e != nil {
		return w, e
	}
	if _, e := q.LockJobForWrite(ctx, p.lease.ID); e != nil {
		return w, e
	}
	kind := ports.SiteCheckinJobKind
	if p.snapshot.ObservationOnly {
		kind = ports.SiteCheckinObservationJobKind
	}
	job, e := q.GetAuthorizedSearchJob(ctx, checkinLease(p.lease, kind))
	if e != nil {
		return w, ports.ErrUnavailable
	}
	var payload struct {
		DayID uuid.UUID `json:"day_id"`
	}
	if statsDecode(job.Payload, &payload) != nil || payload.DayID != p.day.ID {
		return w, ports.ErrUnavailable
	}
	x, e := p.store.material(ctx, q, p.day.IndexerID)
	if e != nil {
		return w, e
	}
	if !checkinSame(x.Config, p.snapshot.Config) || x.SourceURL != p.snapshot.SourceURL || x.Cookie != p.snapshot.Cookie {
		return w, ports.ErrEndpointVersionConflict
	}
	d, e := q.GetSiteCheckinDay(ctx, p.day.ID)
	if e != nil {
		return w, e
	}
	var frozen ports.SiteCheckinConfig
	if checkinDecode(d.ConfigSnapshot, &frozen) != nil || !checkinSame(frozen, x.Config) || d.Status == "attended_observed" || d.RestoreID.Valid {
		return w, ports.ErrEndpointVersionConflict
	}
	binding, e := q.SiteCheckinBinding(ctx, d.IndexerID)
	if e != nil {
		return w, e
	}
	if binding != d.BindingDigest {
		return w, ports.ErrEndpointVersionConflict
	}
	a, e := q.GetSiteCheckinExecution(ctx, p.execution.ID)
	if e != nil {
		return w, e
	}
	b, e := q.SiteCheckinBackend(ctx)
	if e != nil {
		return w, e
	}
	held, e := q.CheckSiteAccountStatsGate(ctx, d.IndexerID)
	if e != nil {
		return w, e
	}
	if !held || a.BackendPid != b.Pid || !a.BackendStart.Time.Equal(b.Started.Time) || a.Nonce != p.execution.Nonce || a.Owner != p.lease.Owner || a.Token != p.lease.Token || a.FinishedAt.Valid {
		return w, ports.ErrUnavailable
	}
	if _, e = q.SiteCheckinArm(ctx, a.Nonce.String()); e != nil {
		return w, e
	}
	now, e := q.SiteCheckinClock(ctx)
	if e != nil {
		return w, e
	}
	if now.Time.Before(d.DayStart.Time) || !now.Time.Before(d.DayEnd.Time) || now.Time.Before(a.CreatedAt.Time) || a.ObserveStartedAt.Valid && now.Time.Before(a.ObserveStartedAt.Time) {
		return w, ports.ErrUnavailable
	}
	cal, e := q.SiteCheckinCalendar(ctx, db.SiteCheckinCalendarParams{Instant: now, Zone: d.Timezone, Daily: x.Config.DailyAt})
	if e != nil {
		return w, e
	}
	if !cal.CalendarDay.Time.Equal(d.CalendarDay.Time) || !cal.DayStart.Time.Equal(d.DayStart.Time) || !cal.DayEnd.Time.Equal(d.DayEnd.Time) {
		return w, ports.ErrUnavailable
	}
	if _, e = q.GetAuthorizedSearchJob(ctx, checkinLease(p.lease, kind)); e != nil {
		return w, ports.ErrUnavailable
	}
	return ports.SiteCheckinWindow{Now: now.Time, DayStart: d.DayStart.Time, DayEnd: d.DayEnd.Time}, nil
}

// Each short operation keeps cancellation away from the physical gate session.
func (p *siteCheckinPermit) operation(caller context.Context, fn func(context.Context, *db.Queries, ports.SiteCheckinWindow) error) (ports.SiteCheckinWindow, error) {
	var w ports.SiteCheckinWindow
	if e := caller.Err(); e != nil {
		return w, e
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(caller), 10*time.Second)
	defer cancel()
	if p.closed || p.terminal {
		return w, ports.ErrUnavailable
	}
	tx, e := p.conn.Begin(ctx)
	if e != nil {
		return w, e
	}
	defer managementRollback(ctx, tx)
	q := db.New(tx)
	w, e = p.validate(ctx, q)
	if e != nil {
		return w, e
	}
	if fn != nil {
		if e = fn(ctx, q, w); e != nil {
			return w, e
		}
	}
	if e = caller.Err(); e != nil {
		return w, e
	}
	if e = tx.Commit(ctx); e != nil {
		return w, e
	}
	if e = caller.Err(); e != nil {
		return w, e
	}
	return w, nil
}
func (p *siteCheckinPermit) Check(ctx context.Context) (ports.SiteCheckinWindow, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.operation(ctx, func(ctx context.Context, q *db.Queries, w ports.SiteCheckinWindow) error {
		a, e := q.GetSiteCheckinExecution(ctx, p.execution.ID)
		if e != nil {
			return e
		}
		if a.PreparedAt.Valid && w.Now.Add(15*time.Second).After(a.ValidUntil.Time) {
			return ports.ErrUnavailable
		}
		return nil
	})
}
func checkinBudget(ctx context.Context, q *db.Queries, id uuid.UUID) error {
	_, e := q.ReserveIndexerBudget(ctx, db.ReserveIndexerBudgetParams{ID: id, IntervalMs: 2000})
	if errors.Is(e, pgx.ErrNoRows) {
		source, err := q.LockRSSIndexer(ctx, id)
		if err != nil {
			return err
		}
		now, err := q.SiteCheckinClock(ctx)
		if err != nil {
			return err
		}
		until := source.NextQueryAt.Time
		if source.RateLimitUntil.Valid && source.RateLimitUntil.Time.After(until) {
			until = source.RateLimitUntil.Time
		}
		return ports.NewRateLimitError(statsRetry(until.Sub(now.Time)), "checkin budget")
	}
	return e
}
func (p *siteCheckinPermit) ReserveObserve(ctx context.Context) (ports.SiteCheckinWindow, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.operation(ctx, func(ctx context.Context, q *db.Queries, w ports.SiteCheckinWindow) error {
		if e := checkinBudget(ctx, q, p.day.IndexerID); e != nil {
			return e
		}
		n, e := q.StartSiteCheckinObserve(ctx, p.execution.ID)
		if e != nil {
			return e
		}
		if n != 1 {
			return ports.ErrRejected
		}
		return nil
	})
}
func (p *siteCheckinPermit) PrepareSubmit(ctx context.Context, o ports.CheckinObservation) (ports.SiteCheckinSendPermission, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var grant ports.SiteCheckinSendPermission
	if p.snapshot.ObservationOnly || p.grant != nil || o.AccountID != p.snapshot.Config.ExpectedAccountID || o.Mark != ports.CheckinMarkPending {
		return grant, ports.ErrRejected
	}
	w, e := p.operation(ctx, func(ctx context.Context, q *db.Queries, w ports.SiteCheckinWindow) error {
		conflict, e := q.SiteCheckinConflictingCalendar(ctx, db.SiteCheckinConflictingCalendarParams{IndexerID: p.day.IndexerID, ExpectedAccountID: p.day.ExpectedAccountID, Timezone: p.day.Timezone})
		if e != nil {
			return e
		}
		if conflict {
			return ports.ErrEndpointVersionConflict
		}
		if e = checkinBudget(ctx, q, p.day.IndexerID); e != nil {
			return e
		}
		a, e := q.PrepareSiteCheckinExecution(ctx, db.PrepareSiteCheckinExecutionParams{ID: p.execution.ID, PermissionID: pgtype.UUID{Bytes: p.store.ids.NewUUID(), Valid: true}})
		if e != nil {
			return e
		}
		n, e := q.MarkSiteCheckinPrepared(ctx, db.MarkSiteCheckinPreparedParams{ID: p.day.ID, PreparedExecutionID: pgtype.UUID{Bytes: p.execution.ID, Valid: true}})
		if e != nil {
			return e
		}
		if n != 1 {
			return ports.ErrUnavailable
		}
		grant = ports.SiteCheckinSendPermission{ID: a.PermissionID.Bytes, DayID: p.day.ID, ExecutionID: a.ID, Window: w, ValidUntil: a.ValidUntil.Time}
		return nil
	})
	if e != nil {
		return ports.SiteCheckinSendPermission{}, e
	}
	grant.Window = w
	p.grant = &grant
	return grant, nil
}
func checkinPositive(o *ports.CheckinObservation, uid string) bool {
	return o != nil && o.AccountID == uid && o.Mark == ports.CheckinMarkAttended
}
func (p *siteCheckinPermit) Finish(ctx context.Context, out ports.SiteCheckinOutcome) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, e := p.operation(ctx, func(ctx context.Context, q *db.Queries, w ports.SiteCheckinWindow) error {
		a, e := q.GetSiteCheckinExecution(ctx, p.execution.ID)
		if e != nil {
			return e
		}
		if p.snapshot.ObservationOnly && (out.Permission != nil || out.Submission != nil) {
			return ports.ErrRejected
		}
		if !a.PreparedAt.Valid && (out.Permission != nil || out.Submission != nil) {
			return ports.ErrRejected
		}
		state, dayState, jobState, code := "not_sent", "review_required", "dead", "unavailable"
		raw := []byte("{}")
		if out.Failure != nil {
			code = statsFailure(out.Failure)
			if rate, ok := ports.IsRateLimited(out.Failure); ok {
				if e = q.DeferSiteAccountStatsBudget(ctx, db.DeferSiteAccountStatsBudgetParams{ID: p.day.IndexerID, RetryMs: statsRetry(rate.RetryAfter).Milliseconds()}); e != nil {
					return e
				}
			}
		}
		if a.PreparedAt.Valid {
			state = "review_required"
			if out.Permission == nil || p.grant == nil || *out.Permission != *p.grant {
				return ports.ErrRejected
			}
		}
		positive := out.Failure == nil && a.ObserveStartedAt.Valid && checkinPositive(out.Observation, p.day.ExpectedAccountID)
		if a.PreparedAt.Valid {
			positive = out.Failure == nil && out.Submission != nil && out.Submission.State == ports.CheckinSubmissionAttended && checkinPositive(out.Submission.Observation, p.day.ExpectedAccountID)
		}
		if positive {
			state, dayState, jobState, code = "attended_observed", "attended_observed", "succeeded", ""
			raw, _ = json.Marshal(map[string]string{"account_id": p.day.ExpectedAccountID, "mark": "attended"})
		} else if p.snapshot.ObservationOnly {
			state = "review_required"
		}
		n, e := q.FinishSiteCheckinExecution(ctx, db.FinishSiteCheckinExecutionParams{ID: a.ID, State: state, Result: raw, SafeCode: code})
		if e != nil {
			return e
		}
		if n != 1 {
			return ports.ErrUnavailable
		}
		if e = q.FinishSiteCheckinDay(ctx, db.FinishSiteCheckinDayParams{ID: p.day.ID, Status: dayState, SafeCode: code}); e != nil {
			return e
		}
		n, e = q.FinishSiteCheckinJob(ctx, db.FinishSiteCheckinJobParams{ID: p.lease.ID, LeaseOwner: pgtype.Text{String: p.lease.Owner, Valid: true}, LeaseToken: p.lease.Token, State: jobState, Column5: code})
		if e != nil {
			return e
		}
		if n != 1 {
			return ports.ErrUnavailable
		}
		return nil
	})
	if e == nil {
		p.terminal = true
	}
	return e
}
func (p *siteCheckinPermit) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	p.closed = true
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	held, e := db.New(p.conn).ReleaseSiteAccountStatsGate(ctx, p.day.IndexerID)
	if e != nil || !held {
		raw := p.conn.Hijack()
		_ = raw.Close(ctx)
		return
	}
	p.conn.Release()
}
func (s *SiteCheckinStore) RecoverExpired(ctx context.Context) error {
	if s == nil || s.pool == nil || s.ids == nil {
		return ports.ErrUnavailable
	}
	ids, e := db.New(s.pool).ListSiteCheckinRecovery(ctx)
	if e != nil {
		return e
	}
	for _, id := range ids {
		if e = s.recover(ctx, id); e != nil {
			return e
		}
	}
	return nil
}
func (s *SiteCheckinStore) recover(ctx context.Context, id uuid.UUID) error {
	d, e := db.New(s.pool).GetSiteCheckinRestoreDay(ctx, id)
	if e != nil {
		return e
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer managementRollback(ctx, tx)
	q := db.New(tx)
	if e = statsRuntimeGuard(ctx, q); e != nil {
		return e
	}
	jobID := d.OriginalJobID
	if d.ObservationJobID.Valid {
		jobID = d.ObservationJobID.Bytes
	}
	_, e = q.LockJobForWrite(ctx, jobID)
	if e != nil {
		return e
	}
	j, e := q.GetJobByID(ctx, jobID)
	if e != nil {
		return e
	}
	if e = checkinAggregate(ctx, q, d.IndexerID); e != nil {
		return e
	}
	_, e = q.LockRSSIndexer(ctx, d.IndexerID)
	if e != nil {
		return e
	}
	c, e := q.GetSiteCheckinConfig(ctx, d.IndexerID)
	if e != nil {
		return e
	}
	d, e = q.GetSiteCheckinDay(ctx, id)
	if e != nil {
		return e
	}
	if d.ObservationJobID.Valid && d.ObservationJobID.Bytes != jobID {
		return nil
	}
	now, e := q.SiteCheckinClock(ctx)
	if e != nil {
		return e
	}
	if j.State == "running" && j.LeaseUntil.Valid && j.LeaseUntil.Time.After(now.Time) {
		return nil
	}
	if j.State != "running" && j.State != "queued" && j.State != "dead" {
		return nil
	}
	if d.Status == "attended_observed" || d.RestoreID.Valid {
		return nil
	}
	binding, e := q.SiteCheckinBinding(ctx, d.IndexerID)
	if e != nil {
		return e
	}
	current := c.Enabled && c.Version == d.ConfigVersion && c.SourceConfigVersion == d.SourceVersion && binding == d.BindingDigest && !now.Time.Before(d.DayStart.Time) && now.Time.Before(d.DayEnd.Time)
	retry := !d.PreparedExecutionID.Valid && current && j.State == "running" && j.Attempts < j.MaxAttempts
	next := "dead"
	if retry {
		next = "queued"
	}
	if _, e = q.ArmSiteCheckinRecovery(ctx); e != nil {
		return e
	}
	if e = q.RecoverSiteCheckinJob(ctx, db.RecoverSiteCheckinJobParams{ID: jobID, NewState: next}); e != nil {
		return e
	}
	if e = q.RecoverSiteCheckinExecutions(ctx, id); e != nil {
		return e
	}
	if !retry {
		if e = q.FinishSiteCheckinDay(ctx, db.FinishSiteCheckinDayParams{ID: id, Status: "review_required", SafeCode: "unavailable"}); e != nil {
			return e
		}
	}
	if d.PreparedExecutionID.Valid && !d.ObservationJobID.Valid && current {
		observeID := s.ids.NewUUID()
		payload, _ := json.Marshal(map[string]any{"day_id": d.ID})
		if e = q.CreateSiteCheckinJob(ctx, db.CreateSiteCheckinJobParams{ID: observeID, Kind: ports.SiteCheckinObservationJobKind, Payload: payload, MaxAttempts: 1}); e != nil {
			return e
		}
		n, err := q.SetSiteCheckinObservationJob(ctx, db.SetSiteCheckinObservationJobParams{ID: id, ObservationJobID: pgtype.UUID{Bytes: observeID, Valid: true}})
		if err != nil {
			return err
		}
		if n != 1 {
			return ports.ErrUnavailable
		}
	}
	return tx.Commit(ctx)
}
