package postgres

import (
	"context"
	"encoding/json"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type interactiveSourceRow struct {
	SearchID      uuid.UUID `json:"search_id"`
	IndexerID     uuid.UUID `json:"indexer_id"`
	ConfigVersion int64     `json:"config_version"`
	JobID         uuid.UUID `json:"job_id"`
	State         string    `json:"state"`
	Reason        string    `json:"reason"`
}

// authorizeInteractiveSource takes locks in account, object, header, source, job order.
// Reading immutable scope before the first lock confers no authority.
func authorizeInteractiveSource(ctx context.Context, q *db.Queries, l ports.InteractiveSearchSourceLease, loading bool) error {
	if q == nil {
		return ports.ErrUnavailable
	}
	h, e := interactiveDecode(q.InteractiveSearchReadHeader(ctx, l.Task.SearchID))
	if e != nil {
		return e
	}
	u, e := q.InteractiveSearchLockAccount(ctx, h.ActorID)
	if e != nil {
		return e
	}
	if !u.IsActive || u.Role != h.ActorRole {
		return ports.ErrForbidden
	}
	if e := AuthorizeMovieDiscoverySearch(ctx, q, h.ID); e != nil {
		return e
	}
	if e := AuthorizeTVEpisodeDiscoverySearch(ctx, q, h.ID); e != nil {
		return e
	}
	if h.SubscriptionID != nil {
		v, e := q.InteractiveSearchLockSubscription(ctx, *h.SubscriptionID)
		if e != nil {
			return e
		}
		if (h.ActorRole != ports.RoleAdmin && v.UserID != h.ActorID) || (h.MediaID != nil && v.MediaID != *h.MediaID) {
			return ports.ErrForbidden
		}
	}
	if h.MediaID != nil {
		if _, e = q.InteractiveSearchLockMedia(ctx, *h.MediaID); e != nil {
			return e
		}
	}
	h, e = interactiveDecode(q.InteractiveSearchLockHeader(ctx, l.Task.SearchID))
	if e != nil {
		return e
	}
	if h.State != "running" || h.CancelledAt != nil || h.ActorID != l.ActorID || h.Query != l.Query || !h.Deadline.Equal(l.Deadline) {
		return ports.ErrInteractiveSearchConflict
	}
	raw, e := q.InteractiveSearchLockSource(ctx, db.InteractiveSearchLockSourceParams{SearchID: h.ID, IndexerID: l.Task.IndexerID})
	if e != nil {
		return e
	}
	var src interactiveSourceRow
	if json.Unmarshal(raw, &src) != nil {
		return ports.ErrUnavailable
	}
	if src.ConfigVersion != l.Task.ConfigVersion || src.JobID != l.Task.Lease.ID || (!loading && src.State != "running") || (loading && src.State != "queued" && src.State != "running") {
		return ports.ErrInteractiveSearchConflict
	}
	if _, e = q.InteractiveSearchLockJob(ctx, src.JobID); e != nil {
		return e
	}
	current, e := q.InteractiveSearchSourceCurrent(ctx, db.InteractiveSearchSourceCurrentParams{ID: src.IndexerID, ConfigVersion: src.ConfigVersion})
	if e != nil {
		return e
	}
	if !current {
		return ports.ErrInteractiveSearchConflict
	}
	valid, e := q.InteractiveSearchJobValid(ctx, db.InteractiveSearchJobValidParams{JobID: src.JobID, Owner: l.Task.Lease.Owner, Token: l.Task.Lease.Token, SearchID: h.ID, IndexerID: src.IndexerID, ConfigVersion: src.ConfigVersion, Deadline: pgtype.Timestamptz{Time: h.Deadline, Valid: true}})
	if e != nil {
		return e
	}
	if !valid {
		return ports.ErrInteractiveSearchConflict
	}
	return nil
}

// AuthorizeInteractiveSearchSource must run before endpoint/query locks in the caller's transaction.
func AuthorizeInteractiveSearchSource(ctx context.Context, q *db.Queries, l ports.InteractiveSearchSourceLease) (err error) {
	defer func() { err = interactiveError(ctx, err) }()
	return authorizeInteractiveSource(ctx, q, l, false)
}

// LinkInteractiveSearchQuery publishes ownership at begin; page facts publish at commit.
func LinkInteractiveSearchQuery(ctx context.Context, q *db.Queries, l ports.InteractiveSearchSourceLease, queryID uuid.UUID, page int32) (err error) {
	defer func() { err = interactiveError(ctx, err) }()
	if page < 0 || page > 3 || queryID == uuid.Nil {
		return ports.ErrRejected
	}
	if e := authorizeInteractiveSource(ctx, q, l, false); e != nil {
		return e
	}
	n, e := q.InteractiveSearchLinkQuery(ctx, db.InteractiveSearchLinkQueryParams{SearchID: l.Task.SearchID, IndexerID: l.Task.IndexerID, Page: page, QueryID: queryID, JobID: l.Task.Lease.ID, Token: l.Task.Lease.Token, Keyword: l.Query, ConfigVersion: l.Task.ConfigVersion})
	if e != nil {
		return e
	}
	if n != 1 {
		return ports.ErrInteractiveSearchConflict
	}
	return nil
}
func (s *InteractiveSearchStore) LoadInteractiveSearchSource(ctx context.Context, t ports.InteractiveSearchSourceTask) (out *ports.InteractiveSearchSourceLease, err error) {
	defer func() { err = interactiveError(ctx, err) }()
	tx, e := s.begin(ctx, false)
	if e != nil {
		return nil, e
	}
	defer managementRollback(ctx, tx)
	q := db.New(tx)
	h, e := interactiveDecode(q.InteractiveSearchReadHeader(ctx, t.SearchID))
	if e != nil {
		return nil, e
	}
	out = &ports.InteractiveSearchSourceLease{Task: t, ActorID: h.ActorID, Query: h.Query, Deadline: h.Deadline}
	if e = authorizeInteractiveSource(ctx, q, *out, true); e != nil {
		return nil, e
	}
	if _, e = tx.Exec(ctx, `UPDATE interactive_search_source SET state='running',reason='' WHERE search_id=$1 AND indexer_id=$2`, t.SearchID, t.IndexerID); e != nil {
		return nil, e
	}
	if e = authorizeInteractiveSource(ctx, q, *out, false); e != nil {
		return nil, e
	}
	return out, tx.Commit(ctx)
}
func interactiveDerive(ctx context.Context, tx pgx.Tx, id uuid.UUID) error {
	_, e := tx.Exec(ctx, `UPDATE interactive_search h SET state=CASE
 WHEN EXISTS(SELECT 1 FROM interactive_search_source s WHERE s.search_id=h.id AND s.state IN ('queued','running')) THEN 'running'
 WHEN NOT EXISTS(SELECT 1 FROM interactive_search_source s WHERE s.search_id=h.id AND s.state<>'succeeded') THEN 'succeeded'
 WHEN EXISTS(SELECT 1 FROM interactive_search_source s WHERE s.search_id=h.id AND s.state='succeeded') OR EXISTS(SELECT 1 FROM interactive_search_query a JOIN indexer_query q ON q.id=a.query_id WHERE a.search_id=h.id AND q.state='succeeded') THEN 'partial'
 ELSE 'failed' END WHERE h.id=$1 AND h.state='running'`, id)
	return e
}
func (s *InteractiveSearchStore) FinishInteractiveSearchSource(ctx context.Context, l ports.InteractiveSearchSourceLease, outcome string) (err error) {
	defer func() { err = interactiveError(ctx, err) }()
	switch outcome {
	case "succeeded", "failed", "deferred", "timeout":
	default:
		return ports.ErrRejected
	}
	tx, e := s.begin(ctx, false)
	if e != nil {
		return e
	}
	defer managementRollback(ctx, tx)
	q := db.New(tx)
	if e = authorizeInteractiveSource(ctx, q, l, false); e != nil {
		return e
	}
	if outcome == "succeeded" {
		var valid bool
		e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM interactive_search_query WHERE search_id=$1 AND indexer_id=$2) AND NOT EXISTS(SELECT 1 FROM interactive_search_query a JOIN indexer_query q ON q.id=a.query_id LEFT JOIN indexer_query_page p ON p.query_id=q.id WHERE a.search_id=$1 AND a.indexer_id=$2 AND (q.state<>'succeeded' OR p.query_id IS NULL))`, l.Task.SearchID, l.Task.IndexerID).Scan(&valid)
		if e != nil {
			return e
		}
		if !valid {
			return ports.ErrInteractiveSearchConflict
		}
	}
	if outcome != "succeeded" {
		if e = failInteractiveSearchQueries(ctx, tx, l.Task.SearchID, l.Task.IndexerID, outcome); e != nil {
			return e
		}
	}
	// Final authorization after every potentially blocking boundary rejects late writes.
	if e = authorizeInteractiveSource(ctx, q, l, false); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE interactive_search_source SET state=$3,reason=$3 WHERE search_id=$1 AND indexer_id=$2`, l.Task.SearchID, l.Task.IndexerID, outcome); e != nil {
		return e
	}
	if e = interactiveDerive(ctx, tx, l.Task.SearchID); e != nil {
		return e
	}
	tag, e := tx.Exec(ctx, `UPDATE job SET state=CASE WHEN $4='succeeded' THEN 'succeeded' ELSE 'failed' END,result=jsonb_build_object('outcome',$4::text),error=NULL,finished_at=clock_timestamp(),updated_at=clock_timestamp(),lease_owner=NULL,lease_until=NULL WHERE id=$1 AND state='running' AND lease_owner=$2 AND lease_token=$3 AND lease_until>clock_timestamp() AND clock_timestamp()<$5`, l.Task.Lease.ID, l.Task.Lease.Owner, l.Task.Lease.Token, outcome, l.Deadline)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return ports.ErrInteractiveSearchConflict
	}
	return tx.Commit(ctx)
}
func interactiveStop(ctx context.Context, tx pgx.Tx, id uuid.UUID, reason string) error {
	// Header is locked first; each source and its job are locked in the same order as callbacks.
	rows, e := tx.Query(ctx, `SELECT indexer_id,job_id FROM interactive_search_source WHERE search_id=$1 AND state IN ('queued','running') ORDER BY indexer_id FOR UPDATE`, id)
	if e != nil {
		return e
	}
	jobs := []uuid.UUID{}
	for rows.Next() {
		var i, j uuid.UUID
		if e = rows.Scan(&i, &j); e != nil {
			rows.Close()
			return e
		}
		jobs = append(jobs, j)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, j := range jobs {
		if _, e = tx.Exec(ctx, `UPDATE job SET state='dead',error=$2,lease_token=lease_token + 1,lease_owner=NULL,lease_until=NULL,finished_at=clock_timestamp(),updated_at=clock_timestamp() WHERE id=$1 AND kind='interactive_search_source' AND state IN ('queued','running')`, j, reason); e != nil {
			return e
		}
	}
	if e = failInteractiveSearchQueries(ctx, tx, id, uuid.Nil, reason); e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `UPDATE interactive_search_source SET state=$2,reason=$2 WHERE search_id=$1 AND state IN ('queued','running')`, id, reason)
	return e
}
func (s *InteractiveSearchStore) ExpireInteractiveSearches(ctx context.Context, limit int32) (n int, err error) {
	defer func() { err = interactiveError(ctx, err) }()
	if limit < 1 || limit > 256 {
		return 0, ports.ErrRejected
	}
	// Discover without locks; acquire each frozen account before its header/source/job locks.
	if s == nil || s.pool == nil {
		return 0, ports.ErrUnavailable
	}
	rows, e := s.pool.Query(ctx, `SELECT h.id FROM interactive_search h WHERE h.state='running' OR EXISTS(SELECT 1 FROM interactive_search_source s JOIN job j ON j.id=s.job_id WHERE s.search_id=h.id AND j.state IN ('queued','running')) ORDER BY h.deadline,h.id LIMIT $1`, limit)
	if e != nil {
		return 0, e
	}
	ids := []uuid.UUID{}
	for rows.Next() {
		var id uuid.UUID
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return 0, e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return 0, e
	}
	for _, id := range ids {
		changed, e := s.expireOne(ctx, id)
		if e != nil {
			return n, e
		}
		if changed {
			n++
		}
	}
	return n, nil
}
func (s *InteractiveSearchStore) expireOne(ctx context.Context, id uuid.UUID) (bool, error) {
	tx, e := s.begin(ctx, false)
	if e != nil {
		return false, e
	}
	defer managementRollback(ctx, tx)
	q := db.New(tx)
	h, e := interactiveDecode(q.InteractiveSearchReadHeader(ctx, id))
	if e != nil {
		return false, e
	}
	// Even revoked accounts can have their logical work fenced; they gain no execution authority.
	if _, e = q.InteractiveSearchLockAccount(ctx, h.ActorID); e != nil {
		return false, e
	}
	h, e = interactiveDecode(q.InteractiveSearchLockHeader(ctx, id))
	if e != nil {
		return false, e
	}
	now, e := q.GetSearchDatabaseTime(ctx)
	if e != nil {
		return false, e
	}
	if h.CancelledAt != nil || h.State == "cancelled" || !now.Time.Before(h.Deadline) {
		reason := "timeout"
		if h.CancelledAt != nil || h.State == "cancelled" {
			reason = "cancelled"
		}
		if e = interactiveStop(ctx, tx, id, reason); e != nil {
			return false, e
		}
		if _, e = tx.Exec(ctx, `UPDATE interactive_search SET state=$2 WHERE id=$1 AND state='running'`, id, reason); e != nil {
			return false, e
		}
		return true, tx.Commit(ctx)
	}
	rows, e := tx.Query(ctx, `SELECT indexer_id,job_id FROM interactive_search_source WHERE search_id=$1 AND state IN ('queued','running') ORDER BY indexer_id FOR UPDATE`, id)
	if e != nil {
		return false, e
	}
	type item struct{ i, j uuid.UUID }
	items := []item{}
	for rows.Next() {
		var v item
		if e = rows.Scan(&v.i, &v.j); e != nil {
			rows.Close()
			return false, e
		}
		items = append(items, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return false, e
	}
	changed := false
	for _, v := range items {
		// Restart before any query is R0. Once begin has published a query, recovery is conservative.
		raw, e := q.InteractiveSearchLockJob(ctx, v.j)
		if e != nil {
			return false, e
		}
		var j struct {
			State      string     `json:"state"`
			LeaseUntil *time.Time `json:"lease_until"`
		}
		if json.Unmarshal(raw, &j) != nil {
			return false, ports.ErrUnavailable
		}
		if j.State != "running" || j.LeaseUntil == nil {
			continue
		}
		var expired bool
		if e = tx.QueryRow(ctx, `SELECT $1::timestamptz<=clock_timestamp()`, j.LeaseUntil).Scan(&expired); e != nil {
			return false, e
		}
		if !expired {
			continue
		}
		var started bool
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM interactive_search_query WHERE search_id=$1 AND indexer_id=$2)`, id, v.i).Scan(&started); e != nil {
			return false, e
		}
		state, source, reason := "queued", "queued", ""
		if started {
			state, source, reason = "dead", "failed", "lease_expired"
			if e = failInteractiveSearchQueries(ctx, tx, id, v.i, "lease_expired"); e != nil {
				return false, e
			}
		}
		if _, e = tx.Exec(ctx, `UPDATE job SET state=$2,max_attempts=CASE WHEN $2='queued' THEN GREATEST(max_attempts,attempts+1) ELSE max_attempts END,lease_token=lease_token + 1,lease_owner=NULL,lease_until=NULL,error=NULL,updated_at=clock_timestamp(),finished_at=CASE WHEN $2='dead' THEN clock_timestamp() ELSE NULL END WHERE id=$1`, v.j, state); e != nil {
			return false, e
		}
		if _, e = tx.Exec(ctx, `UPDATE interactive_search_source SET state=$3,reason=$4 WHERE search_id=$1 AND indexer_id=$2`, id, v.i, source, reason); e != nil {
			return false, e
		}
		changed = true
	}
	if e = interactiveDerive(ctx, tx, id); e != nil {
		return false, e
	}
	return changed, tx.Commit(ctx)
}
