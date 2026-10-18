package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type SearchEndpointStore struct{ pool *pgxpool.Pool }

func NewSearchEndpointStore(pool *pgxpool.Pool) *SearchEndpointStore {
	return &SearchEndpointStore{pool: pool}
}

var _ ports.SearchEndpointRepository = (*SearchEndpointStore)(nil)

func readEndpointScope(ctx context.Context, q *db.Queries, t ports.SearchEndpointTask) (ports.SearchEndpointScope, db.GetLeasedSearchEndpointScopeRow, error) {
	var empty ports.SearchEndpointScope
	var row db.GetLeasedSearchEndpointScopeRow
	if t.RoundID == uuid.Nil || t.IndexerID == uuid.Nil || t.Lease.ID == uuid.Nil || t.Lease.Owner == "" || t.Lease.Token <= 0 {
		return empty, row, ports.ErrRejected
	}
	row, e := q.GetLeasedSearchEndpointScope(ctx, db.GetLeasedSearchEndpointScopeParams{RoundID: t.RoundID, IndexerID: t.IndexerID, JobID: t.Lease.ID, Owner: pgtype.Text{String: t.Lease.Owner, Valid: true}, Token: t.Lease.Token})
	if e != nil {
		if errors.Is(e, pgx.ErrNoRows) {
			e = ports.ErrRejected
		}
		return empty, row, e
	}
	var snap ports.RoundSnapshot
	if json.Unmarshal(row.DecisionSnapshot, &snap) != nil {
		return empty, row, ports.ErrRejected
	}
	version, ok := snap.Indexers[t.IndexerID.String()]
	if !ok || version <= 0 {
		return empty, row, ports.ErrRejected
	}
	return ports.SearchEndpointScope{Task: t, ConfigVersion: version, Title: snap.Title, Year: snap.Year, Deadline: row.Deadline.Time, SnapshotHash: sha256.Sum256(row.DecisionSnapshot), MaySearch: row.BeforeDeadline && row.Health == "online" && row.ConfigVersion == version}, row, nil
}
func (s *SearchEndpointStore) Load(ctx context.Context, t ports.SearchEndpointTask) (ports.SearchEndpointScope, error) {
	q := db.New(s.pool)
	scope, _, e := readEndpointScope(ctx, q, t)
	if e == nil {
		e = loadEndpointPages(ctx, q, &scope)
	}
	return scope, e
}
func (s *SearchEndpointStore) Complete(ctx context.Context, scope ports.SearchEndpointScope, queryID uuid.UUID) error {
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)
	t := scope.Task
	if _, e = q.LockSearchRound(ctx, t.RoundID); e != nil {
		return e
	}
	if _, e = q.LockJobForWrite(ctx, t.Lease.ID); e != nil {
		return e
	}
	if _, e = q.LockRSSIndexer(ctx, t.IndexerID); e != nil {
		return e
	}
	fresh, row, e := readEndpointScope(ctx, q, t)
	if e != nil {
		return e
	}
	if e = loadEndpointPages(ctx, q, &fresh); e != nil {
		return e
	}
	if fresh.NextPage != scope.NextPage {
		return ports.ErrRejected
	}
	if fresh.SnapshotHash != scope.SnapshotHash || !fresh.Deadline.Equal(scope.Deadline) || fresh.ConfigVersion != scope.ConfigVersion || fresh.Title != scope.Title || fresh.Year != scope.Year {
		return ports.ErrRejected
	}
	state := "failed"
	linked := pgtype.UUID{}
	if row.State == "running" && row.Health == "online" && row.ConfigVersion == scope.ConfigVersion && len(fresh.SavedQueries) > 0 {
		state = "returned"
		linked = pgtype.UUID{Bytes: fresh.SavedQueries[0], Valid: true}
	}
	if queryID != uuid.Nil {
		candidateQuery, e := q.GetQueryExecution(ctx, queryID)
		if e != nil {
			return e
		}
		keyword := scope.Title
		if scope.Year > 0 {
			keyword = fmt.Sprintf("%s %d", keyword, scope.Year)
		}
		if candidateQuery.IndexerID != t.IndexerID || candidateQuery.ConfigVersion != scope.ConfigVersion || candidateQuery.Keyword != keyword || candidateQuery.QueryType != "search" || candidateQuery.Page != scope.NextPage {
			return ports.ErrRejected
		}
		beforeDeadline := candidateQuery.State == "running" && row.BeforeDeadline || candidateQuery.State == "succeeded" && candidateQuery.FinishedAt.Valid && !candidateQuery.FinishedAt.Time.After(scope.Deadline)
		if row.State == "running" && row.Health == "online" && row.ConfigVersion == scope.ConfigVersion && beforeDeadline {
			state = "returned"
			linked = pgtype.UUID{Bytes: queryID, Valid: true}
			if len(fresh.SavedQueries) > 0 {
				linked = pgtype.UUID{Bytes: fresh.SavedQueries[0], Valid: true}
			}
			if candidateQuery.State == "succeeded" {
				_, proofErr := q.GetIndexerQueryPage(ctx, queryID)
				if proofErr != nil && !errors.Is(proofErr, pgx.ErrNoRows) {
					return proofErr
				}
				if proofErr == nil {
					more, err := recordEndpointPage(ctx, q, fresh, queryID)
					if err != nil {
						return err
					}
					idx, err := q.GetIndexerByID(ctx, t.IndexerID)
					if err != nil {
						return err
					}
					if more && fresh.MaySearch && idx.NextQueryAt.Time.Before(scope.Deadline) {
						_, err = q.YieldSearchEndpointPageJob(ctx, db.YieldSearchEndpointPageJobParams{JobID: t.Lease.ID, Owner: pgtype.Text{String: t.Lease.Owner, Valid: true}, Token: t.Lease.Token, NotBefore: idx.NextQueryAt})
						if err != nil {
							return err
						}
						valid, err := q.IsSearchEndpointLeaseTimeValid(ctx, row.LeaseUntil)
						if err != nil {
							return err
						}
						if !valid {
							return ports.ErrRejected
						}
						return tx.Commit(ctx)
					}
				}
			}
		}
	}
	n, e := q.ReturnRoundEndpoint(ctx, db.ReturnRoundEndpointParams{RoundID: t.RoundID, IndexerID: t.IndexerID, QueryID: linked, State: state})
	if e != nil {
		return e
	}
	if n != 1 {
		return ports.ErrRejected
	}
	if _, e = q.FinishJob(ctx, db.FinishJobParams{Column1: t.Lease.ID, Column2: t.Lease.Owner, Column3: t.Lease.Token, Column4: []byte(`{}`)}); e != nil {
		return e
	}
	valid, err := q.IsSearchEndpointLeaseTimeValid(ctx, row.LeaseUntil)
	if err != nil {
		return err
	}
	if !valid {
		return ports.ErrRejected
	}
	return tx.Commit(ctx)
}
