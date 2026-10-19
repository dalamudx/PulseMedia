package postgres_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func interactiveFixture(t *testing.T) (*pgxpool.Pool, string, *ports.Actor, *postgres.InteractiveSearchStore) {
	t.Helper()
	dsn := pgtest.GetCluster(t).NewTestDB(t)
	if v, e := postgres.Migrate(t.Context(), dsn, repositoryMigrationsThrough(t, 86)); e != nil || v != 86 {
		t.Fatalf("migrate86: %d %v", v, e)
	}
	p, e := postgres.NewPool(t.Context(), dsn, postgres.DefaultPoolConfig())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(p.Close)
	var version string
	if e = p.QueryRow(t.Context(), `SHOW server_version`).Scan(&version); e != nil || !strings.HasPrefix(version, "18.6") {
		t.Fatalf("requires PG18.6: %s %v", version, e)
	}
	a := pgtest.CreateUserActor(t, db.New(p), &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleAdmin})
	return p, dsn, a, postgres.NewInteractiveSearchStore(p, interactiveIDs{})
}
func interactiveSite(t *testing.T, p *pgxpool.Pool, name, health string, enabled, deleted bool) uuid.UUID {
	t.Helper()
	id := uuid.New()
	interactiveExec(t, p, `INSERT INTO indexer(id,name,url,api_key_encrypted,health) VALUES($1,$2,'https://site.test',decode('01','hex'),$3)`, id, name, health)
	interactiveExec(t, p, `INSERT INTO indexer_lifecycle(indexer_id,enabled,deleted_at) VALUES($1,$2,CASE WHEN $3 THEN clock_timestamp() ELSE NULL END)`, id, enabled, deleted)
	return id
}
func interactiveInput() ports.PreparedInteractiveSearch {
	return ports.PreparedInteractiveSearch{InteractiveSearchInput: ports.InteractiveSearchInput{Query: "private-query-material"}, RankingPolicy: quality.DefaultPolicy()}
}
func interactiveCreate(t *testing.T, s *postgres.InteractiveSearchStore, a *ports.Actor, key string) *ports.InteractiveSearchHeader {
	t.Helper()
	h, e := s.CreateInteractiveSearch(t.Context(), a, interactiveInput(), key, interactiveHash)
	if e != nil {
		t.Fatal(e)
	}
	return h
}
func interactiveClaim(t *testing.T, p *pgxpool.Pool, h *ports.InteractiveSearchHeader, indexer uuid.UUID) ports.InteractiveSearchSourceTask {
	t.Helper()
	var task ports.InteractiveSearchSourceTask
	var raw []byte
	e := p.QueryRow(t.Context(), `UPDATE job SET state='running',lease_owner='interactive-test',lease_token=lease_token+1,attempts=attempts+1,lease_until=clock_timestamp()+interval '60 seconds' WHERE id=(SELECT job_id FROM interactive_search_source WHERE search_id=$1 AND indexer_id=$2) RETURNING id,payload,lease_token`, h.ID, indexer).Scan(&task.Lease.ID, &raw, &task.Lease.Token)
	if e != nil {
		t.Fatal(e)
	}
	lease := task.Lease
	if json.Unmarshal(raw, &task) != nil {
		t.Fatal("payload")
	}
	task.Lease = lease
	task.Lease.Owner = "interactive-test"
	return task
}
func interactiveLoad(t *testing.T, p *pgxpool.Pool, s *postgres.InteractiveSearchStore, h *ports.InteractiveSearchHeader, indexer uuid.UUID) ports.InteractiveSearchSourceLease {
	t.Helper()
	l, e := s.LoadInteractiveSearchSource(t.Context(), interactiveClaim(t, p, h, indexer))
	if e != nil {
		t.Fatal(e)
	}
	return *l
}
func interactivePublish(t *testing.T, p *pgxpool.Pool, l ports.InteractiveSearchSourceLease, count int, complete bool) uuid.UUID {
	t.Helper()
	ctx := t.Context()
	tx, e := p.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)
	if e = postgres.AuthorizeInteractiveSearchSource(ctx, q, l); e != nil {
		t.Fatal(e)
	}
	id := uuid.New()
	if _, e = tx.Exec(ctx, `INSERT INTO indexer_query(id,indexer_id,query_type,keyword,page,config_version,state) VALUES($1,$2,'interactive',$3,0,$4,'running')`, id, l.Task.IndexerID, l.Query, l.Task.ConfigVersion); e != nil {
		t.Fatal(e)
	}
	if e = postgres.LinkInteractiveSearchQuery(ctx, q, l, id, 0); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < count; i++ {
		candidate := uuid.New()
		if _, e = tx.Exec(ctx, `INSERT INTO release_candidate(id,indexer_id,title,guid,link_encrypted,size_bytes,seeders,source_config_version) VALUES($1,$2,'private-candidate',$3,decode('01','hex'),100,3,$4)`, candidate, l.Task.IndexerID, candidate.String(), l.Task.ConfigVersion); e != nil {
			t.Fatal(e)
		}
		if e = q.LinkQueryCandidate(ctx, db.LinkQueryCandidateParams{QueryID: id, CandidateID: candidate}); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = tx.Exec(ctx, `UPDATE indexer_query SET state='succeeded',results_count=$2,finished_at=clock_timestamp() WHERE id=$1`, id, count); e != nil {
		t.Fatal(e)
	}
	var total *int64
	if complete {
		v := int64(count)
		total = &v
	}
	if _, e = tx.Exec(ctx, `INSERT INTO indexer_query_page(query_id,page_offset,page_limit,item_count,offset_verified,total) VALUES($1,0,50,$2,true,$3)`, id, count, total); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	return id
}
func interactiveState(t *testing.T, p *pgxpool.Pool) []byte {
	t.Helper()
	var raw []byte
	e := p.QueryRow(t.Context(), `SELECT jsonb_build_object('header',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM interactive_search x),'source',(SELECT jsonb_agg(to_jsonb(x) ORDER BY search_id,indexer_id) FROM interactive_search_source x),'job',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM job x),'audit',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM audit_log x),'event',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM outbox_event x),'receipt',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM idempotency_record x),'query',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM indexer_query x),'page',(SELECT jsonb_agg(to_jsonb(x) ORDER BY query_id) FROM indexer_query_page x))`).Scan(&raw)
	if e != nil {
		t.Fatal(e)
	}
	return raw
}
func TestInteractiveSearchCommandsPG(t *testing.T) {
	p, _, a, s := interactiveFixture(t)
	ctx := t.Context()
	if _, e := s.CreateInteractiveSearch(ctx, a, interactiveInput(), "empty-source", interactiveHash); !errors.Is(e, ports.ErrRejected) {
		t.Fatalf("empty sources: %v", e)
	}
	offline := interactiveSite(t, p, "Offline enabled", "offline", true, false)
	interactiveSite(t, p, "Disabled", "online", false, false)
	interactiveSite(t, p, "Deleted", "online", false, true)
	const n = 8
	var wg sync.WaitGroup
	receipts := make(chan *ports.InteractiveSearchHeader, n)
	errs := make(chan error, n)
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := s.CreateInteractiveSearch(ctx, a, interactiveInput(), "concurrent-create", interactiveHash)
			receipts <- r
			errs <- e
		}()
	}
	wg.Wait()
	close(receipts)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var h *ports.InteractiveSearchHeader
	for r := range receipts {
		if h == nil {
			h = r
		}
		if !reflect.DeepEqual(h, r) {
			t.Fatal("unstable receipts")
		}
	}
	if h.SourceCount != 1 || h.Deadline.Sub(h.CreatedAt) != 30*time.Second || !reflect.DeepEqual(h.RankingPolicy, quality.DefaultPolicy()) {
		t.Fatal("frozen header")
	}
	if interactiveCount(t, p, "interactive_search") != 1 || interactiveCount(t, p, "job") != 1 || interactiveCount(t, p, "audit_log") != 1 {
		t.Fatal("create did not deduplicate")
	}
	if _, e := s.CreateInteractiveSearch(ctx, a, interactiveInput(), "concurrent-create", interactiveOtherHash); !errors.Is(e, ports.ErrInteractiveSearchConflict) {
		t.Fatalf("hash fence %v", e)
	}
	before := interactiveState(t, p)
	snap, e := s.ReadInteractiveSearch(ctx, a, h.ID)
	if e != nil || snap.State != "running" || snap.Candidates == nil || snap.Sources[0].IndexerID != offline || snap.StatisticsMode != "latest_persisted" {
		t.Fatalf("running empty %v %+v", e, snap)
	}
	if !bytes.Equal(before, interactiveState(t, p)) {
		t.Fatal("GET mutation")
	}
	other := pgtest.CreateUserActor(t, db.New(p), &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleAdmin})
	if _, e = s.ReadInteractiveSearch(ctx, other, h.ID); !errors.Is(e, ports.ErrForbidden) {
		t.Fatalf("other actor: %v", e)
	}
	interactiveExec(t, p, `UPDATE app_user SET is_active=false WHERE id=$1`, a.ID)
	for _, run := range []func() error{func() error { _, e := s.ReadInteractiveSearch(ctx, a, h.ID); return e }, func() error {
		_, e := s.CreateInteractiveSearch(ctx, a, interactiveInput(), "concurrent-create", interactiveHash)
		return e
	}, func() error { _, e := s.ReadInteractiveSearchReceipt(ctx, a, "concurrent-create", "create"); return e }, func() error {
		_, e := s.CancelInteractiveSearch(ctx, a, h.ID, "cancel-command", interactiveHash)
		return e
	}} {
		if e = run(); !errors.Is(e, ports.ErrForbidden) {
			t.Fatalf("revoked %v", e)
		}
	}
	interactiveExec(t, p, `UPDATE app_user SET is_active=true WHERE id=$1`, a.ID)
	cancelled, e := s.CancelInteractiveSearch(ctx, a, h.ID, "cancel-command", interactiveHash)
	if e != nil || cancelled.CancelledAt == nil {
		t.Fatalf("cancel %v", e)
	}
	again, e := s.CancelInteractiveSearch(ctx, a, h.ID, "cancel-command", interactiveHash)
	if e != nil || !reflect.DeepEqual(again, cancelled) {
		t.Fatalf("cancel receipt %v", e)
	}
	original, e := s.CreateInteractiveSearch(ctx, a, interactiveInput(), "concurrent-create", interactiveHash)
	if e != nil || !reflect.DeepEqual(h, original) {
		t.Fatalf("create receipt after cancel %v", e)
	}
	var state string
	if e = p.QueryRow(ctx, `SELECT state FROM job WHERE kind='interactive_search_source'`).Scan(&state); e != nil || state != "dead" {
		t.Fatal("cancel job")
	}
	var events []byte
	if e = p.QueryRow(ctx, `SELECT jsonb_agg(payload) FROM outbox_event`).Scan(&events); e != nil || bytes.Contains(events, []byte("private-query")) {
		t.Fatal("global event plaintext")
	}
}
func TestInteractiveSearchProgressAndFencesPG(t *testing.T) {
	p, _, a, s := interactiveFixture(t)
	ctx := t.Context()
	one := interactiveSite(t, p, "One", "offline", true, false)
	two := interactiveSite(t, p, "Two", "rate_limited", true, false)
	h := interactiveCreate(t, s, a, "progress-create")
	first := interactiveLoad(t, p, s, h, one)
	second := interactiveLoad(t, p, s, h, two)
	query := interactivePublish(t, p, first, 1, true)
	snap, e := s.ReadInteractiveSearch(ctx, a, h.ID)
	if e != nil || snap.State != "running" || len(snap.Candidates) != 1 {
		t.Fatalf("progress page %v %+v", e, snap)
	}
	before := interactiveState(t, p)
	if _, e = s.ReadInteractiveSearch(ctx, a, h.ID); e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(before, interactiveState(t, p)) {
		t.Fatal("GET page mutation")
	}
	// Seeders describe the latest persisted source observation, even for a prior query.
	interactiveExec(t, p, `UPDATE release_candidate SET seeders=99 WHERE id=(SELECT candidate_id FROM indexer_query_candidate WHERE query_id=$1)`, query)
	snap, e = s.ReadInteractiveSearch(ctx, a, h.ID)
	if e != nil || snap.Candidates[0].Candidate.Seeders != 99 {
		t.Fatalf("latest seeders %v", e)
	}
	if e = s.FinishInteractiveSearchSource(ctx, first, "succeeded"); e != nil {
		t.Fatal(e)
	}
	bad := second
	bad.Task.Lease.Token++
	if e = s.FinishInteractiveSearchSource(ctx, bad, "failed"); !errors.Is(e, ports.ErrInteractiveSearchConflict) {
		t.Fatalf("token fence %v", e)
	}
	bad = second
	bad.Task.Lease.Owner = "other-owner"
	if e = s.FinishInteractiveSearchSource(ctx, bad, "failed"); !errors.Is(e, ports.ErrInteractiveSearchConflict) {
		t.Fatalf("owner fence %v", e)
	}
	if e = s.FinishInteractiveSearchSource(ctx, second, "deferred"); e != nil {
		t.Fatal(e)
	}
	snap, e = s.ReadInteractiveSearch(ctx, a, h.ID)
	if e != nil || snap.State != "partial" || len(snap.Candidates) != 1 {
		t.Fatalf("partial %v", e)
	}
	interactiveExec(t, p, `UPDATE indexer SET config_version=config_version+1 WHERE id=$1`, one)
	snap, e = s.ReadInteractiveSearch(ctx, a, h.ID)
	if e != nil || len(snap.Candidates) != 0 {
		t.Fatalf("stale read %v", e)
	}
	// Current configuration and revoked actor are checked at each callback boundary.
	next := interactiveCreate(t, s, a, "next-create")
	lease := interactiveLoad(t, p, s, next, one)
	interactiveExec(t, p, `UPDATE indexer SET config_version=config_version+1 WHERE id=$1`, one)
	if e = s.FinishInteractiveSearchSource(ctx, lease, "failed"); !errors.Is(e, ports.ErrInteractiveSearchConflict) {
		t.Fatalf("stale write %v", e)
	}
	interactiveExec(t, p, `UPDATE app_user SET role='member' WHERE id=$1`, a.ID)
	if _, e = s.LoadInteractiveSearchSource(ctx, lease.Task); !errors.Is(e, ports.ErrForbidden) {
		t.Fatalf("role fence %v", e)
	}
}
func TestInteractiveSearchEmptyCancelRecoveryPG(t *testing.T) {
	p, _, a, s := interactiveFixture(t)
	ctx := t.Context()
	site := interactiveSite(t, p, "Only", "offline", true, false)
	h := interactiveCreate(t, s, a, "complete-empty")
	l := interactiveLoad(t, p, s, h, site)
	interactivePublish(t, p, l, 0, true)
	if e := s.FinishInteractiveSearchSource(ctx, l, "succeeded"); e != nil {
		t.Fatal(e)
	}
	snap, e := s.ReadInteractiveSearch(ctx, a, h.ID)
	if e != nil || snap.State != "succeeded" || snap.Partial || snap.Candidates == nil || len(snap.Candidates) != 0 {
		t.Fatalf("complete empty %v %+v", e, snap)
	}
	h = interactiveCreate(t, s, a, "failed-empty")
	l = interactiveLoad(t, p, s, h, site)
	if e = s.FinishInteractiveSearchSource(ctx, l, "failed"); e != nil {
		t.Fatal(e)
	}
	snap, e = s.ReadInteractiveSearch(ctx, a, h.ID)
	if e != nil || snap.State != "failed" {
		t.Fatalf("failed empty %v", e)
	}
	h = interactiveCreate(t, s, a, "recover-before-begin")
	l = interactiveLoad(t, p, s, h, site)
	interactiveExec(t, p, `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, l.Task.Lease.ID)
	if e = s.FinishInteractiveSearchSource(ctx, l, "failed"); !errors.Is(e, ports.ErrInteractiveSearchConflict) {
		t.Fatalf("expired write %v", e)
	}
	n, e := s.ExpireInteractiveSearches(ctx, 20)
	if e != nil || n != 1 {
		t.Fatalf("R0 recovery %d %v", n, e)
	}
	var state string
	var token int64
	if e = p.QueryRow(ctx, `SELECT state,lease_token FROM job WHERE id=$1`, l.Task.Lease.ID).Scan(&state, &token); e != nil || state != "queued" || token <= l.Task.Lease.Token {
		t.Fatalf("R0 state %s %d %v", state, token, e)
	}
	claimed, e := db.New(p).ClaimJob(ctx, db.ClaimJobParams{Column1: []string{"interactive_search_source"}, Column2: "restarted-owner", Column3: 12000})
	if e != nil || claimed.ID != l.Task.Lease.ID || claimed.LeaseToken <= token {
		t.Fatalf("R0 not claimable %v", e)
	}
	task := l.Task
	task.Lease = ports.JobLease{ID: claimed.ID, Owner: "restarted-owner", Token: claimed.LeaseToken}
	reloaded, e := s.LoadInteractiveSearchSource(ctx, task)
	if e != nil {
		t.Fatal(e)
	}
	l = *reloaded
	if _, e = s.CancelInteractiveSearch(ctx, a, h.ID, "cancel-running", interactiveHash); e != nil {
		t.Fatal(e)
	}
	if e = s.FinishInteractiveSearchSource(ctx, l, "failed"); !errors.Is(e, ports.ErrInteractiveSearchConflict) {
		t.Fatalf("cancel late write %v", e)
	}
}
func TestInteractiveSearchCapAndTailRollbackPG(t *testing.T) {
	p, _, a, s := interactiveFixture(t)
	ctx := t.Context()
	for i := 0; i < 65; i++ {
		interactiveSite(t, p, "Site", "offline", true, false)
	}
	if _, e := s.CreateInteractiveSearch(ctx, a, interactiveInput(), "cap-reject", interactiveHash); !errors.Is(e, ports.ErrRejected) {
		t.Fatalf("cap %v", e)
	}
	if interactiveCount(t, p, "interactive_search") != 0 || interactiveCount(t, p, "job") != 0 {
		t.Fatal("cap partial creation")
	}
	interactiveExec(t, p, `UPDATE indexer_lifecycle SET enabled=false WHERE indexer_id<>(SELECT id FROM indexer ORDER BY id LIMIT 1)`)
	interactiveExec(t, p, `CREATE FUNCTION fail_interactive_tail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test tail fault'; END $$; CREATE TRIGGER fail_interactive_tail BEFORE INSERT ON idempotency_record FOR EACH ROW EXECUTE FUNCTION fail_interactive_tail()`)
	before := interactiveState(t, p)
	if _, e := s.CreateInteractiveSearch(ctx, a, interactiveInput(), "tail-create", interactiveHash); !errors.Is(e, ports.ErrUnavailable) {
		t.Fatalf("tail %v", e)
	}
	if !bytes.Equal(before, interactiveState(t, p)) {
		t.Fatal("tail leaked business/job/audit/event")
	}
	interactiveExec(t, p, `DROP TRIGGER fail_interactive_tail ON idempotency_record`)
	h := interactiveCreate(t, s, a, "tail-create")
	interactiveExec(t, p, `CREATE TRIGGER fail_interactive_tail BEFORE INSERT ON idempotency_record FOR EACH ROW EXECUTE FUNCTION fail_interactive_tail()`)
	before = interactiveState(t, p)
	if _, e := s.CancelInteractiveSearch(ctx, a, h.ID, "tail-cancel", interactiveHash); !errors.Is(e, ports.ErrUnavailable) {
		t.Fatalf("cancel tail %v", e)
	}
	if !bytes.Equal(before, interactiveState(t, p)) {
		t.Fatal("cancel tail leaked")
	}
}
