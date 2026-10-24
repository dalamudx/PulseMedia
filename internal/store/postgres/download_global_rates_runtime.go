package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type DownloadGlobalRatesStore struct {
	pool    *pgxpool.Pool
	ids     ports.IDGen
	decoder ports.CredentialDecoder
}

var _ ports.DownloadGlobalRatesRepository = (*DownloadGlobalRatesStore)(nil)

func NewDownloadGlobalRatesStore(pool *pgxpool.Pool, ids ports.IDGen, decoder ports.CredentialDecoder) *DownloadGlobalRatesStore {
	return &DownloadGlobalRatesStore{pool: pool, ids: ids, decoder: decoder}
}

type globalRatesPayload struct {
	ClientID    uuid.UUID `json:"client_id"`
	ConfigStamp string    `json:"config_stamp"`
}

func (s *DownloadGlobalRatesStore) EnqueueDue(ctx context.Context) error {
	if s == nil || s.pool == nil || s.ids == nil {
		return ports.ErrUnavailable
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer managementRollback(ctx, tx)
	q := db.New(tx)
	if err = statsRuntimeGuard(ctx, q); err != nil {
		return err
	}
	if err = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "download_global_rates_scheduler", AggregateID: "global"}); err != nil {
		return err
	}
	ids, err := q.ListDueDownloadGlobalRates(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err = q.LockDownloadClientForEdit(ctx, id); errors.Is(err, pgx.ErrNoRows) {
			continue
		} else if err != nil {
			return err
		}
		schedule, e := q.GetDownloadGlobalRatesSchedule(ctx, id)
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		if e == nil && schedule.ActiveJobID.Valid {
			continue
		}
		stamp, e := q.DownloadGlobalRatesBinding(ctx, id)
		if e != nil {
			return e
		}
		if len(stamp) != 64 {
			return ports.ErrUnavailable
		}
		jobID := s.ids.NewUUID()
		raw, e := json.Marshal(globalRatesPayload{ClientID: id, ConfigStamp: stamp})
		if e != nil {
			return ports.ErrUnavailable
		}
		if _, e = q.CreateDownloadGlobalRatesJob(ctx, db.CreateDownloadGlobalRatesJobParams{ID: jobID, DedupeKey: pgtype.Text{String: fmt.Sprintf("download-global-rates:%s:%s", id, jobID), Valid: true}, Payload: raw}); e != nil {
			return e
		}
		if e = q.CreateDownloadGlobalRatesAttempt(ctx, db.CreateDownloadGlobalRatesAttemptParams{JobID: jobID, ClientID: id, ConfigStamp: stamp}); e != nil {
			return e
		}
		if e = q.QueueDownloadGlobalRatesSchedule(ctx, db.QueueDownloadGlobalRatesScheduleParams{ClientID: id, ActiveJobID: pgtype.UUID{Bytes: jobID, Valid: true}}); e != nil {
			return e
		}
	}
	if err = statsRuntimeGuard(ctx, q); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *DownloadGlobalRatesStore) RecoverExpired(ctx context.Context) error {
	if s == nil || s.pool == nil {
		return ports.ErrUnavailable
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer managementRollback(ctx, tx)
	q := db.New(tx)
	if err = statsRuntimeGuard(ctx, q); err != nil {
		return err
	}
	rows, err := q.ListExpiredDownloadGlobalRates(ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		// A logically expired job cannot release a still-running physical call.
		held, e := q.TryDownloadGlobalRatesRecoveryGate(ctx, row.ClientID)
		if e != nil {
			return e
		}
		if !held {
			continue
		}
		n, e := q.AbandonDownloadGlobalRatesAttempt(ctx, row.JobID)
		if e != nil {
			return e
		}
		if n != 1 {
			return ports.ErrUnavailable
		}
		if _, e = q.AbandonDownloadGlobalRatesSchedule(ctx, db.AbandonDownloadGlobalRatesScheduleParams{ClientID: row.ClientID, LatestJobID: pgtype.UUID{Bytes: row.JobID, Valid: true}}); e != nil {
			return e
		}
		if _, e = q.AbandonDownloadGlobalRatesJob(ctx, row.JobID); e != nil {
			return e
		}
	}
	if err = statsRuntimeGuard(ctx, q); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type downloadGlobalRatesPermit struct {
	store            *DownloadGlobalRatesStore
	conn             *pgxpool.Conn
	lease            ports.JobLease
	source           ports.DownloadGlobalRatesSource
	mu               sync.Mutex
	closed, terminal bool
}

func globalRatesLeaseParams(l ports.JobLease) db.GetAuthorizedSearchJobParams {
	return db.GetAuthorizedSearchJobParams{ID: l.ID, Kind: ports.DownloadGlobalRatesJobKind, LeaseOwner: pgtype.Text{String: l.Owner, Valid: true}, LeaseToken: l.Token}
}

func (s *DownloadGlobalRatesStore) material(ctx context.Context, q *db.Queries, id uuid.UUID) (ports.DownloadGlobalRatesSource, error) {
	var out ports.DownloadGlobalRatesSource
	client, err := q.LockDownloadClientForEdit(ctx, id)
	if err != nil {
		return out, err
	}
	if client.ClientType != "qbittorrent" && client.ClientType != "transmission" {
		return out, ports.ErrRejected
	}
	stamp, err := q.DownloadGlobalRatesBinding(ctx, id)
	if err != nil {
		return out, err
	}
	if len(stamp) != 64 {
		return out, ports.ErrEndpointVersionConflict
	}
	if s.decoder == nil {
		return out, ports.ErrUnavailable
	}
	password, err := s.decoder.DecryptSecret(client.PasswordEncrypted)
	if err != nil {
		return out, ports.ErrUnavailable
	}
	return ports.DownloadGlobalRatesSource{ClientID: id, ConfigStamp: stamp, ClientType: client.ClientType, URL: client.Url, Username: client.Username, Password: string(password)}, nil
}

func (s *DownloadGlobalRatesStore) Acquire(ctx context.Context, lease ports.JobLease) (ports.DownloadGlobalRatesPermit, error) {
	if s == nil || s.pool == nil || lease.ID == uuid.Nil || lease.Owner == "" || lease.Token < 1 {
		return nil, ports.ErrRejected
	}
	q := db.New(s.pool)
	if _, err := q.GetAuthorizedSearchJob(ctx, globalRatesLeaseParams(lease)); err != nil {
		return nil, ports.ErrStaleDeliveryLease
	}
	attempt, err := q.GetDownloadGlobalRatesAttempt(ctx, lease.ID)
	if err != nil {
		return nil, err
	}
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	p := &downloadGlobalRatesPermit{store: s, conn: conn, lease: lease, source: ports.DownloadGlobalRatesSource{ClientID: attempt.ClientID}}
	if err = db.New(conn).AcquireDownloadGlobalRatesGate(ctx, attempt.ClientID); err != nil {
		p.Close()
		return nil, err
	}
	// Finish/Check never run a cancellable query on this session while I/O is alive.
	// Acquire is still before native I/O, so cancellation here may safely discard it.
	err = func() error {
		tx, e := conn.Begin(ctx)
		if e != nil {
			return e
		}
		defer managementRollback(ctx, tx)
		q := db.New(tx)
		if e = statsRuntimeGuard(ctx, q); e != nil {
			return e
		}
		if _, e = q.LockJobForWrite(ctx, lease.ID); e != nil {
			return e
		}
		if _, e = q.GetAuthorizedSearchJob(ctx, globalRatesLeaseParams(lease)); e != nil {
			return ports.ErrStaleDeliveryLease
		}
		p.source, e = s.material(ctx, q, attempt.ClientID)
		if e != nil {
			return e
		}
		current, e := q.GetDownloadGlobalRatesAttempt(ctx, lease.ID)
		if e != nil {
			return e
		}
		if current.State != "queued" || current.ExecutionOwner.Valid || current.ConfigStamp != p.source.ConfigStamp {
			return ports.ErrEndpointVersionConflict
		}
		n, e := q.StartDownloadGlobalRatesAttempt(ctx, db.StartDownloadGlobalRatesAttemptParams{JobID: lease.ID, ExecutionOwner: pgtype.Text{String: lease.Owner, Valid: true}, ExecutionToken: pgtype.Int8{Int64: lease.Token, Valid: true}})
		if e != nil {
			return e
		}
		if n != 1 {
			return ports.ErrStaleDeliveryLease
		}
		if e = p.validate(ctx, q); e != nil {
			return e
		}
		return tx.Commit(ctx)
	}()
	if err != nil {
		p.Close()
		return nil, err
	}
	return p, nil
}

func (p *downloadGlobalRatesPermit) Snapshot() ports.DownloadGlobalRatesSource { return p.source }

func (p *downloadGlobalRatesPermit) validate(ctx context.Context, q *db.Queries) error {
	if p.closed || p.terminal || p.conn.Conn().IsClosed() {
		return ports.ErrUnavailable
	}
	if err := statsRuntimeGuard(ctx, q); err != nil {
		return err
	}
	if _, err := q.LockJobForWrite(ctx, p.lease.ID); err != nil {
		return err
	}
	job, err := q.GetAuthorizedSearchJob(ctx, globalRatesLeaseParams(p.lease))
	if err != nil {
		return ports.ErrStaleDeliveryLease
	}
	var payload globalRatesPayload
	if statsDecode(job.Payload, &payload) != nil || payload != (globalRatesPayload{ClientID: p.source.ClientID, ConfigStamp: p.source.ConfigStamp}) || job.MaxAttempts != 1 {
		return ports.ErrRejected
	}
	current, err := p.store.material(ctx, q, p.source.ClientID)
	if err != nil {
		return err
	}
	if current != p.source {
		return ports.ErrEndpointVersionConflict
	}
	attempt, err := q.GetDownloadGlobalRatesAttempt(ctx, p.lease.ID)
	if err != nil {
		return err
	}
	if attempt.ClientID != p.source.ClientID || attempt.ConfigStamp != p.source.ConfigStamp || attempt.State != "running" || !attempt.ExecutionOwner.Valid || attempt.ExecutionOwner.String != p.lease.Owner || !attempt.ExecutionToken.Valid || attempt.ExecutionToken.Int64 != p.lease.Token {
		return ports.ErrStaleDeliveryLease
	}
	schedule, err := q.GetDownloadGlobalRatesSchedule(ctx, p.source.ClientID)
	if err != nil {
		return err
	}
	if !schedule.ActiveJobID.Valid || schedule.ActiveJobID.Bytes != p.lease.ID {
		return ports.ErrStaleDeliveryLease
	}
	held, err := q.CheckDownloadGlobalRatesGate(ctx, p.source.ClientID)
	if err != nil {
		return err
	}
	if !held {
		return ports.ErrUnavailable
	}
	_, err = q.GetAuthorizedSearchJob(ctx, globalRatesLeaseParams(p.lease))
	if err != nil {
		return ports.ErrStaleDeliveryLease
	}
	return nil
}

func (p *downloadGlobalRatesPermit) Check(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	caller := ctx
	ctx = context.WithoutCancel(ctx)
	if p.closed || p.terminal {
		return ports.ErrUnavailable
	}
	tx, err := p.conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer managementRollback(ctx, tx)
	if err = p.validate(ctx, db.New(tx)); err != nil {
		return err
	}
	if err = caller.Err(); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func validGlobalRateObservation(o ports.DownloadGlobalRatesObservation) bool {
	if o.ObservedAt.IsZero() || o.RetryAfter < 0 || o.RetryAfter > 24*time.Hour {
		return false
	}
	if o.Failure == ports.DownloadGlobalRatesOK {
		return o.Rates != nil && o.RetryAfter == 0 && (o.Rates.DownloadBytesPerSecond == nil || *o.Rates.DownloadBytesPerSecond >= 0) && (o.Rates.UploadBytesPerSecond == nil || *o.Rates.UploadBytesPerSecond >= 0)
	}
	if o.Rates != nil {
		return false
	}
	switch o.Failure {
	case ports.DownloadGlobalRatesUnavailable, ports.DownloadGlobalRatesAuth, ports.DownloadGlobalRatesTimeout, ports.DownloadGlobalRatesBlocked, ports.DownloadGlobalRatesBadResponse, ports.DownloadGlobalRatesLimited:
		return true
	default:
		return false
	}
}

func (p *downloadGlobalRatesPermit) Finish(ctx context.Context, o ports.DownloadGlobalRatesObservation) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validGlobalRateObservation(o) {
		return ports.ErrRejected
	}
	caller := ctx
	ctx = context.WithoutCancel(ctx)
	if p.closed || p.terminal {
		return ports.ErrUnavailable
	}
	tx, err := p.conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer managementRollback(ctx, tx)
	q := db.New(tx)
	if err = p.validate(ctx, q); err != nil {
		return err
	}
	state := "succeeded"
	if o.Failure != ports.DownloadGlobalRatesOK {
		state = "failed"
	}
	params := db.FinishDownloadGlobalRatesAttemptParams{JobID: p.lease.ID, State: state, ObservedAt: pgtype.Timestamptz{Time: o.ObservedAt.UTC(), Valid: true}, FailureCode: string(o.Failure)}
	if o.Rates != nil {
		if o.Rates.DownloadBytesPerSecond != nil {
			params.DownloadBytesPerSecond = pgtype.Int8{Int64: *o.Rates.DownloadBytesPerSecond, Valid: true}
		}
		if o.Rates.UploadBytesPerSecond != nil {
			params.UploadBytesPerSecond = pgtype.Int8{Int64: *o.Rates.UploadBytesPerSecond, Valid: true}
		}
	}
	n, err := q.FinishDownloadGlobalRatesAttempt(ctx, params)
	if err != nil {
		return err
	}
	if n != 1 {
		return ports.ErrStaleDeliveryLease
	}
	delay := 10 * time.Second
	if o.RetryAfter > delay {
		delay = o.RetryAfter
	}
	n, err = q.FinishDownloadGlobalRatesSchedule(ctx, db.FinishDownloadGlobalRatesScheduleParams{ClientID: p.source.ClientID, JobID: pgtype.UUID{Bytes: p.lease.ID, Valid: true}, Success: state == "succeeded", DelayMs: delay.Milliseconds()})
	if err != nil {
		return err
	}
	if n != 1 {
		return ports.ErrStaleDeliveryLease
	}
	n, err = q.FinishDownloadGlobalRatesJob(ctx, db.FinishDownloadGlobalRatesJobParams{ID: p.lease.ID, LeaseOwner: pgtype.Text{String: p.lease.Owner, Valid: true}, LeaseToken: p.lease.Token})
	if err != nil {
		return err
	}
	if n != 1 {
		return ports.ErrStaleDeliveryLease
	}
	if err = statsRuntimeGuard(ctx, q); err != nil {
		return err
	}
	if err = caller.Err(); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	p.terminal = true
	return nil
}

func (p *downloadGlobalRatesPermit) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	p.closed = true
	cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	released, err := db.New(p.conn).ReleaseDownloadGlobalRatesGate(cleanup, p.source.ClientID)
	if err != nil || !released {
		raw := p.conn.Hijack()
		_ = raw.Close(cleanup)
		return
	}
	p.conn.Release()
}
