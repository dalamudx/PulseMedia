package postgres

import (
	"context"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ReadInteractiveSearch reads account, object scope, source lifecycle and all page
// evidence in one RR snapshot with account locks and no writes. Seeders are latest
// persisted statistics.
func (s *InteractiveSearchStore) ReadInteractiveSearch(ctx context.Context, a *ports.Actor, id uuid.UUID) (out *ports.InteractiveSearchSnapshot, err error) {
	defer func() { err = interactiveError(ctx, err) }()
	tx, e := s.begin(ctx, true)
	if e != nil {
		return nil, e
	}
	defer managementRollback(ctx, tx)
	if e = interactiveActor(ctx, tx, a, false); e != nil {
		return nil, e
	}
	if e = AuthorizeMovieDiscoverySearch(ctx, db.New(tx), id); e != nil {
		return nil, e
	}
	if e = AuthorizeTVEpisodeDiscoverySearch(ctx, db.New(tx), id); e != nil {
		return nil, e
	}
	h, e := interactiveOwned(ctx, tx, a, id, false)
	if e != nil {
		return nil, e
	}
	out = &ports.InteractiveSearchSnapshot{Header: *h.public(), State: h.State, Sources: []ports.InteractiveSearchSource{}, Candidates: []ports.InteractiveSearchCandidate{}, StatisticsMode: "latest_persisted"}
	rows, e := tx.Query(ctx, `SELECT s.search_id,s.indexer_id,s.config_version,s.job_id,s.state,s.reason,i.name,i.config_version,COALESCE(l.enabled,true),l.deleted_at FROM interactive_search_source s JOIN indexer i ON i.id=s.indexer_id LEFT JOIN indexer_lifecycle l ON l.indexer_id=i.id WHERE s.search_id=$1 ORDER BY s.indexer_id`, id)
	if e != nil {
		return nil, e
	}
	type source struct {
		row     interactiveSourceRow
		name    string
		version int64
		enabled bool
		deleted *time.Time
	}
	sources := []source{}
	for rows.Next() {
		var v source
		if e = rows.Scan(&v.row.SearchID, &v.row.IndexerID, &v.row.ConfigVersion, &v.row.JobID, &v.row.State, &v.row.Reason, &v.name, &v.version, &v.enabled, &v.deleted); e != nil {
			rows.Close()
			return nil, e
		}
		sources = append(sources, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	if len(sources) != h.SourceCount {
		return nil, ports.ErrInteractiveSearchConflict
	}
	q := db.New(tx)
	seen := map[uuid.UUID]bool{}
	for _, src := range sources {
		view := ports.InteractiveSearchSource{IndexerID: src.row.IndexerID, Name: src.name, ConfigVersion: src.row.ConfigVersion, State: src.row.State, Reason: src.row.Reason, QueryIDs: []uuid.UUID{}}
		if src.version != src.row.ConfigVersion || !src.enabled || src.deleted != nil {
			view.State = "failed"
			view.Reason = "source_changed"
			view.Partial = true
			out.Partial = true
			out.Sources = append(out.Sources, view)
			continue
		}
		pages, e := interactivePages(ctx, tx, id, src.row.IndexerID)
		if e != nil {
			return nil, e
		}
		firstComplete := false
		completed := 0
		for position, p := range pages {
			if int(p.page) != position || p.job != src.row.JobID || p.token <= 0 {
				return nil, ports.ErrInteractiveSearchConflict
			}
			query, e := q.LockFreeInteractiveQuery(ctx, p.query)
			if e != nil {
				return nil, e
			}
			if query.IndexerID != src.row.IndexerID || query.QueryType != "interactive" || query.Keyword != h.Query || query.ConfigVersion != src.row.ConfigVersion || query.Page != p.page || query.RssJobID.Valid {
				return nil, ports.ErrInteractiveSearchConflict
			}
			if query.State != "succeeded" {
				view.Partial = true
				continue
			}
			if !query.FinishedAt.Valid || query.FinishedAt.Time.After(h.Deadline) {
				return nil, ports.ErrInteractiveSearchConflict
			}
			page, e := q.GetIndexerQueryPage(ctx, p.query)
			if e != nil {
				return nil, e
			}
			if page.PageOffset != int64(p.page)*50 || page.PageLimit != 50 || page.ItemCount != query.ResultsCount {
				return nil, ports.ErrInteractiveSearchConflict
			}
			candidates, e := q.ListQueryCandidates(ctx, p.query)
			if e != nil {
				return nil, e
			}
			var associated int
			if e = tx.QueryRow(ctx, `SELECT count(*) FROM indexer_query_candidate WHERE query_id=$1`, p.query).Scan(&associated); e != nil {
				return nil, e
			}
			if len(candidates) != int(query.ResultsCount) || associated != len(candidates) {
				return nil, ports.ErrInteractiveSearchConflict
			}
			if p.page == 0 {
				firstComplete, e = q.IsIndexerQueryComplete(ctx, p.query)
				if e != nil {
					return nil, e
				}
			}
			completed++
			view.QueryIDs = append(view.QueryIDs, p.query)
			for _, c := range candidates {
				if c.IndexerID != src.row.IndexerID || c.SourceConfigVersion != src.row.ConfigVersion {
					return nil, ports.ErrInteractiveSearchConflict
				}
				if seen[c.ID] {
					continue
				}
				seen[c.ID] = true
				out.Candidates = append(out.Candidates, ports.InteractiveSearchCandidate{Candidate: searchCandidateDTO(c), SourceName: src.name})
			}
		}
		// Retain the existing conservative first-page completeness interpretation.
		view.Partial = view.Partial || !firstComplete || view.State != "succeeded" || completed == 0
		out.Partial = out.Partial || view.Partial
		out.Sources = append(out.Sources, view)
	}
	out.TotalCandidates = len(out.Candidates)
	if out.State == "succeeded" && out.Partial {
		out.State = "partial"
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return out, nil
}

type interactivePage struct {
	page       int32
	query, job uuid.UUID
	token      int64
}

func interactivePages(ctx context.Context, tx pgx.Tx, search, indexer uuid.UUID) ([]interactivePage, error) {
	rows, e := tx.Query(ctx, `SELECT page,query_id,source_job_id,source_token FROM interactive_search_query WHERE search_id=$1 AND indexer_id=$2 ORDER BY page`, search, indexer)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []interactivePage{}
	for rows.Next() {
		var v interactivePage
		if e = rows.Scan(&v.page, &v.query, &v.job, &v.token); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
