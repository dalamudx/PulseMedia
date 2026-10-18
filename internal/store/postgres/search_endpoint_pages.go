package postgres

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
)

func loadEndpointPages(ctx context.Context, q *db.Queries, scope *ports.SearchEndpointScope) error {
	rows, e := q.ListSearchEndpointPages(ctx, db.ListSearchEndpointPagesParams{RoundID: scope.Task.RoundID, IndexerID: scope.Task.IndexerID})
	if e != nil {
		return e
	}
	seen := make(map[string]bool)
	for n, row := range rows {
		if row.Page != int32(n) || row.PageOffset != int64(n)*50 {
			return ports.ErrRejected
		}
		scope.SavedQueries = append(scope.SavedQueries, row.QueryID)
		candidates, e := q.ListQueryCandidates(ctx, row.QueryID)
		if e != nil {
			return e
		}
		if len(candidates) != int(row.ItemCount) {
			return ports.ErrRejected
		}
		for _, c := range candidates {
			if seen[c.Guid] {
				return ports.ErrRejected
			}
			seen[c.Guid] = true
			scope.SeenGUIDs = append(scope.SeenGUIDs, c.Guid)
		}
		if row.Total.Valid {
			total := row.Total.Int64
			if scope.PageTotal != nil && *scope.PageTotal != total {
				return ports.ErrRejected
			}
			scope.PageTotal = &total
		}
	}
	scope.NextPage = int32(len(rows))
	if len(rows) > 0 {
		last := rows[len(rows)-1]
		scope.MaySearch = scope.MaySearch && scope.NextPage < 4 && last.OffsetVerified && last.Total.Valid && last.ItemCount == 50 && last.PageLimit == 50 && last.PageOffset+int64(last.ItemCount) < last.Total.Int64
	}
	return nil
}

func recordEndpointPage(ctx context.Context, q *db.Queries, scope ports.SearchEndpointScope, queryID uuid.UUID) (bool, error) {
	proof, e := q.GetIndexerQueryPage(ctx, queryID)
	if e != nil {
		return false, e
	}
	if proof.PageOffset != int64(scope.NextPage)*50 || proof.PageLimit != 50 {
		return false, ports.ErrRejected
	}
	if scope.NextPage > 0 {
		if !proof.OffsetVerified || !proof.Total.Valid || scope.PageTotal == nil || proof.Total.Int64 != *scope.PageTotal {
			return false, ports.ErrRejected
		}
		seen := make(map[string]bool, len(scope.SeenGUIDs))
		for _, guid := range scope.SeenGUIDs {
			seen[guid] = true
		}
		rows, e := q.ListQueryCandidates(ctx, queryID)
		if e != nil {
			return false, e
		}
		for _, row := range rows {
			if seen[row.Guid] {
				return false, ports.ErrRejected
			}
		}
	}
	if e = q.AddSearchEndpointPage(ctx, db.AddSearchEndpointPageParams{RoundID: scope.Task.RoundID, IndexerID: scope.Task.IndexerID, Page: scope.NextPage, QueryID: queryID, SourceJobID: scope.Task.Lease.ID, SourceToken: scope.Task.Lease.Token}); e != nil {
		return false, e
	}
	return scope.NextPage < 3 && proof.OffsetVerified && proof.Total.Valid && proof.ItemCount == 50 && proof.PageOffset+int64(proof.ItemCount) < proof.Total.Int64, nil
}
