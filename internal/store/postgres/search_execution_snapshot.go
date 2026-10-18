package postgres

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Eligibility, completeness and candidate fields share one database snapshot,
// taken after all HTTP calls. A later endpoint cannot keep an earlier stale
// configuration alive merely because that earlier response arrived first.
func (s *SearchExecutionStore) ReadAggregateSnapshot(ctx context.Context, ids []uuid.UUID, failed bool) ([]ports.SearchCandidate, bool, error) {
	if e := ctx.Err(); e != nil {
		return nil, false, e
	}
	tx, e := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return nil, false, e
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.queries.WithTx(tx)
	rows := make([]ports.SearchCandidate, 0)
	seen := map[uuid.UUID]bool{}
	completed, incomplete := 0, false
	for _, id := range ids {
		query, e := q.GetCurrentIndexerQuery(ctx, id)
		if errors.Is(e, pgx.ErrNoRows) {
			failed = true
			continue
		}
		if e != nil {
			return nil, false, e
		}
		complete, e := q.IsIndexerQueryComplete(ctx, id)
		if e != nil {
			return nil, false, e
		}
		candidates, e := q.ListQueryCandidates(ctx, id)
		if e != nil {
			return nil, false, e
		}
		valid := len(candidates) == int(query.ResultsCount)
		for _, c := range candidates {
			valid = valid && c.IndexerID == query.IndexerID && c.SourceConfigVersion == query.ConfigVersion
		}
		if !valid {
			failed = true
			continue
		}
		completed++
		incomplete = incomplete || !complete
		for _, c := range candidates {
			if !seen[c.ID] {
				seen[c.ID] = true
				rows = append(rows, searchCandidateDTO(c))
			}
		}
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, false, e
	}
	if completed == 0 || (failed && len(rows) == 0) {
		return nil, false, ports.ErrUnavailable
	}
	return rows, failed || incomplete, nil
}
func (s *SearchExecutionStore) ReadPage(ctx context.Context, id uuid.UUID) (ports.SearchPageEvidence, error) {
	v, e := s.queries.GetIndexerQueryPage(ctx, id)
	out := ports.SearchPageEvidence{QueryID: v.QueryID, PageOffset: v.PageOffset, PageLimit: v.PageLimit, ItemCount: v.ItemCount, OffsetVerified: v.OffsetVerified}
	if v.Total.Valid {
		total := v.Total.Int64
		out.Total = &total
	}
	return out, e
}
func (s *SearchExecutionStore) ReadQueryCandidates(ctx context.Context, id uuid.UUID) ([]ports.SearchCandidate, error) {
	rows, e := s.queries.ListQueryCandidates(ctx, id)
	out := make([]ports.SearchCandidate, 0, len(rows))
	for _, v := range rows {
		out = append(out, searchCandidateDTO(v))
	}
	return out, e
}
func (s *SearchExecutionStore) ContinuationDelay(ctx context.Context, id uuid.UUID, version int64) (time.Duration, error) {
	v, e := s.queries.GetSearchContinuationDelay(ctx, db.GetSearchContinuationDelayParams{ID: id, ConfigVersion: version})
	return time.Duration(v) * time.Millisecond, e
}
