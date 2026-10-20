package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/indexer"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type SiteAccountStatsStore struct {
	pool    *pgxpool.Pool
	ids     ports.IDGen
	decoder ports.CredentialDecoder
}

func NewSiteAccountStatsStore(pool *pgxpool.Pool, ids ports.IDGen, decoder ports.CredentialDecoder) *SiteAccountStatsStore {
	return &SiteAccountStatsStore{pool: pool, ids: ids, decoder: decoder}
}

type statsJobPayload struct {
	IndexerID     uuid.UUID `json:"indexer_id"`
	ConfigVersion int64     `json:"config_version"`
	SourceVersion int64     `json:"source_version"`
}

func statsRuntimeGuard(ctx context.Context, q *db.Queries) error {
	isolated, e := q.GuardDoubanSyncIsolation(ctx)
	if e != nil {
		return e
	}
	if isolated {
		return ports.ErrUnavailable
	}
	return nil
}

func (s *SiteAccountStatsStore) material(ctx context.Context, q *db.Queries, id uuid.UUID) (ports.SiteAccountStatsExecution, error) {
	var out ports.SiteAccountStatsExecution
	source, e := q.LockRSSIndexer(ctx, id)
	if e != nil {
		return out, e
	}
	row, e := q.GetSiteAccountStatsConfig(ctx, id)
	if e != nil {
		return out, e
	}
	config, e := statsSnapshot(ctx, q, row)
	if e != nil {
		return out, e
	}
	if !config.Enabled || config.SourceConfigVersion != source.ConfigVersion {
		return out, ports.ErrEndpointVersionConflict
	}
	stored, e := q.GetIndexerConfiguration(ctx, id)
	if e != nil {
		return out, e
	}
	if stored.Protocol != string(ports.IndexerProtocolNexusPHP) {
		return out, ports.ErrRejected
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
	return ports.SiteAccountStatsExecution{Config: *config, SourceURL: resolved.Url, Cookie: string(cookie)}, nil
}

func (s *SiteAccountStatsStore) EnqueueDue(ctx context.Context) error {
	if s == nil || s.pool == nil || s.ids == nil {
		return ports.ErrUnavailable
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
	// Serialize the bounded scheduler batch, including absent schedule rows.
	if e = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "site_account_stats_scheduler", AggregateID: "global"}); e != nil {
		return e
	}
	ids, e := q.ListDueSiteAccountStats(ctx)
	if e != nil {
		return e
	}
	for _, id := range ids {
		execution, err := s.material(ctx, q, id)
		if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, ports.ErrNotFound) || errors.Is(err, ports.ErrRejected) || errors.Is(err, ports.ErrEndpointVersionConflict) {
			continue
		}
		if err != nil {
			return err
		}
		schedule, err := q.GetSiteAccountStatsSchedule(ctx, id)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err == nil {
			if schedule.ActiveJobID.Valid {
				continue
			}
			if schedule.ConfigVersion == execution.Config.Version {
				if schedule.State != "idle" {
					continue
				}
				now, err := q.GetSearchDatabaseTime(ctx)
				if err != nil {
					return err
				}
				if schedule.NextDueAt.Time.After(now.Time) {
					continue
				}
			}
		}
		c := execution.Config
		payload, _ := json.Marshal(statsJobPayload{id, c.Version, c.SourceConfigVersion})
		job, err := q.CreateSiteAccountStatsJob(ctx, db.CreateSiteAccountStatsJobParams{ID: s.ids.NewUUID(), DedupeKey: pgtype.Text{String: fmt.Sprintf("%s:%d", id, c.Version), Valid: true}, Payload: payload})
		if err != nil {
			return err
		}
		if err = q.CreateSiteAccountStatsAttempt(ctx, db.CreateSiteAccountStatsAttemptParams{JobID: job.ID, IndexerID: id, ConfigVersion: c.Version, SourceVersion: c.SourceConfigVersion}); err != nil {
			return err
		}
		if err = q.QueueSiteAccountStatsSchedule(ctx, db.QueueSiteAccountStatsScheduleParams{IndexerID: id, ConfigVersion: c.Version, ActiveJobID: pgtype.UUID{Bytes: job.ID, Valid: true}}); err != nil {
			return err
		}
	}
	if e = statsRuntimeGuard(ctx, q); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

func (s *SiteAccountStatsStore) RecoverExpired(ctx context.Context) error {
	if s == nil || s.pool == nil {
		return ports.ErrUnavailable
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
	ids, e := q.ListExpiredSiteAccountStats(ctx)
	if e != nil {
		return e
	}
	for _, id := range ids {
		if e = q.ReviewSiteAccountStatsAttempt(ctx, id); e != nil {
			return e
		}
		if e = q.ReviewSiteAccountStatsSchedule(ctx, pgtype.UUID{Bytes: id, Valid: true}); e != nil {
			return e
		}
		if e = q.ReviewSiteAccountStatsJob(ctx, id); e != nil {
			return e
		}
	}
	if e = statsRuntimeGuard(ctx, q); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

type siteAccountStatsPermit struct {
	store                                *SiteAccountStatsStore
	conn                                 *pgxpool.Conn
	lease                                ports.JobLease
	snapshot                             ports.SiteAccountStatsExecution
	binding                              string
	mu                                   sync.Mutex
	closed, consumed, reserved, terminal bool
}

func statsLeaseParams(l ports.JobLease) db.GetAuthorizedSearchJobParams {
	return db.GetAuthorizedSearchJobParams{ID: l.ID, Kind: ports.SiteAccountStatsJobKind, LeaseOwner: pgtype.Text{String: l.Owner, Valid: true}, LeaseToken: l.Token}
}

func (s *SiteAccountStatsStore) Acquire(ctx context.Context, lease ports.JobLease) (ports.SiteAccountStatsPermit, error) {
	if s == nil || s.pool == nil || lease.ID == uuid.Nil || lease.Owner == "" || lease.Token < 1 {
		return nil, ports.ErrUnavailable
	}
	q := db.New(s.pool)
	if _, e := q.GetAuthorizedSearchJob(ctx, statsLeaseParams(lease)); e != nil {
		return nil, ports.ErrUnavailable
	}
	attempt, e := q.GetSiteAccountStatsAttempt(ctx, lease.ID)
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	conn, e := s.pool.Acquire(ctx)
	if e != nil {
		return nil, e
	}
	p := &siteAccountStatsPermit{store: s, conn: conn, lease: lease, snapshot: ports.SiteAccountStatsExecution{Config: ports.SiteAccountStatsConfig{IndexerID: attempt.IndexerID}}}
	// Cancellation cannot release this session while the caller's physical HTTP is alive.
	if e = db.New(conn).AcquireSiteAccountStatsGate(ctx, attempt.IndexerID); e != nil {
		p.Close()
		return nil, e
	}
	tx, e := conn.Begin(ctx)
	if e != nil {
		p.Close()
		return nil, e
	}
	defer managementRollback(ctx, tx)
	q = db.New(tx)
	if e = statsRuntimeGuard(ctx, q); e == nil {
		_, e = q.LockJobForWrite(ctx, lease.ID)
	}
	if e == nil {
		_, e = q.GetAuthorizedSearchJob(ctx, statsLeaseParams(lease))
	}
	if e == nil {
		p.snapshot, e = s.material(ctx, q, attempt.IndexerID)
	}
	if e == nil {
		var a db.SiteAccountStatsAttempt
		a, e = q.GetSiteAccountStatsAttempt(ctx, lease.ID)
		if e == nil && (a.State != "queued" || a.ExecutionOwner.Valid || a.ConfigVersion != p.snapshot.Config.Version || a.SourceVersion != p.snapshot.Config.SourceConfigVersion) {
			e = ports.ErrUnavailable
		}
	}
	if e == nil {
		_, e = q.BindSiteAccountStatsAttempt(ctx, db.BindSiteAccountStatsAttemptParams{JobID: lease.ID, ExecutionOwner: pgtype.Text{String: lease.Owner, Valid: true}, ExecutionToken: pgtype.Int8{Int64: lease.Token, Valid: true}})
	}
	if e == nil {
		p.binding, e = q.SiteAccountStatsBinding(ctx, attempt.IndexerID)
	}
	if e == nil {
		e = p.validate(ctx, q)
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

func (p *siteAccountStatsPermit) Snapshot() ports.SiteAccountStatsExecution { return p.snapshot }

func (p *siteAccountStatsPermit) validate(ctx context.Context, q *db.Queries) error {
	if p.closed || p.terminal || p.conn.Conn().IsClosed() {
		return ports.ErrUnavailable
	}
	if e := statsRuntimeGuard(ctx, q); e != nil {
		return e
	}
	if _, e := q.LockJobForWrite(ctx, p.lease.ID); e != nil {
		return e
	}
	job, e := q.GetAuthorizedSearchJob(ctx, statsLeaseParams(p.lease))
	if e != nil {
		return ports.ErrUnavailable
	}
	var payload statsJobPayload
	c := p.snapshot.Config
	if statsDecode(job.Payload, &payload) != nil || payload != (statsJobPayload{c.IndexerID, c.Version, c.SourceConfigVersion}) || job.MaxAttempts != 1 {
		return ports.ErrUnavailable
	}
	current, e := p.store.material(ctx, q, c.IndexerID)
	if e != nil {
		return e
	}
	if !statsSame(current.Config, p.snapshot.Config) || current.SourceURL != p.snapshot.SourceURL || current.Cookie != p.snapshot.Cookie {
		return ports.ErrEndpointVersionConflict
	}
	schedule, e := q.GetSiteAccountStatsSchedule(ctx, c.IndexerID)
	if e != nil {
		return e
	}
	want := "queued"
	if p.reserved {
		want = "running"
	}
	if schedule.ConfigVersion != c.Version || !schedule.ActiveJobID.Valid || schedule.ActiveJobID.Bytes != p.lease.ID || schedule.State != want {
		return ports.ErrUnavailable
	}
	a, e := q.GetSiteAccountStatsAttempt(ctx, p.lease.ID)
	if e != nil {
		return e
	}
	if a.IndexerID != c.IndexerID || a.ConfigVersion != c.Version || a.SourceVersion != c.SourceConfigVersion || a.State != want || !a.ExecutionOwner.Valid || a.ExecutionOwner.String != p.lease.Owner || !a.ExecutionToken.Valid || a.ExecutionToken.Int64 != p.lease.Token {
		return ports.ErrUnavailable
	}
	held, e := q.CheckSiteAccountStatsGate(ctx, c.IndexerID)
	if e != nil {
		return e
	}
	if !held {
		return ports.ErrUnavailable
	}
	binding, e := q.ArmSiteAccountStatsCommit(ctx, c.IndexerID)
	if e != nil {
		return e
	}
	if binding != p.binding {
		return ports.ErrEndpointVersionConflict
	}
	// The clock is read again after all potentially blocking source/config locks.
	_, e = q.GetAuthorizedSearchJob(ctx, statsLeaseParams(p.lease))
	return e
}

func (p *siteAccountStatsPermit) Reserve(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	caller := ctx
	// Keep pgx cancellation from closing the physical source-lock session.
	ctx = context.WithoutCancel(ctx)
	if p.consumed || p.closed || p.terminal {
		return ports.ErrUnavailable
	}
	p.consumed = true
	tx, e := p.conn.Begin(ctx)
	if e != nil {
		return e
	}
	defer managementRollback(ctx, tx)
	q := db.New(tx)
	if e = p.validate(ctx, q); e != nil {
		return e
	}
	_, e = q.ReserveIndexerBudget(ctx, db.ReserveIndexerBudgetParams{ID: p.snapshot.Config.IndexerID, IntervalMs: 2000})
	if errors.Is(e, pgx.ErrNoRows) {
		source, err := q.LockRSSIndexer(ctx, p.snapshot.Config.IndexerID)
		if err != nil {
			return err
		}
		now, err := q.GetSearchDatabaseTime(ctx)
		if err != nil {
			return err
		}
		until := source.NextQueryAt.Time
		if source.RateLimitUntil.Valid && source.RateLimitUntil.Time.After(until) {
			until = source.RateLimitUntil.Time
		}
		return ports.NewRateLimitError(statsRetry(until.Sub(now.Time)), "site account statistics budget")
	}
	if e != nil {
		return e
	}
	if e = p.validate(ctx, q); e != nil {
		return e
	}
	if e = p.start(ctx, q); e != nil {
		return e
	}
	if _, e = q.GetAuthorizedSearchJob(ctx, statsLeaseParams(p.lease)); e != nil {
		return e
	}
	if err := caller.Err(); err != nil {
		return err
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	p.reserved = true
	return nil
}

func (p *siteAccountStatsPermit) start(ctx context.Context, q *db.Queries) error {
	n, e := q.StartSiteAccountStatsAttempt(ctx, db.StartSiteAccountStatsAttemptParams{JobID: p.lease.ID, ExecutionOwner: pgtype.Text{String: p.lease.Owner, Valid: true}, ExecutionToken: pgtype.Int8{Int64: p.lease.Token, Valid: true}})
	if e != nil {
		return e
	}
	if n != 1 {
		return ports.ErrUnavailable
	}
	n, e = q.StartSiteAccountStatsSchedule(ctx, db.StartSiteAccountStatsScheduleParams{IndexerID: p.snapshot.Config.IndexerID, ActiveJobID: pgtype.UUID{Bytes: p.lease.ID, Valid: true}, ConfigVersion: p.snapshot.Config.Version})
	if e != nil {
		return e
	}
	if n != 1 {
		return ports.ErrUnavailable
	}
	return nil
}

func (p *siteAccountStatsPermit) Check(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	caller := ctx
	// Keep pgx cancellation from closing the physical source-lock session.
	ctx = context.WithoutCancel(ctx)
	if p.closed || p.terminal {
		return ports.ErrUnavailable
	}
	tx, e := p.conn.Begin(ctx)
	if e != nil {
		return e
	}
	defer managementRollback(ctx, tx)
	if e = p.validate(ctx, db.New(tx)); e != nil {
		return e
	}
	if err := caller.Err(); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func statsRetry(d time.Duration) time.Duration {
	if d <= 0 {
		d = 10 * time.Second
	}
	if d < 2*time.Second {
		return 2 * time.Second
	}
	if d > 24*time.Hour {
		return 24 * time.Hour
	}
	return d
}
func statsFailure(e error) string {
	switch {
	case errors.Is(e, ports.ErrAuthRequired):
		return "auth_required"
	case errors.Is(e, ports.ErrBlocked):
		return "blocked"
	case errors.Is(e, ports.ErrBadResponse):
		return "bad_response"
	}
	if _, ok := ports.IsRateLimited(e); ok {
		return "rate_limited"
	}
	return "unavailable"
}

func (p *siteAccountStatsPermit) Finish(ctx context.Context, sample *ports.SiteAccountStats, resultError error) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	caller := ctx
	// Keep pgx cancellation from closing the physical source-lock session.
	ctx = context.WithoutCancel(ctx)
	if p.closed || p.terminal {
		return ports.ErrUnavailable
	}
	if resultError == nil && (!p.reserved || sample == nil) {
		return ports.ErrRejected
	}
	var raw []byte
	if resultError == nil {
		if indexer.ValidateSiteAccountStats(*sample) != nil || sample.AccountID != p.snapshot.Config.ExpectedAccountID {
			resultError = ports.ErrBadResponse
		} else {
			raw, _ = json.Marshal(sample)
		}
	}
	tx, e := p.conn.Begin(ctx)
	if e != nil {
		return e
	}
	defer managementRollback(ctx, tx)
	q := db.New(tx)
	if e = p.validate(ctx, q); e != nil {
		return e
	}
	if !p.reserved {
		if e = p.start(ctx, q); e != nil {
			return e
		}
	}
	state, jobState, code := "succeeded", "succeeded", ""
	interval := p.snapshot.Config.IntervalSeconds
	if resultError != nil {
		state, jobState, code = "failed", "dead", statsFailure(resultError)
		if rate, ok := ports.IsRateLimited(resultError); ok {
			retry := statsRetry(rate.RetryAfter)
			if e = q.DeferSiteAccountStatsBudget(ctx, db.DeferSiteAccountStatsBudgetParams{ID: p.snapshot.Config.IndexerID, RetryMs: retry.Milliseconds()}); e != nil {
				return e
			}
			interval = int32((retry + time.Second - 1) / time.Second)
		}
	}
	// Recheck full bindings before changing the terminal graph. Deferred checks use the commit clock.
	if p.reserved {
		if e = p.validate(ctx, q); e != nil {
			return e
		}
	}
	n, e := q.FinishSiteAccountStatsAttempt(ctx, db.FinishSiteAccountStatsAttemptParams{JobID: p.lease.ID, State: state, Sample: raw, FailureCode: code})
	if e != nil {
		return e
	}
	if n != 1 {
		return ports.ErrUnavailable
	}
	n, e = q.FinishSiteAccountStatsSchedule(ctx, db.FinishSiteAccountStatsScheduleParams{IndexerID: p.snapshot.Config.IndexerID, ActiveJobID: pgtype.UUID{Bytes: p.lease.ID, Valid: true}, ConfigVersion: p.snapshot.Config.Version, IntervalSeconds: interval})
	if e != nil {
		return e
	}
	if n != 1 {
		return ports.ErrUnavailable
	}
	n, e = q.FinishSiteAccountStatsJob(ctx, db.FinishSiteAccountStatsJobParams{ID: p.lease.ID, LeaseOwner: pgtype.Text{String: p.lease.Owner, Valid: true}, LeaseToken: p.lease.Token, State: jobState, Result: []byte("{}"), Error: pgtype.Text{String: code, Valid: code != ""}})
	if e != nil {
		return e
	}
	if n != 1 {
		return ports.ErrUnavailable
	}
	if e = statsRuntimeGuard(ctx, q); e != nil {
		return e
	}
	if err := caller.Err(); err != nil {
		return err
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	p.terminal = true
	return nil
}

func (p *siteAccountStatsPermit) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	p.closed = true
	cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	unlocked, e := db.New(p.conn).ReleaseSiteAccountStatsGate(cleanup, p.snapshot.Config.IndexerID)
	if e != nil || !unlocked {
		raw := p.conn.Hijack()
		_ = raw.Close(cleanup)
		return
	}
	p.conn.Release()
}
