package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"sync"
	"time"
)

type SearchExecutionStore struct {
	pool    *pgxpool.Pool
	queries *db.Queries
}

func NewSearchExecutionStore(pool *pgxpool.Pool) *SearchExecutionStore {
	return &SearchExecutionStore{pool, Queries(pool)}
}

var _ ports.SearchRepository = (*SearchExecutionStore)(nil)

func (s *SearchExecutionStore) GetIndexer(ctx context.Context, id uuid.UUID) (ports.SearchIndexer, error) {
	v, e := s.queries.GetIndexerByID(ctx, id)
	return searchIndexerDTO(v), e
}
func (s *SearchExecutionStore) ListIndexers(ctx context.Context) ([]ports.SearchIndexer, error) {
	rows, e := s.queries.ListIndexers(ctx)
	out := make([]ports.SearchIndexer, 0, len(rows))
	for _, v := range rows {
		out = append(out, searchIndexerDTO(v))
	}
	return out, e
}
func (s *SearchExecutionStore) CreateIndexer(ctx context.Context, v ports.SearchIndexer) (ports.SearchIndexer, error) {
	row, e := s.queries.CreateIndexer(ctx, db.CreateIndexerParams{ID: v.ID, Name: v.Name, Url: v.Url, ApiKeyEncrypted: v.ApiKeyEncrypted, Categories: v.Categories})
	return searchIndexerDTO(row), e
}
func (s *SearchExecutionStore) DeleteIndexer(ctx context.Context, id uuid.UUID) error {
	return s.queries.DeleteIndexer(ctx, id)
}

type searchExecutionPermit struct {
	store                   *SearchExecutionStore
	spec                    ports.SearchQuerySpec
	release                 func()
	mu                      sync.Mutex
	closed, begun, terminal bool
}

func searchQueryType(spec ports.SearchQuerySpec) string {
	if spec.RSSJobID != uuid.Nil {
		return "rss"
	}
	return "search"
}
func searchShared(v db.IndexerQuery) *ports.IndexerQueryResult {
	return &ports.IndexerQueryResult{QueryID: v.ID, State: v.State, ResultsCount: int(v.ResultsCount), FromShared: true}
}
func (s *SearchExecutionStore) AdmitQuery(ctx context.Context, spec ports.SearchQuerySpec) (ports.SearchQueryAdmission, ports.SearchExecutionPermit, error) {
	var out ports.SearchQueryAdmission
	if spec.ID == uuid.Nil || spec.IndexerID == uuid.Nil || spec.ConfigVersion < 1 || spec.Page < 0 || spec.Page > 2147483647/50 || spec.MinimumInterval < 0 {
		return out, nil, ports.ErrBadResponse
	}
	idx, e := s.queries.GetIndexerByID(ctx, spec.IndexerID)
	if e != nil {
		return out, nil, e
	}
	if idx.ConfigVersion != spec.ConfigVersion {
		return out, nil, ports.ErrUnavailable
	}
	out.Indexer = searchIndexerDTO(idx)
	if spec.RSSJobID != uuid.Nil {
		if !idx.RssEnabled {
			return out, nil, ports.ErrRSSConfigConflict
		}
		cached, e := s.queries.GetRSSJobQuery(ctx, db.GetRSSJobQueryParams{RssJobID: pgtype.UUID{Bytes: spec.RSSJobID, Valid: true}, ConfigVersion: spec.ConfigVersion})
		if e == nil {
			out.Shared = searchShared(cached)
			return out, nil, nil
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return out, nil, e
		}
	}
	active, e := s.queries.GetActiveQuery(ctx, db.GetActiveQueryParams{QueryType: searchQueryType(spec), IndexerID: spec.IndexerID, Keyword: spec.Keyword, Page: spec.Page, ConfigVersion: spec.ConfigVersion})
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return out, nil, e
	}
	release, e := AcquireIndexerPermit(ctx, s.pool, spec.IndexerID)
	if e != nil {
		if errors.Is(e, ports.ErrUnavailable) && active.ID != uuid.Nil {
			out.Shared = searchShared(active)
			return out, nil, nil
		}
		return out, nil, e
	}
	if spec.HealthJob != nil {
		copyJob := *spec.HealthJob
		spec.HealthJob = &copyJob
	}
	p := &searchExecutionPermit{store: s, spec: spec, release: release}
	idx, e = p.reserve(ctx, false)
	if e != nil {
		p.Close()
		return out, nil, e
	}
	out.Indexer = searchIndexerDTO(idx)
	return out, p, nil
}
func (p *searchExecutionPermit) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.closed {
		p.closed = true
		p.release()
	}
}
func (p *searchExecutionPermit) reserve(ctx context.Context, second bool) (db.Indexer, error) {
	spec := p.spec
	tx, e := p.store.pool.Begin(ctx)
	if e != nil {
		return db.Indexer{}, e
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := p.store.queries.WithTx(tx)
	fresh, e := q.LockRSSIndexer(ctx, spec.IndexerID)
	if e != nil || fresh.ConfigVersion != spec.ConfigVersion || (!spec.ProbeOnly && (fresh.Health != "online" || (spec.RSSJobID != uuid.Nil && !fresh.RssEnabled))) {
		return db.Indexer{}, ports.ErrUnavailable
	}
	var guard db.GetAuthorizedSearchJobParams
	if j := spec.HealthJob; j != nil {
		if j.IndexerID != spec.IndexerID || j.ConfigVersion != spec.ConfigVersion {
			return db.Indexer{}, ports.ErrBadResponse
		}
		if _, e = q.LockJobForWrite(ctx, j.ID); e != nil {
			return db.Indexer{}, ports.ErrUnavailable
		}
		guard = db.GetAuthorizedSearchJobParams{ID: j.ID, Kind: "indexer_health", LeaseOwner: pgtype.Text{String: j.Owner, Valid: true}, LeaseToken: j.Token}
		row, e := q.GetAuthorizedSearchJob(ctx, guard)
		if e != nil {
			return db.Indexer{}, ports.ErrUnavailable
		}
		var payload ports.IndexerHealthJob
		if json.Unmarshal(row.Payload, &payload) != nil || payload.IndexerID != spec.IndexerID || payload.ConfigVersion != spec.ConfigVersion {
			return db.Indexer{}, ports.ErrBadResponse
		}
	}
	row, e := q.ReserveIndexerBudget(ctx, db.ReserveIndexerBudgetParams{ID: spec.IndexerID, IntervalMs: spec.MinimumInterval.Milliseconds()})
	if e != nil {
		return db.Indexer{}, ports.ErrUnavailable
	}
	if spec.HealthJob != nil {
		if _, e = q.GetAuthorizedSearchJob(ctx, guard); e != nil {
			return db.Indexer{}, ports.ErrUnavailable
		}
	}
	if e = tx.Commit(ctx); e != nil {
		return db.Indexer{}, e
	}
	return row, nil
}
func (p *searchExecutionPermit) BeginQuery(ctx context.Context) (*ports.IndexerQueryResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.begun || p.terminal {
		return nil, ports.ErrUnavailable
	}
	s := p.spec
	q := p.store.queries
	if e := q.RetireInterruptedIndexerQueries(ctx, s.IndexerID); e != nil {
		return nil, e
	}
	row, e := q.CreateRunningQuery(ctx, db.CreateRunningQueryParams{ID: s.ID, IndexerID: s.IndexerID, QueryType: searchQueryType(s), RssJobID: pgtype.UUID{Bytes: s.RSSJobID, Valid: s.RSSJobID != uuid.Nil}, Keyword: s.Keyword, Page: s.Page, ConfigVersion: s.ConfigVersion})
	if e != nil {
		var pgErr *pgconn.PgError
		if errors.As(e, &pgErr) && pgErr.Code == "23505" {
			v, e2 := q.GetActiveQuery(ctx, db.GetActiveQueryParams{QueryType: searchQueryType(s), IndexerID: s.IndexerID, Keyword: s.Keyword, Page: s.Page, ConfigVersion: s.ConfigVersion})
			if e2 == nil {
				p.terminal = true
				return searchShared(v), nil
			}
		}
		return nil, e
	}
	p.begun = true
	return &ports.IndexerQueryResult{QueryID: row.ID, State: row.State}, nil
}
func (p *searchExecutionPermit) ReserveHealthSearch(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || !p.begun || p.terminal || !p.spec.ProbeOnly {
		return ports.ErrUnavailable
	}
	_, e := p.reserve(ctx, true)
	return e
}
func (p *searchExecutionPermit) CommitQuery(ctx context.Context, commit ports.SearchQueryCommit) (*ports.IndexerQueryResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || !p.begun || p.terminal {
		return nil, ports.ErrUnavailable
	}
	s := p.spec
	tx, e := p.store.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := p.store.queries.WithTx(tx)
	fresh, e := q.LockRSSIndexer(ctx, s.IndexerID)
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	current, e := q.LockIndexerQueryExecution(ctx, s.ID)
	if e != nil || current.State != "running" {
		return nil, ports.ErrUnavailable
	}
	if fresh.ConfigVersion != s.ConfigVersion || (!s.ProbeOnly && (fresh.Health != "online" || (s.RSSJobID != uuid.Nil && !fresh.RssEnabled))) || current.ConfigVersion != s.ConfigVersion || current.IndexerID != s.IndexerID || current.Page != s.Page || current.Keyword != s.Keyword || current.QueryType != searchQueryType(s) || current.RssJobID.Valid != (s.RSSJobID != uuid.Nil) || (current.RssJobID.Valid && uuid.UUID(current.RssJobID.Bytes) != s.RSSJobID) {
		if _, e = q.FinishQueryFailed(ctx, db.FinishQueryFailedParams{ID: s.ID, Error: pgtype.Text{String: "indexer configuration changed", Valid: true}}); e != nil {
			return nil, e
		}
		if e = tx.Commit(ctx); e != nil {
			return nil, e
		}
		p.terminal = true
		return &ports.IndexerQueryResult{QueryID: s.ID, State: "failed"}, ports.ErrUnavailable
	}
	if s.ProbeOnly {
		if len(commit.Candidates) != 0 || commit.Page != nil {
			return nil, ports.ErrBadResponse
		}
	} else {
		proof := commit.Page
		if proof == nil || proof.QueryID != s.ID || proof.PageOffset != int64(s.Page)*50 || proof.PageLimit != 50 || proof.ItemCount != int32(len(commit.Candidates)) || proof.ItemCount > 50 {
			return nil, ports.ErrBadResponse
		}
	}
	for _, v := range commit.Candidates {
		if v.ID == uuid.Nil || v.IndexerID != s.IndexerID || v.SourceConfigVersion != s.ConfigVersion || len(v.LinkEncrypted) == 0 {
			return nil, ports.ErrBadResponse
		}
		row, e := q.UpsertReleaseCandidate(ctx, db.UpsertReleaseCandidateParams{ID: v.ID, IndexerID: v.IndexerID, Title: v.Title, Guid: v.Guid, LinkEncrypted: v.LinkEncrypted, InfoHash: searchPGText(v.InfoHash), SizeBytes: v.SizeBytes, Seeders: v.Seeders, Leechers: v.Leechers, PublishDate: searchPGTime(v.PublishDate), QualityProfile: v.QualityProfile, SourceConfigVersion: v.SourceConfigVersion})
		if e != nil {
			return nil, e
		}
		if e = q.LinkQueryCandidate(ctx, db.LinkQueryCandidateParams{QueryID: s.ID, CandidateID: row.ID}); e != nil {
			return nil, e
		}
	}
	if proof := commit.Page; proof != nil {
		v := db.InsertIndexerQueryPageParams{QueryID: s.ID, PageOffset: proof.PageOffset, PageLimit: proof.PageLimit, ItemCount: proof.ItemCount, OffsetVerified: proof.OffsetVerified}
		if proof.Total != nil {
			v.Total = pgtype.Int8{Int64: *proof.Total, Valid: true}
		}
		if e = q.InsertIndexerQueryPage(ctx, v); e != nil {
			return nil, e
		}
	}
	if _, e = q.FinishQuerySuccess(ctx, db.FinishQuerySuccessParams{ID: s.ID, ResultsCount: int32(len(commit.Candidates))}); e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	p.terminal = true
	return &ports.IndexerQueryResult{QueryID: s.ID, State: "succeeded", ResultsCount: len(commit.Candidates)}, nil
}
func (p *searchExecutionPermit) FailQuery(ctx context.Context, failure ports.SearchQueryFailure, retry time.Duration) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || !p.begun || p.terminal {
		return ports.ErrUnavailable
	}
	message := "indexer search failed"
	switch failure {
	case ports.SearchFailureAuth:
		message = "indexer authentication required"
	case ports.SearchFailureTimeout:
		message = "indexer timed out"
	case ports.SearchFailureBadResponse:
		message = "indexer bad response"
	case ports.SearchFailureUnsupported:
		message = "indexer capabilities unsupported"
	case ports.SearchFailureInvalidResult:
		message = "invalid indexer result"
	case ports.SearchFailureRateLimited:
		message = "indexer rate limited"
		if retry <= 0 {
			retry = time.Minute
		}
		if retry > 24*time.Hour {
			retry = 24 * time.Hour
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = p.store.queries.DeferIndexerBudget(cleanup, db.DeferIndexerBudgetParams{ID: p.spec.IndexerID, IntervalMs: retry.Milliseconds()})
		cancel()
	}
	// Only a permit-bound running query can be failed; messages are selected above.
	tx, e := p.store.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := p.store.queries.WithTx(tx)
	v, e := q.LockIndexerQueryExecution(ctx, p.spec.ID)
	if e != nil || v.State != "running" || v.IndexerID != p.spec.IndexerID || v.ConfigVersion != p.spec.ConfigVersion {
		return ports.ErrUnavailable
	}
	if _, e = q.FinishQueryFailed(ctx, db.FinishQueryFailedParams{ID: p.spec.ID, Error: pgtype.Text{String: message, Valid: true}}); e != nil {
		return e
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	p.terminal = true
	return nil
}
