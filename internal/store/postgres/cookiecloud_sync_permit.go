package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/cookies"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

var _ ports.CookieCloudSyncRepository = (*CookieCloudSyncStore)(nil)

type cookieCloudSource struct {
	row             db.Indexer
	configuration   db.IndexerConfiguration
	url, credential string
}

func (cookieCloudSource) String() string { return "CookieCloudSource{redacted}" }

type cookieCloudSyncPermit struct {
	store                                *CookieCloudSyncStore
	conn                                 *pgxpool.Conn
	lease                                ports.JobLease
	snapshot                             ports.CookieCloudExecution
	sources                              []cookieCloudSource
	binding                              string
	mu                                   sync.Mutex
	closed, consumed, reserved, terminal bool
}

func (*cookieCloudSyncPermit) String() string { return "CookieCloudPermit{redacted}" }

type cookieCloudSyncResult struct {
	Updated   int `json:"updated"`
	Unchanged int `json:"unchanged"`
	Unmatched int `json:"unmatched"`
	Rejected  int `json:"rejected"`
}

func cookieCloudLease(l ports.JobLease) db.GetAuthorizedSearchJobParams {
	return db.GetAuthorizedSearchJobParams{ID: l.ID, Kind: ports.CookieCloudSyncJobKind, LeaseOwner: pgtype.Text{String: l.Owner, Valid: true}, LeaseToken: l.Token}
}
func sameCookieCloudExecution(a, b ports.CookieCloudExecution) bool {
	return a.Connection == b.Connection && a.Config.OwnerID == b.Config.OwnerID && a.Config.Version == b.Config.Version && a.Config.Enabled == b.Config.Enabled && a.Config.IntervalSeconds == b.Config.IntervalSeconds && a.Config.ServerOrigin == b.Config.ServerOrigin && a.Config.HasConnection == b.Config.HasConnection && a.Config.UpdatedAt.Equal(b.Config.UpdatedAt)
}
func sameCookieCloudSources(a, b []cookieCloudSource) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		left, right := a[i], b[i]
		if left.row.ID != right.row.ID || left.row.ConfigVersion != right.row.ConfigVersion || left.row.Name != right.row.Name || left.row.Url != right.row.Url || !bytes.Equal(left.row.ApiKeyEncrypted, right.row.ApiKeyEncrypted) || !bytes.Equal(left.row.Categories, right.row.Categories) || !reflect.DeepEqual(left.configuration, right.configuration) || left.url != right.url || left.credential != right.credential {
			return false
		}
	}
	return true
}
func (s *CookieCloudSyncStore) sources(ctx context.Context, q *db.Queries) ([]cookieCloudSource, error) {
	rows, err := q.ListCookieCloudSources(ctx)
	if err != nil {
		return nil, err
	}
	if len(rows) > 10000 {
		return nil, ports.ErrRejected
	}
	out := make([]cookieCloudSource, 0, len(rows))
	for _, row := range rows {
		config, e := q.GetIndexerConfiguration(ctx, row.ID)
		if e != nil {
			return nil, e
		}
		if config.Protocol != string(ports.IndexerProtocolNexusPHP) || config.ConfigurationVersion != row.ConfigVersion {
			return nil, ports.ErrCookieCloudConfigConflict
		}
		// Refresh reads the original configuration; expired credentials must remain refreshable.
		resolved, modes, e := ResolveIndexerConfiguration(ctx, q, s.decoder, row)
		if e != nil {
			return nil, e
		}
		if modes.Protocol != ports.IndexerProtocolNexusPHP {
			return nil, ports.ErrRejected
		}
		credential, e := s.decoder.DecryptSecret(row.ApiKeyEncrypted)
		if e != nil {
			return nil, ports.ErrUnavailable
		}
		out = append(out, cookieCloudSource{row: row, configuration: config, url: resolved.Url, credential: string(credential)})
		clear(credential)
	}
	return out, nil
}

func (s *CookieCloudSyncStore) Acquire(ctx context.Context, lease ports.JobLease) (permit ports.CookieCloudSyncPermit, err error) {
	defer func() { err = cookieCloudRuntimeError(err) }()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if s == nil || s.pool == nil || s.ids == nil || s.decoder == nil || s.protector == nil {
		return nil, ports.ErrUnavailable
	}
	if lease.ID == uuid.Nil || lease.Owner == "" || lease.Token < 1 {
		return nil, ports.ErrCookieCloudSyncLeaseLost
	}
	attempt, err := db.New(s.pool).GetCookieCloudSyncAttempt(ctx, lease.ID)
	if err != nil {
		return nil, ports.ErrCookieCloudSyncLeaseLost
	}
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	p := &cookieCloudSyncPermit{store: s, conn: conn, lease: lease, snapshot: ports.CookieCloudExecution{Config: ports.CookieCloudConfigView{OwnerID: attempt.OwnerID}}}
	ok := false
	defer func() {
		if !ok {
			p.Close()
		}
	}()
	if err = db.New(conn).AcquireCookieCloudSyncGate(ctx, attempt.OwnerID); err != nil {
		return nil, err
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer managementRollback(ctx, tx)
	q := db.New(tx)
	if err = cookieCloudRuntimeGuard(ctx, q); err != nil {
		return nil, err
	}
	if _, err = q.LockJobForWrite(ctx, lease.ID); err != nil {
		return nil, err
	}
	if _, err = q.GetAuthorizedSearchJob(ctx, cookieCloudLease(lease)); err != nil {
		return nil, ports.ErrCookieCloudSyncLeaseLost
	}
	p.snapshot, err = s.execution(ctx, q, attempt.OwnerID)
	if err != nil {
		return nil, err
	}
	attempt, err = q.GetCookieCloudSyncAttempt(ctx, lease.ID)
	if err != nil {
		return nil, err
	}
	if attempt.State != "queued" || attempt.ExecutionOwner.Valid || attempt.ConfigVersion != p.snapshot.Config.Version {
		return nil, ports.ErrCookieCloudSyncLeaseLost
	}
	p.sources, err = s.sources(ctx, q)
	if err != nil {
		return nil, err
	}
	p.binding, err = q.CookieCloudSyncBinding(ctx, attempt.OwnerID)
	if err != nil {
		return nil, err
	}
	n, err := q.BindCookieCloudSyncAttempt(ctx, db.BindCookieCloudSyncAttemptParams{JobID: lease.ID, ExecutionOwner: pgtype.Text{String: lease.Owner, Valid: true}, ExecutionToken: pgtype.Int8{Int64: lease.Token, Valid: true}})
	if err != nil {
		return nil, err
	}
	if n != 1 {
		return nil, ports.ErrCookieCloudSyncLeaseLost
	}
	if err = p.validate(ctx, q); err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	ok = true
	return p, nil
}
func (p *cookieCloudSyncPermit) Snapshot() ports.CookieCloudExecution { return p.snapshot }
func (p *cookieCloudSyncPermit) validate(ctx context.Context, q *db.Queries) error {
	if p.closed || p.terminal || p.conn.Conn().IsClosed() {
		return ports.ErrCookieCloudSyncLeaseLost
	}
	if err := cookieCloudRuntimeGuard(ctx, q); err != nil {
		return err
	}
	if _, err := q.LockJobForWrite(ctx, p.lease.ID); err != nil {
		return err
	}
	job, err := q.GetAuthorizedSearchJob(ctx, cookieCloudLease(p.lease))
	if err != nil {
		return ports.ErrCookieCloudSyncLeaseLost
	}
	var payload cookieCloudJobPayload
	if statsDecode(job.Payload, &payload) != nil || payload != (cookieCloudJobPayload{p.snapshot.Config.OwnerID, p.snapshot.Config.Version}) || job.MaxAttempts != 1 {
		return ports.ErrCookieCloudSyncLeaseLost
	}
	current, err := p.store.execution(ctx, q, p.snapshot.Config.OwnerID)
	if err != nil {
		return err
	}
	if !sameCookieCloudExecution(current, p.snapshot) {
		return ports.ErrCookieCloudConfigConflict
	}
	sources, err := p.store.sources(ctx, q)
	if err != nil {
		return err
	}
	if !sameCookieCloudSources(sources, p.sources) {
		return ports.ErrCookieCloudConfigConflict
	}
	schedule, err := q.GetCookieCloudSyncSchedule(ctx, p.snapshot.Config.OwnerID)
	if err != nil {
		return err
	}
	state := "queued"
	if p.reserved {
		state = "running"
	}
	if schedule.ConfigVersion != p.snapshot.Config.Version || schedule.State != state || !schedule.ActiveJobID.Valid || schedule.ActiveJobID.Bytes != p.lease.ID {
		return ports.ErrCookieCloudSyncLeaseLost
	}
	attempt, err := q.GetCookieCloudSyncAttempt(ctx, p.lease.ID)
	if err != nil {
		return err
	}
	if attempt.OwnerID != p.snapshot.Config.OwnerID || attempt.ConfigVersion != p.snapshot.Config.Version || attempt.State != state || !attempt.ExecutionOwner.Valid || attempt.ExecutionOwner.String != p.lease.Owner || !attempt.ExecutionToken.Valid || attempt.ExecutionToken.Int64 != p.lease.Token || !attempt.ExecutionPid.Valid || uint32(attempt.ExecutionPid.Int32) != p.conn.Conn().PgConn().PID() {
		return ports.ErrCookieCloudSyncLeaseLost
	}
	held, err := q.CheckCookieCloudSyncGate(ctx, p.snapshot.Config.OwnerID)
	if err != nil {
		return err
	}
	if !held {
		return ports.ErrCookieCloudSyncLeaseLost
	}
	binding, err := q.ArmCookieCloudSyncCommit(ctx, p.snapshot.Config.OwnerID)
	if err != nil {
		return err
	}
	if binding != p.binding {
		return ports.ErrCookieCloudConfigConflict
	}
	_, err = q.GetAuthorizedSearchJob(ctx, cookieCloudLease(p.lease))
	if err != nil {
		return ports.ErrCookieCloudSyncLeaseLost
	}
	return nil
}
func (p *cookieCloudSyncPermit) Reserve(ctx context.Context) (err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	defer func() { err = cookieCloudRuntimeError(err) }()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if p.closed || p.terminal || p.consumed {
		return ports.ErrCookieCloudSyncLeaseLost
	}
	p.consumed = true
	caller := ctx
	ctx = context.WithoutCancel(ctx)
	tx, err := p.conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer managementRollback(ctx, tx)
	q := db.New(tx)
	if err = p.validate(ctx, q); err != nil {
		return err
	}
	n, err := q.StartCookieCloudSyncAttempt(ctx, db.StartCookieCloudSyncAttemptParams{JobID: p.lease.ID, ExecutionOwner: pgtype.Text{String: p.lease.Owner, Valid: true}, ExecutionToken: pgtype.Int8{Int64: p.lease.Token, Valid: true}})
	if err != nil {
		return err
	}
	if n != 1 {
		return ports.ErrCookieCloudSyncLeaseLost
	}
	n, err = q.StartCookieCloudSyncSchedule(ctx, db.StartCookieCloudSyncScheduleParams{OwnerID: p.snapshot.Config.OwnerID, ActiveJobID: pgtype.UUID{Bytes: p.lease.ID, Valid: true}, ConfigVersion: p.snapshot.Config.Version})
	if err != nil {
		return err
	}
	if n != 1 {
		return ports.ErrCookieCloudSyncLeaseLost
	}
	if caller.Err() != nil {
		return caller.Err()
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	p.reserved = true
	return nil
}
func (p *cookieCloudSyncPermit) Check(ctx context.Context) (err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	defer func() { err = cookieCloudRuntimeError(err) }()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if p.closed || p.terminal {
		return ports.ErrCookieCloudSyncLeaseLost
	}
	caller := ctx
	ctx = context.WithoutCancel(ctx)
	tx, err := p.conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer managementRollback(ctx, tx)
	if err = p.validate(ctx, db.New(tx)); err != nil {
		return err
	}
	if caller.Err() != nil {
		return caller.Err()
	}
	return tx.Commit(ctx)
}
func cloudCookies(backup *ports.CookieCloudBackup) ([]cookies.Cookie, error) {
	if backup == nil || backup.Cookies == nil {
		return nil, ports.ErrBadResponse
	}
	out := []cookies.Cookie{}
	for _, group := range backup.Cookies {
		for _, cookie := range group {
			if len(out) >= 10000 {
				return nil, ports.ErrBadResponse
			}
			out = append(out, cookies.Cookie{Name: cookie.Name, Value: cookie.Value, Domain: cookie.Domain, Path: cookie.Path, HostOnly: cookie.HostOnly, Secure: cookie.Secure, Session: cookie.Session, ExpirationDate: cookie.ExpirationDate, Partitioned: cookie.PartitionKey != nil})
		}
	}
	return out, nil
}
func (p *cookieCloudSyncPermit) sourceEvent(ctx context.Context, q *db.Queries, id uuid.UUID, version int64) error {
	action := "indexer.credentials_synchronized"
	payload, _ := json.Marshal(struct {
		IndexerID uuid.UUID `json:"indexer_id"`
		Version   int64     `json:"configuration_version"`
		JobID     uuid.UUID `json:"job_id"`
		OwnerID   uuid.UUID `json:"owner_id"`
	}{id, version, p.lease.ID, p.snapshot.Config.OwnerID})
	if _, err := q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: p.store.ids.NewUUID(), ActorType: string(ports.ActorTypeSystem), ActorID: "cookiecloud-scheduler", Action: action, TargetType: "indexer", TargetID: id.String(), Payload: payload}); err != nil {
		return err
	}
	aggregate := db.LockOutboxAggregateParams{AggregateType: "indexer", AggregateID: id.String()}
	if err := q.LockOutboxAggregate(ctx, aggregate); err != nil {
		return err
	}
	seq, err := q.GetNextAggregateSeq(ctx, db.GetNextAggregateSeqParams(aggregate))
	if err != nil {
		return err
	}
	_, err = q.InsertOutboxEvent(ctx, db.InsertOutboxEventParams{ID: p.store.ids.NewUUID(), AggregateType: aggregate.AggregateType, AggregateID: aggregate.AggregateID, Seq: int64(seq), EventType: action, Payload: payload})
	return err
}
func (p *cookieCloudSyncPermit) apply(ctx context.Context, q *db.Queries, observed []cookies.Cookie) (cookieCloudSyncResult, []cookieCloudSource, *time.Time, error) {
	var result cookieCloudSyncResult
	var validUntil *time.Time
	next := append([]cookieCloudSource(nil), p.sources...)
	for _, source := range p.sources {
		if err := q.LockCookieCloudSyncSourceGate(ctx, source.row.ID); err != nil {
			return result, nil, nil, err
		}
		if _, err := q.LockRSSIndexer(ctx, source.row.ID); err != nil {
			return result, nil, nil, err
		}
		if _, err := q.LockCookieCloudSyncSourceConfiguration(ctx, source.row.ID); err != nil {
			return result, nil, nil, err
		}
	}
	if err := p.validate(ctx, q); err != nil {
		return result, nil, nil, err
	}
	now, err := q.GetSearchDatabaseTime(ctx)
	if err != nil || !now.Valid {
		return result, nil, nil, ports.ErrUnavailable
	}
	for i, source := range p.sources {
		selection, e := cookies.SelectSharedOrigin(source.url, observed, now.Time)
		if e != nil {
			result.Rejected++
			continue
		}
		if selection.Count == 0 {
			result.Unmatched++
			continue
		}
		if selection.ValidUntil != nil && (validUntil == nil || selection.ValidUntil.Before(*validUntil)) {
			deadline := selection.ValidUntil.UTC().Truncate(time.Microsecond)
			validUntil = &deadline
		}
		if selection.Header == source.credential {
			if e := recordCookieCloudCredentialEvidence(ctx, q, p.lease.ID, source.row.ID, source.row.ConfigVersion, selection.ValidUntil); e != nil {
				return result, nil, nil, e
			}
			result.Unchanged++
			continue
		}
		encrypted, e := p.store.protector.EncryptSecret([]byte(selection.Header))
		if e != nil || len(encrypted) == 0 {
			return result, nil, nil, ports.ErrUnavailable
		}
		row, e := q.UpdateCookieCloudSourceCredential(ctx, db.UpdateCookieCloudSourceCredentialParams{IndexerID: source.row.ID, ExpectedVersion: source.row.ConfigVersion, CredentialCiphertext: encrypted})
		if e != nil {
			return result, nil, nil, e
		}
		if row.ConfigVersion != source.row.ConfigVersion+1 || row.Name != source.row.Name || row.Url != source.row.Url || !bytes.Equal(row.Categories, source.row.Categories) || !bytes.Equal(row.ApiKeyEncrypted, encrypted) {
			return result, nil, nil, ports.ErrCookieCloudConfigConflict
		}
		n, e := q.AdvanceCookieCloudSourceConfiguration(ctx, db.AdvanceCookieCloudSourceConfigurationParams{IndexerID: source.row.ID, ExpectedVersion: source.configuration.ConfigurationVersion, NewVersion: row.ConfigVersion})
		if e != nil {
			return result, nil, nil, e
		}
		if n != 1 {
			return result, nil, nil, ports.ErrCookieCloudConfigConflict
		}
		if e := recordCookieCloudCredentialEvidence(ctx, q, p.lease.ID, row.ID, row.ConfigVersion, selection.ValidUntil); e != nil {
			return result, nil, nil, e
		}
		next[i].row = row
		next[i].configuration.ConfigurationVersion = row.ConfigVersion
		next[i].credential = selection.Header
		if e = p.sourceEvent(ctx, q, row.ID, row.ConfigVersion); e != nil {
			return result, nil, nil, e
		}
		result.Updated++
	}
	current, err := p.store.sources(ctx, q)
	if err != nil {
		return result, nil, nil, err
	}
	if !sameCookieCloudSources(next, current) {
		return result, nil, nil, ports.ErrCookieCloudConfigConflict
	}
	return result, next, validUntil, nil
}
func cloudFailure(err error) string {
	switch {
	case errors.Is(err, ports.ErrAuthRequired):
		return "auth_required"
	case errors.Is(err, ports.ErrBlocked):
		return "blocked"
	case errors.Is(err, ports.ErrBadResponse):
		return "bad_response"
	case errors.Is(err, ports.ErrRejected):
		return "rejected"
	case errors.Is(err, ports.ErrForbidden):
		return "forbidden"
	case errors.Is(err, ports.ErrCookieCloudConfigConflict):
		return "configuration_changed"
	case errors.Is(err, ports.ErrCookieCloudSyncLeaseLost):
		return "lease_lost"
	}
	if _, ok := ports.IsRateLimited(err); ok {
		return "rate_limited"
	}
	return "unavailable"
}
func (p *cookieCloudSyncPermit) Finish(ctx context.Context, backup *ports.CookieCloudBackup, failure error) (err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	defer func() { err = cookieCloudRuntimeError(err) }()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if p.closed || p.terminal || !p.reserved {
		return ports.ErrCookieCloudSyncLeaseLost
	}
	caller := ctx
	ctx = context.WithoutCancel(ctx)
	tx, err := p.conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer managementRollback(ctx, tx)
	q := db.New(tx)
	if err = p.validate(ctx, q); err != nil {
		return err
	}
	state, jobState, code := "succeeded", "succeeded", ""
	interval := p.snapshot.Config.IntervalSeconds
	var raw []byte
	var deadline *time.Time
	expected := p.sources
	if failure == nil {
		observed, e := cloudCookies(backup)
		if e != nil {
			return e
		}
		var result cookieCloudSyncResult
		result, expected, deadline, err = p.apply(ctx, q, observed)
		if err != nil {
			return err
		}
		raw, _ = json.Marshal(result)
	} else {
		state, jobState, code = "failed", "dead", cloudFailure(failure)
		if limited, ok := ports.IsRateLimited(failure); ok {
			retry := limited.RetryAfter
			if retry > time.Hour {
				retry = time.Hour
			}
			if retry > 0 {
				seconds := int32((retry + time.Second - 1) / time.Second)
				if seconds > interval {
					interval = seconds
				}
			}
		}
	}
	armed, err := q.ArmCookieCloudSyncCommit(ctx, p.snapshot.Config.OwnerID)
	if err != nil {
		return err
	}
	until := pgtype.Timestamptz{}
	if deadline != nil {
		until = pgtype.Timestamptz{Time: *deadline, Valid: true}
	}
	n, err := q.FinishCookieCloudSyncAttempt(ctx, db.FinishCookieCloudSyncAttemptParams{JobID: p.lease.ID, State: state, Result: raw, FailureCode: code, ValidUntil: until})
	if err != nil {
		return err
	}
	if n != 1 {
		return ports.ErrCookieCloudSyncLeaseLost
	}
	n, err = q.FinishCookieCloudSyncSchedule(ctx, db.FinishCookieCloudSyncScheduleParams{OwnerID: p.snapshot.Config.OwnerID, ActiveJobID: pgtype.UUID{Bytes: p.lease.ID, Valid: true}, ConfigVersion: p.snapshot.Config.Version, IntervalSeconds: interval})
	if err != nil {
		return err
	}
	if n != 1 {
		return ports.ErrCookieCloudSyncLeaseLost
	}
	n, err = q.FinishCookieCloudSyncJob(ctx, db.FinishCookieCloudSyncJobParams{ID: p.lease.ID, LeaseOwner: pgtype.Text{String: p.lease.Owner, Valid: true}, LeaseToken: p.lease.Token, State: jobState, Result: raw, Error: pgtype.Text{String: code, Valid: code != ""}})
	if err != nil {
		return err
	}
	if n != 1 {
		return ports.ErrCookieCloudSyncLeaseLost
	}
	if err = p.store.event(ctx, q, p.snapshot.Config.OwnerID, p.lease.ID, p.snapshot.Config.Version, "cookiecloud.sync_"+state); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE"); err != nil {
		return err
	}
	if err = cookieCloudRuntimeGuard(ctx, q); err != nil {
		return err
	}
	current, err := p.store.execution(ctx, q, p.snapshot.Config.OwnerID)
	if err != nil {
		return err
	}
	if !sameCookieCloudExecution(current, p.snapshot) {
		return ports.ErrCookieCloudConfigConflict
	}
	sources, err := p.store.sources(ctx, q)
	if err != nil {
		return err
	}
	if !sameCookieCloudSources(sources, expected) {
		return ports.ErrCookieCloudConfigConflict
	}
	binding, err := q.CookieCloudSyncBinding(ctx, p.snapshot.Config.OwnerID)
	if err != nil {
		return err
	}
	if binding != armed {
		return ports.ErrCookieCloudConfigConflict
	}
	held, err := q.CheckCookieCloudSyncGate(ctx, p.snapshot.Config.OwnerID)
	if err != nil || !held {
		return ports.ErrCookieCloudSyncLeaseLost
	}
	job, err := q.GetJobByID(ctx, p.lease.ID)
	if err != nil {
		return err
	}
	now, err := q.GetSearchDatabaseTime(ctx)
	if err != nil || !now.Valid {
		return ports.ErrUnavailable
	}
	if job.State != jobState || job.Kind != ports.CookieCloudSyncJobKind || !job.LeaseOwner.Valid || job.LeaseOwner.String != p.lease.Owner || job.LeaseToken != p.lease.Token || !job.LeaseUntil.Valid || !job.LeaseUntil.Time.After(now.Time) || (deadline != nil && !deadline.After(now.Time)) {
		return ports.ErrCookieCloudSyncLeaseLost
	}
	if caller.Err() != nil {
		return caller.Err()
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	p.terminal = true
	return nil
}
func (p *cookieCloudSyncPermit) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	p.closed = true
	cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	released, err := db.New(p.conn).ReleaseCookieCloudSyncGate(cleanup, p.snapshot.Config.OwnerID)
	if err != nil || !released {
		raw := p.conn.Hijack()
		_ = raw.Close(cleanup)
		return
	}
	p.conn.Release()
}
