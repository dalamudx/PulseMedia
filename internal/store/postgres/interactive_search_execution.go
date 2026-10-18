package postgres

import (
	"context"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

func validInteractiveQuerySpec(spec ports.SearchQuerySpec) bool {
	l := spec.Interactive
	return l != nil && l.Task.SearchID != uuid.Nil && l.Task.IndexerID == spec.IndexerID && l.Task.ConfigVersion == spec.ConfigVersion && l.ActorID != uuid.Nil && l.Query == spec.Keyword && spec.Actor == nil && !spec.ProbeOnly && spec.RSSJobID == uuid.Nil && spec.HealthJob == nil && l.Task.Lease.ID != uuid.Nil && l.Task.Lease.Owner != "" && l.Task.Lease.Token > 0
}

// The original dedicated endpoint connection is held throughout. Query creation
// and durable search ownership are published together, before external I/O.
func (p *searchExecutionPermit) beginInteractiveQuery(ctx context.Context) (*ports.IndexerQueryResult, error) {
	spec := p.spec
	if !validInteractiveQuerySpec(spec) {
		return nil, ports.ErrBadResponse
	}
	tx, err := p.store.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := p.store.queries.WithTx(tx)
	if err = AuthorizeInteractiveSearchSource(ctx, q, *spec.Interactive); err != nil {
		return nil, err
	}
	fresh, err := q.LockRSSIndexer(ctx, spec.IndexerID)
	if err != nil || fresh.ConfigVersion != spec.ConfigVersion {
		return nil, ports.ErrUnavailable
	}
	if _, err = p.store.executionIndexer(ctx, q, fresh); err != nil {
		return nil, err
	}
	if err = q.RetireInterruptedIndexerQueries(ctx, spec.IndexerID); err != nil {
		return nil, err
	}
	row, err := q.CreateRunningQuery(ctx, db.CreateRunningQueryParams{ID: spec.ID, IndexerID: spec.IndexerID, QueryType: "interactive", RssJobID: pgtype.UUID{}, Keyword: spec.Keyword, Page: spec.Page, ConfigVersion: spec.ConfigVersion})
	if err != nil {
		return nil, err
	}
	if err = LinkInteractiveSearchQuery(ctx, q, *spec.Interactive, row.ID, spec.Page); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	p.begun = true
	return &ports.IndexerQueryResult{QueryID: row.ID, State: row.State}, nil
}

func (s *SearchExecutionStore) InteractiveSearchContinuationDelay(ctx context.Context, id uuid.UUID, version int64) (time.Duration, error) {
	delay, err := s.queries.GetInteractiveSearchContinuationDelay(ctx, db.GetInteractiveSearchContinuationDelayParams{ID: id, ConfigVersion: version})
	if err != nil {
		return 0, ports.ErrUnavailable
	}
	return time.Duration(delay) * time.Millisecond, nil
}
