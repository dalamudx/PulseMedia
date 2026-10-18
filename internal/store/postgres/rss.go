package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"time"
)

type RSSStore struct {
	pool *pgxpool.Pool
	q    *db.Queries
	ids  ports.IDGen
}

var _ ports.RSSRepository = (*RSSStore)(nil)

func NewRSSStore(pool *pgxpool.Pool, ids ports.IDGen) *RSSStore {
	return &RSSStore{pool, db.New(pool), ids}
}
func rssInfo(row db.Indexer) ports.RSSInfo {
	r := ports.RSSInfo{IndexerID: row.ID, Enabled: row.RssEnabled, Kind: row.RssKind, IntervalSeconds: row.RssIntervalSeconds, ConfigVersion: row.ConfigVersion, NextPollAt: row.RssNextPollAt.Time}
	if row.RssLastQueryID.Valid {
		id := uuid.UUID(row.RssLastQueryID.Bytes)
		r.LastQueryID = &id
	}
	if row.RssLastSuccessAt.Valid {
		t := row.RssLastSuccessAt.Time
		r.LastSuccessAt = &t
	}
	return r
}
func (f *RSSStore) GetRSS(ctx context.Context, id uuid.UUID) (*ports.RSSInfo, error) {
	r, e := f.q.GetIndexerByID(ctx, id)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, ports.ErrNotFound
	}
	if e != nil {
		return nil, e
	}
	info := rssInfo(r)
	if info.LastQueryID != nil {
		n, e := f.q.CountRSSQueryCandidates(ctx, *info.LastQueryID)
		if e != nil {
			return nil, e
		}
		info.LastCandidateCount = &n
	}
	return &info, nil
}
func (f *RSSStore) ConfigureRSS(ctx context.Context, p ports.PreparedRSSConfig) (*ports.RSSInfo, error) {
	a, id, c, key, hash, cipher := p.Actor, p.IndexerID, p.Settings, p.IdempotencyKey, p.RequestHash, p.EncryptedURL
	if a == nil || !a.IsAdmin() {
		return nil, ports.ErrForbidden
	}
	tx, e := f.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := f.q.WithTx(tx)
	lock, _ := json.Marshal([]string{string(a.Type), a.ID, key})
	if e = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "indexer.rss.configure", AggregateID: string(lock)}); e != nil {
		return nil, e
	}
	old, e := q.GetIdempotencyRecord(ctx, db.GetIdempotencyRecordParams{ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: key})
	if e == nil {
		if old.RequestHash != hash {
			return nil, ports.ErrRSSConfigConflict
		}
		var r ports.RSSInfo
		if json.Unmarshal(old.ResponseBody, &r) != nil {
			return nil, ports.ErrRSSConfigConflict
		}
		return &r, nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return nil, e
	}
	row, e := q.LockRSSIndexer(ctx, id)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, ports.ErrNotFound
	}
	if e != nil {
		return nil, e
	}
	if row.ConfigVersion != c.ExpectedVersion {
		return nil, ports.ErrRSSConfigConflict
	}
	row, e = q.ConfigureIndexerRSS(ctx, db.ConfigureIndexerRSSParams{ID: id, RssEnabled: c.Enabled, RssKind: c.Kind, RssUrlEncrypted: cipher, RssIntervalSeconds: c.IntervalSeconds, ConfigVersion: c.ExpectedVersion})
	if e != nil {
		return nil, e
	}
	if e = q.CancelOldRSSJobs(ctx, id.String()); e != nil {
		return nil, e
	}
	info := rssInfo(row)
	safe, _ := json.Marshal(info)
	if _, e = q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: f.ids.NewUUID(), ActorType: string(a.Type), ActorID: a.ID, Action: "indexer.rss_configured", TargetType: "indexer", TargetID: id.String(), Payload: safe}); e != nil {
		return nil, e
	}
	if e = f.publish(ctx, q, id, "indexer.rss_configured", safe); e != nil {
		return nil, e
	}
	if _, e = q.CreateIdempotencyRecord(ctx, db.CreateIdempotencyRecordParams{ID: f.ids.NewUUID(), ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: key, RequestHash: hash, ResponseCode: 200, ResponseBody: safe, ResourceID: pgtype.Text{String: id.String(), Valid: true}}); e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return &info, nil
}
func (f *RSSStore) QueueDue(ctx context.Context, limit int32) (int, error) {
	tx, e := f.pool.Begin(ctx)
	if e != nil {
		return 0, e
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := f.q.WithTx(tx)
	rows, e := q.ListDueRSSIndexers(ctx, limit)
	if e != nil {
		return 0, e
	}
	n := 0
	for _, r := range rows {
		count, e := q.QueueRSSPoll(ctx, db.QueueRSSPollParams{ID: f.ids.NewUUID(), ID_2: r.ID})
		if e != nil {
			return 0, e
		}
		n += int(count)
		if e = q.AdvanceRSSSchedule(ctx, r.ID); e != nil {
			return 0, e
		}
	}
	if e = tx.Commit(ctx); e != nil {
		return 0, e
	}
	return n, nil
}
func (f *RSSStore) authorize(ctx context.Context, q *db.Queries, p ports.RSSPollJob) error {
	job, e := q.GetAuthorizedSearchJob(ctx, db.GetAuthorizedSearchJobParams{ID: p.ID, Kind: "rss_poll", LeaseOwner: pgtype.Text{String: p.Owner, Valid: true}, LeaseToken: p.Token})
	if e != nil {
		return e
	}
	var stored ports.RSSPollJob
	if json.Unmarshal(job.Payload, &stored) != nil || stored.IndexerID != p.IndexerID || stored.ConfigVersion != p.ConfigVersion {
		return ports.ErrRSSConfigConflict
	}
	return nil
}
func (f *RSSStore) CheckRSS(ctx context.Context, p ports.RSSPollJob) (bool, error) {
	if e := f.authorize(ctx, f.q, p); e != nil {
		return false, e
	}
	r, e := f.q.GetIndexerByID(ctx, p.IndexerID)
	if e != nil {
		return false, e
	}
	return !r.RssEnabled || r.ConfigVersion != p.ConfigVersion || r.Health != "online", nil
}
func (f *RSSStore) BindRSSQuery(ctx context.Context, p ports.RSSPollJob, id uuid.UUID) error {
	tx, e := f.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := f.q.WithTx(tx)
	if _, e = q.LockJobForWrite(ctx, p.ID); e != nil {
		return e
	}
	if e = f.authorize(ctx, q, p); e != nil {
		return e
	}
	if e = q.BindSharedRSSQuery(ctx, db.BindSharedRSSQueryParams{ID: id, RssJobID: pgtype.UUID{Bytes: p.ID, Valid: true}}); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (f *RSSStore) CompleteRSS(ctx context.Context, p ports.RSSPollJob, queryID *uuid.UUID) error {
	tx, e := f.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := f.q.WithTx(tx)
	row, e := q.LockRSSIndexer(ctx, p.IndexerID)
	if e != nil {
		return e
	}
	if _, e = q.LockJobForWrite(ctx, p.ID); e != nil {
		return e
	}
	if e = f.authorize(ctx, q, p); e != nil {
		return e
	}
	if queryID != nil {
		if !row.RssEnabled || row.ConfigVersion != p.ConfigVersion || row.Health != "online" {
			return ports.ErrRSSConfigConflict
		}
		execution, e := q.GetQueryExecution(ctx, *queryID)
		if e != nil {
			return e
		}
		if execution.State != "succeeded" || execution.QueryType != "rss" || !execution.RssJobID.Valid || uuid.UUID(execution.RssJobID.Bytes) != p.ID || execution.IndexerID != row.ID || execution.ConfigVersion != p.ConfigVersion {
			return ports.ErrRSSConfigConflict
		}
		if _, e = q.AcknowledgeRSSQuery(ctx, db.AcknowledgeRSSQueryParams{ID: row.ID, RssLastQueryID: pgtype.UUID{Bytes: *queryID, Valid: true}, ConfigVersion: p.ConfigVersion}); e != nil {
			return e
		}
		if e = q.AttachRSSToRunningRounds(ctx, *queryID); e != nil {
			return e
		}
		count, e := q.CountRSSQueryCandidates(ctx, *queryID)
		if e != nil {
			return e
		}
		safe, _ := json.Marshal(map[string]any{"query_id": queryID, "candidate_count": count})
		if _, e = q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: f.ids.NewUUID(), ActorType: "system", ActorID: p.Owner, Action: "indexer.rss_received", TargetType: "indexer", TargetID: row.ID.String(), Payload: safe}); e != nil {
			return e
		}
		if e = f.publish(ctx, q, row.ID, "indexer.rss_received", safe); e != nil {
			return e
		}
	}
	if _, e = q.FinishJob(ctx, db.FinishJobParams{Column1: p.ID, Column2: p.Owner, Column3: p.Token, Column4: []byte(`{}`)}); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (f *RSSStore) RetryRSSDelay(ctx context.Context, id uuid.UUID) time.Duration {
	delay := 5 * time.Second
	row, e := f.q.GetIndexerByID(ctx, id)
	if e != nil {
		return delay
	}
	now, e := f.q.GetSearchDatabaseTime(ctx)
	if e != nil {
		return delay
	}
	for _, t := range []pgtype.Timestamptz{row.NextQueryAt, row.RateLimitUntil} {
		if t.Valid && t.Time.Sub(now.Time) > delay {
			delay = t.Time.Sub(now.Time)
		}
	}
	return delay
}
func (f *RSSStore) publish(ctx context.Context, q *db.Queries, id uuid.UUID, event string, payload []byte) error {
	if e := q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "indexer", AggregateID: id.String()}); e != nil {
		return e
	}
	seq, e := q.GetNextAggregateSeq(ctx, db.GetNextAggregateSeqParams{AggregateType: "indexer", AggregateID: id.String()})
	if e != nil {
		return e
	}
	_, e = q.InsertOutboxEvent(ctx, db.InsertOutboxEventParams{ID: f.ids.NewUUID(), AggregateType: "indexer", AggregateID: id.String(), EventType: event, Payload: payload, Seq: int64(seq)})
	return e
}
