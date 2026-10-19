package postgres

import (
	"bytes"
	"context"
	"errors"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func validAutomaticTVQuerySpec(s ports.SearchQuerySpec) bool {
	b := s.AutomaticTV
	if b == nil || s.ID == uuid.Nil || s.IndexerID == uuid.Nil || s.ConfigVersion < 1 || s.Page < 0 || s.Page > 3 || s.MinimumInterval < 0 || s.ProbeOnly || s.Actor != nil || s.Interactive != nil || s.HealthJob != nil || s.RSSJobID != uuid.Nil || b.Task.IndexerID != s.IndexerID {
		return false
	}
	v, _, hash, e := canonicalAutomaticTVQuery(b.Query)
	return e == nil && hash == b.ParametersHash && v.Offset == s.Page*50 && s.Keyword == "tv-episode-query-v1:"+hash
}

// Caller must use one transaction and call before business/indexer/query locks.
func AuthorizeAutomaticTVSearchSource(ctx context.Context, q *db.Queries, b ports.AutomaticTVQueryBinding) (err error) {
	defer func() { err = automaticTVError(ctx, err) }()
	r, src, e := automaticTVAuthorize(ctx, q, b.Task)
	if e != nil {
		return e
	}
	expected := src.Parameters
	expected.Offset = b.Query.Offset
	_, raw, hash, e := canonicalAutomaticTVQuery(expected)
	if e != nil {
		return e
	}
	_, actual, actualHash, e := canonicalAutomaticTVQuery(b.Query)
	if e != nil || !bytes.Equal(raw, actual) || hash != actualHash || b.ParametersHash != hash || b.SnapshotHash != r.SnapshotHash {
		return ports.ErrAcquisitionRoundAborted
	}
	return nil
}

// Bind publishes only the original source job's next page, never a foreign query.
func LinkAutomaticTVSearchQuery(ctx context.Context, q *db.Queries, b ports.AutomaticTVQueryBinding, id uuid.UUID) (err error) {
	defer func() { err = automaticTVError(ctx, err) }()
	if id == uuid.Nil {
		return ports.ErrRejected
	}
	if e := AuthorizeAutomaticTVSearchSource(ctx, q, b); e != nil {
		return e
	}
	_, raw, hash, e := canonicalAutomaticTVQuery(b.Query)
	if e != nil {
		return e
	}
	r, e := automaticTVDecodeSource(q.AutomaticTVReadSource(ctx, db.AutomaticTVReadSourceParams{RoundID: b.Task.RoundID, IndexerID: b.Task.IndexerID}))
	if e != nil {
		return e
	}
	pages, e := automaticTVPages(ctx, q, b.Task)
	if e != nil {
		return e
	}
	if len(pages) != int(b.Query.Offset/50) {
		return ports.ErrAcquisitionRoundAborted
	}
	for _, p := range pages {
		query, e := q.GetQueryExecution(ctx, p.QueryID)
		if e != nil {
			return e
		}
		if query.State != "succeeded" || p.SourceJobID != b.Task.Lease.ID || p.SourceToken != b.Task.Lease.Token {
			return ports.ErrAcquisitionRoundAborted
		}
	}
	n, e := q.AutomaticTVLinkQuery(ctx, db.AutomaticTVLinkQueryParams{RoundID: b.Task.RoundID, IndexerID: b.Task.IndexerID, Page: b.Query.Offset / 50, QueryID: id, JobID: b.Task.Lease.ID, Token: b.Task.Lease.Token, Parameters: raw, ParametersHash: hash, ConfigVersion: r.ConfigVersion})
	if e != nil {
		return e
	}
	if n != 1 {
		return ports.ErrAcquisitionRoundAborted
	}
	return nil
}
func (p *searchExecutionPermit) reserveAutomaticTV(ctx context.Context) (out db.Indexer, err error) {
	defer func() { err = automaticTVError(ctx, err) }()
	if !validAutomaticTVQuerySpec(p.spec) {
		return out, ports.ErrRejected
	}
	tx, e := p.store.pool.Begin(ctx)
	if e != nil {
		return out, e
	}
	defer managementRollback(ctx, tx)
	q := db.New(tx)
	if e = AuthorizeAutomaticTVSearchSource(ctx, q, *p.spec.AutomaticTV); e != nil {
		return out, e
	}
	fresh, e := q.LockRSSIndexer(ctx, p.spec.IndexerID)
	if e != nil {
		return out, e
	}
	if fresh.ConfigVersion != p.spec.ConfigVersion {
		return out, ports.ErrAcquisitionRoundAborted
	}
	row, e := q.ReserveIndexerBudget(ctx, db.ReserveIndexerBudgetParams{ID: fresh.ID, IntervalMs: p.spec.MinimumInterval.Milliseconds()})
	if errors.Is(e, pgx.ErrNoRows) {
		return out, ports.ErrAutomaticTVSearchDeferred
	}
	if e != nil {
		return out, e
	}
	resolved, e := p.store.executionIndexer(ctx, q, row)
	if e != nil {
		return out, e
	}
	if resolved.Protocol != ports.IndexerProtocolTorznab {
		return out, ports.ErrUnavailable
	}
	if e = AuthorizeAutomaticTVSearchSource(ctx, q, *p.spec.AutomaticTV); e != nil {
		return out, e
	}
	if e = tx.Commit(ctx); e != nil {
		return out, e
	}
	p.indexer = resolved
	return row, nil
}

// Begin/Commit/Fail are called by the shared dispatch with p.mu held.
func (p *searchExecutionPermit) beginAutomaticTVQuery(ctx context.Context) (out *ports.IndexerQueryResult, err error) {
	defer func() { err = automaticTVError(ctx, err) }()
	if p.closed || p.begun || p.terminal || !validAutomaticTVQuerySpec(p.spec) {
		return nil, ports.ErrRejected
	}
	s := p.spec
	tx, e := p.store.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer managementRollback(ctx, tx)
	q := db.New(tx)
	if e = AuthorizeAutomaticTVSearchSource(ctx, q, *s.AutomaticTV); e != nil {
		return nil, e
	}
	fresh, e := q.LockRSSIndexer(ctx, s.IndexerID)
	if e != nil {
		return nil, e
	}
	if fresh.ConfigVersion != s.ConfigVersion {
		return nil, ports.ErrAcquisitionRoundAborted
	}
	if _, e = p.store.executionIndexer(ctx, q, fresh); e != nil {
		return nil, e
	}
	// The caller still owns the physical endpoint permit: interrupted calls are gone.
	if e = q.RetireInterruptedIndexerQueries(ctx, s.IndexerID); e != nil {
		return nil, e
	}
	row, e := q.CreateRunningQuery(ctx, db.CreateRunningQueryParams{ID: s.ID, IndexerID: s.IndexerID, QueryType: "tv_episode", Keyword: s.Keyword, Page: s.Page, ConfigVersion: s.ConfigVersion})
	if e != nil {
		return nil, e
	}
	if e = LinkAutomaticTVSearchQuery(ctx, q, *s.AutomaticTV, row.ID); e != nil {
		return nil, e
	}
	if _, e = tx.Exec(ctx, `UPDATE automatic_tv_source SET state='running' WHERE round_id=$1 AND indexer_id=$2 AND state='queued'`, s.AutomaticTV.Task.RoundID, s.IndexerID); e != nil {
		return nil, e
	}
	if e = AuthorizeAutomaticTVSearchSource(ctx, q, *s.AutomaticTV); e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	p.begun = true
	return &ports.IndexerQueryResult{QueryID: row.ID, State: row.State}, nil
}
func automaticTVBoundQuery(ctx context.Context, q *db.Queries, s ports.SearchQuerySpec) error {
	if !validAutomaticTVQuerySpec(s) {
		return ports.ErrRejected
	}
	if e := AuthorizeAutomaticTVSearchSource(ctx, q, *s.AutomaticTV); e != nil {
		return e
	}
	fresh, e := q.LockRSSIndexer(ctx, s.IndexerID)
	if e != nil {
		return e
	}
	if fresh.ConfigVersion != s.ConfigVersion {
		return ports.ErrAcquisitionRoundAborted
	}
	query, e := q.LockIndexerQueryExecution(ctx, s.ID)
	if e != nil {
		return e
	}
	if query.State != "running" || query.IndexerID != s.IndexerID || query.ConfigVersion != s.ConfigVersion || query.QueryType != "tv_episode" || query.Page != s.Page || query.Keyword != s.Keyword || query.RssJobID.Valid {
		return ports.ErrAcquisitionRoundAborted
	}
	pages, e := automaticTVPages(ctx, q, s.AutomaticTV.Task)
	if e != nil {
		return e
	}
	if len(pages) <= int(s.Page) {
		return ports.ErrAcquisitionRoundAborted
	}
	a := pages[s.Page]
	_, raw, hash, e := canonicalAutomaticTVQuery(a.Parameters)
	if e != nil {
		return e
	}
	_, expected, expectedHash, e := canonicalAutomaticTVQuery(s.AutomaticTV.Query)
	if e != nil || a.Page != s.Page || a.QueryID != s.ID || a.SourceJobID != s.AutomaticTV.Task.Lease.ID || a.SourceToken != s.AutomaticTV.Task.Lease.Token || a.ParametersHash != hash || hash != expectedHash || !bytes.Equal(raw, expected) {
		return ports.ErrAcquisitionRoundAborted
	}
	return nil
}
func (p *searchExecutionPermit) commitAutomaticTVQuery(ctx context.Context, c ports.SearchQueryCommit) (out *ports.IndexerQueryResult, err error) {
	defer func() { err = automaticTVError(ctx, err) }()
	if p.closed || !p.begun || p.terminal || !validAutomaticTVQuerySpec(p.spec) {
		return nil, ports.ErrRejected
	}
	s := p.spec
	proof := c.Page
	if proof == nil || proof.QueryID != s.ID || proof.PageOffset != int64(s.Page)*50 || proof.PageLimit != 50 || proof.ItemCount != int32(len(c.Candidates)) || proof.ItemCount > 50 {
		return nil, ports.ErrRejected
	}
	tx, e := p.store.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer managementRollback(ctx, tx)
	q := db.New(tx)
	if e = automaticTVBoundQuery(ctx, q, s); e != nil {
		return nil, e
	}
	for _, v := range c.Candidates {
		if v.ID == uuid.Nil || v.IndexerID != s.IndexerID || v.SourceConfigVersion != s.ConfigVersion || len(v.LinkEncrypted) == 0 {
			return nil, ports.ErrRejected
		}
		row, e := q.UpsertReleaseCandidate(ctx, db.UpsertReleaseCandidateParams{ID: v.ID, IndexerID: v.IndexerID, Title: v.Title, Guid: v.Guid, LinkEncrypted: v.LinkEncrypted, InfoHash: searchPGText(v.InfoHash), SizeBytes: v.SizeBytes, Seeders: v.Seeders, Leechers: v.Leechers, PublishDate: searchPGTime(v.PublishDate), QualityProfile: v.QualityProfile, SourceConfigVersion: v.SourceConfigVersion})
		if e != nil {
			return nil, e
		}
		if e = q.LinkQueryCandidate(ctx, db.LinkQueryCandidateParams{QueryID: s.ID, CandidateID: row.ID}); e != nil {
			return nil, e
		}
		stamp := quality.AcquisitionSourceStamp(row.Title, row.InfoHash.String, row.LinkEncrypted, row.QualityProfile)
		if e = q.AutomaticTVFreezeCandidate(ctx, db.AutomaticTVFreezeCandidateParams{QueryID: s.ID, CandidateID: row.ID, SourceStamp: stamp, Title: row.Title, Guid: row.Guid, InfoHash: row.InfoHash, LinkEncrypted: row.LinkEncrypted, QualityProfile: row.QualityProfile, SizeBytes: row.SizeBytes, SourceConfigVersion: row.SourceConfigVersion}); e != nil {
			return nil, e
		}
	}
	page := db.InsertIndexerQueryPageParams{QueryID: s.ID, PageOffset: proof.PageOffset, PageLimit: 50, ItemCount: proof.ItemCount, OffsetVerified: proof.OffsetVerified}
	if proof.Total != nil {
		page.Total = pgtype.Int8{Int64: *proof.Total, Valid: true}
	}
	if e = q.InsertIndexerQueryPage(ctx, page); e != nil {
		return nil, e
	}
	if _, e = q.FinishQuerySuccess(ctx, db.FinishQuerySuccessParams{ID: s.ID, ResultsCount: int32(len(c.Candidates))}); e != nil {
		return nil, e
	}
	if e = AuthorizeAutomaticTVSearchSource(ctx, q, *s.AutomaticTV); e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	p.terminal = true
	return &ports.IndexerQueryResult{QueryID: s.ID, State: "succeeded", ResultsCount: len(c.Candidates)}, nil
}
func (p *searchExecutionPermit) failAutomaticTVQuery(ctx context.Context, f ports.SearchQueryFailure, retry time.Duration) (err error) {
	defer func() { err = automaticTVError(ctx, err) }()
	if p.closed || !p.begun || p.terminal || !validAutomaticTVQuerySpec(p.spec) {
		return ports.ErrRejected
	}
	switch f {
	case ports.SearchFailureUnavailable, ports.SearchFailureAuth, ports.SearchFailureTimeout, ports.SearchFailureBadResponse, ports.SearchFailureUnsupported, ports.SearchFailureRateLimited, ports.SearchFailureInvalidResult:
	default:
		return ports.ErrRejected
	}
	tx, e := p.store.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer managementRollback(ctx, tx)
	q := db.New(tx)
	if e = automaticTVBoundQuery(ctx, q, p.spec); e != nil {
		return e
	}
	if f == ports.SearchFailureRateLimited {
		if retry <= 0 {
			retry = time.Minute
		}
		if retry > 24*time.Hour {
			retry = 24 * time.Hour
		}
		if e = q.DeferIndexerBudget(ctx, db.DeferIndexerBudgetParams{ID: p.spec.IndexerID, IntervalMs: retry.Milliseconds()}); e != nil {
			return e
		}
	}
	if _, e = q.FinishQueryFailed(ctx, db.FinishQueryFailedParams{ID: p.spec.ID, Error: pgtype.Text{String: string(f), Valid: true}}); e != nil {
		return e
	}
	if e = AuthorizeAutomaticTVSearchSource(ctx, q, *p.spec.AutomaticTV); e != nil {
		return e
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	p.terminal = true
	return nil
}
