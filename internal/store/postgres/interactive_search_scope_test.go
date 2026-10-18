package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type interactiveIDs struct{}

func (interactiveIDs) NewUUID() uuid.UUID    { return uuid.New() }
func (interactiveIDs) NewUUIDString() string { return uuid.NewString() }

const interactiveHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const interactiveOtherHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func interactiveExec(t *testing.T, p *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, e := p.Exec(t.Context(), sql, args...); e != nil {
		t.Fatal(e)
	}
}
func interactiveCount(t *testing.T, p *pgxpool.Pool, table string) int {
	t.Helper()
	var n int
	if e := p.QueryRow(t.Context(), "SELECT count(*) FROM "+table).Scan(&n); e != nil {
		t.Fatal(e)
	}
	return n
}

func TestInteractiveSearchScopeAndReceiptPG(t *testing.T) {
	p, _, admin, s := interactiveFixture(t)
	ctx := t.Context()
	interactiveSite(t, p, "Site", "offline", true, false)
	a := pgtest.CreateUserActor(t, db.New(p), &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleMember})
	other := pgtest.CreateUserActor(t, db.New(p), &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleMember})
	media, root, sub := uuid.New(), uuid.New(), uuid.New()
	interactiveExec(t, p, `INSERT INTO media(id,title,media_type) VALUES($1,'movie','movie')`, media)
	interactiveExec(t, p, `INSERT INTO library_root(id,path,media_type,name) VALUES($1,'/configured-test-root','movie','root')`, root)
	interactiveExec(t, p, `INSERT INTO subscription(id,user_id,media_id,root_id) VALUES($1,$2,$3,$4)`, sub, a.ID, media, root)
	in := interactiveInput()
	in.MediaID = &media
	in.SubscriptionID = &sub
	if _, e := s.CreateInteractiveSearch(ctx, other, in, "wrong-scope", interactiveHash); !errors.Is(e, ports.ErrForbidden) {
		t.Fatalf("scope %v", e)
	}
	h, e := s.CreateInteractiveSearch(ctx, a, in, "member-scope", interactiveHash)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.ReadInteractiveSearch(ctx, admin, h.ID); !errors.Is(e, ports.ErrForbidden) {
		t.Fatalf("admin other owner %v", e)
	}
	interactiveExec(t, p, `UPDATE subscription SET user_id=$2 WHERE id=$1`, sub, other.ID)
	for _, f := range []func() error{func() error { _, e := s.CreateInteractiveSearch(ctx, a, in, "member-scope", interactiveHash); return e }, func() error { _, e := s.ReadInteractiveSearchReceipt(ctx, a, "member-scope", "create"); return e }, func() error { _, e := s.ReadInteractiveSearch(ctx, a, h.ID); return e }, func() error {
		_, e := s.CancelInteractiveSearch(ctx, a, h.ID, "scope-cancel", interactiveHash)
		return e
	}} {
		if e = f(); !errors.Is(e, ports.ErrForbidden) {
			t.Fatalf("current scope %v", e)
		}
	}
	// Token scopes and a forged current role never elevate a member.
	token := *a
	token.Scopes = []string{"admin"}
	if _, e = s.CreateInteractiveSearch(ctx, &token, in, "token-reject", interactiveHash); !errors.Is(e, ports.ErrForbidden) {
		t.Fatalf("token %v", e)
	}
	in.SubscriptionID = nil
	in.MediaID = nil
	if _, e = s.CreateInteractiveSearch(ctx, a, in, "member-global", interactiveHash); e != nil {
		t.Fatal(e)
	}
}
func TestInteractiveSearchDeadlineAndHistoryPG(t *testing.T) {
	p, dsn, a, s := interactiveFixture(t)
	ctx := t.Context()
	// Empty 86 can go down and up; no schema content is discarded.
	if v, e := postgres.Rollback(ctx, dsn, "../../../migrations"); e != nil || v != 85 {
		t.Fatalf("empty down: %d %v", v, e)
	}
	if v, e := postgres.Migrate(ctx, dsn, "../../../migrations"); e != nil || v != 86 {
		t.Fatalf("up: %d %v", v, e)
	}
	site := interactiveSite(t, p, "Site", "offline", true, false)
	// An already durable deadline requires no real-time sleep and exercises the real DB clock.
	id, job := uuid.New(), uuid.New()
	interactiveExec(t, p, `INSERT INTO interactive_search(id,actor_id,actor_role,query,ranking_policy,source_count,created_at,deadline) VALUES($1,$2,'admin','expired','{}',1,statement_timestamp()-interval '31 seconds',statement_timestamp()-interval '1 second')`, id, a.ID)
	interactiveExec(t, p, `INSERT INTO job(id,kind,dedupe_key,state,payload,lease_owner,lease_token,lease_until) VALUES($1,'interactive_search_source',$2,'running',jsonb_build_object('search_id',$3::uuid,'indexer_id',$4::uuid,'config_version',1),'interactive-test',1,clock_timestamp()+interval '60 seconds')`, job, id.String()+":"+site.String(), id, site)
	interactiveExec(t, p, `INSERT INTO interactive_search_source(search_id,indexer_id,config_version,job_id,state) VALUES($1,$2,1,$3,'running')`, id, site, job)
	task := ports.InteractiveSearchSourceTask{SearchID: id, IndexerID: site, ConfigVersion: 1, Lease: ports.JobLease{ID: job, Owner: "interactive-test", Token: 1}}
	if _, e := s.LoadInteractiveSearchSource(ctx, task); !errors.Is(e, ports.ErrInteractiveSearchConflict) {
		t.Fatalf("deadline load %v", e)
	}
	before := interactiveState(t, p)
	snap, e := s.ReadInteractiveSearch(ctx, a, id)
	if e != nil || snap.State != "running" {
		t.Fatalf("GET durable state %v", e)
	}
	if string(before) != string(interactiveState(t, p)) {
		t.Fatal("deadline GET wrote timeout")
	}
	n, e := s.ExpireInteractiveSearches(ctx, 10)
	if e != nil || n != 1 {
		t.Fatalf("expire %d %v", n, e)
	}
	snap, e = s.ReadInteractiveSearch(ctx, a, id)
	if e != nil || snap.State != "timeout" || snap.Sources[0].State != "timeout" {
		t.Fatalf("durable timeout %v", e)
	}
	if _, e = postgres.Rollback(ctx, dsn, "../../../migrations"); e == nil || !strings.Contains(e.Error(), "durable history") {
		t.Fatalf("history downgrade %v", e)
	}
	// The continuation query ignores the health projection and retains config fencing.
	delay, e := db.New(p).GetInteractiveSearchContinuationDelay(ctx, db.GetInteractiveSearchContinuationDelayParams{ID: site, ConfigVersion: 1})
	if e != nil || delay < 0 {
		t.Fatalf("offline delay %d %v", delay, e)
	}
	// A pending lock cannot make a once-valid lease write after its durable deadline.
	h := interactiveCreate(t, s, a, "late-owner")
	l := interactiveLoad(t, p, s, h, site)
	l.Deadline = l.Deadline.Add(time.Second)
	if e = s.FinishInteractiveSearchSource(context.Background(), l, "failed"); !errors.Is(e, ports.ErrInteractiveSearchConflict) {
		t.Fatalf("deadline identity %v", e)
	}
}

func TestInteractiveSearchLiveSessionBoundPG(t *testing.T) {
	p, _, a, s := interactiveFixture(t)
	interactiveSite(t, p, "Site", "offline", true, false)
	ctx := t.Context()
	first := interactiveCreate(t, s, a, "bound-first")
	var wg sync.WaitGroup
	var mu sync.Mutex
	accepted, rejected := 0, 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, e := s.CreateInteractiveSearch(ctx, a, interactiveInput(), fmt.Sprintf("bound-race-%d", i), interactiveHash)
			mu.Lock()
			defer mu.Unlock()
			if e == nil {
				accepted++
			} else if errors.Is(e, ports.ErrRejected) {
				rejected++
			} else {
				t.Errorf("create bound: %v", e)
			}
		}(i)
	}
	wg.Wait()
	if accepted != 3 || rejected != 5 || interactiveCount(t, p, "interactive_search") != 4 || interactiveCount(t, p, "job") != 4 {
		t.Fatalf("bound accepted=%d rejected=%d", accepted, rejected)
	}
	before := interactiveState(t, p)
	old, e := s.CreateInteractiveSearch(ctx, a, interactiveInput(), "bound-first", interactiveHash)
	if e != nil || old.ID != first.ID {
		t.Fatalf("receipt at bound %v", e)
	}
	if string(before) != string(interactiveState(t, p)) {
		t.Fatal("receipt changed bound state")
	}
	if _, e = s.CancelInteractiveSearch(ctx, a, first.ID, "bound-cancel", interactiveHash); e != nil {
		t.Fatal(e)
	}
	if _, e = s.CreateInteractiveSearch(ctx, a, interactiveInput(), "bound-after-cancel", interactiveHash); e != nil {
		t.Fatalf("cancel frees logical slot %v", e)
	}
}

func TestInteractiveSearchQueryOwnershipAndAccountLockPG(t *testing.T) {
	p, _, a, s := interactiveFixture(t)
	ctx := t.Context()
	site := interactiveSite(t, p, "Site", "offline", true, false)
	first := interactiveCreate(t, s, a, "ownership-one")
	second := interactiveCreate(t, s, a, "ownership-two")
	one := interactiveLoad(t, p, s, first, site)
	two := interactiveLoad(t, p, s, second, site)
	tx, e := p.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	q := db.New(tx)
	if e = postgres.AuthorizeInteractiveSearchSource(ctx, q, one); e != nil {
		t.Fatal(e)
	}
	query := uuid.New()
	if _, e = tx.Exec(ctx, `INSERT INTO indexer_query(id,indexer_id,query_type,keyword,page,config_version,state) VALUES($1,$2,'interactive',$3,0,1,'running')`, query, site, one.Query); e != nil {
		t.Fatal(e)
	}
	if e = postgres.LinkInteractiveSearchQuery(ctx, q, one, query, 0); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	tx, e = p.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if e = postgres.LinkInteractiveSearchQuery(ctx, db.New(tx), two, query, 0); e == nil {
		t.Fatal("query shared across sessions")
	}
	if e = tx.Rollback(ctx); e != nil {
		t.Fatal(e)
	}
	// Distinct running interactive executions with identical text are admitted.
	interactivePublish(t, p, two, 0, false)
	snap, e := s.ReadInteractiveSearch(ctx, a, second.ID)
	if e != nil || !snap.Partial || len(snap.Candidates) != 0 {
		t.Fatalf("conservative empty page %v", e)
	}
	lock, e := p.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = lock.Exec(ctx, `SELECT id FROM app_user WHERE id=$1 FOR UPDATE`, a.ID); e != nil {
		t.Fatal(e)
	}
	blocked, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	_, e = s.ReadInteractiveSearch(blocked, a, first.ID)
	if !errors.Is(e, context.DeadlineExceeded) {
		t.Fatalf("GET did not acquire account lock: %v", e)
	}
	if e = lock.Rollback(ctx); e != nil {
		t.Fatal(e)
	}
	// Scope is rechecked by the execution helper using the durable actor, not caller claims.
	interactiveExec(t, p, `UPDATE app_user SET is_active=false WHERE id=$1`, a.ID)
	tx, e = p.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	e = postgres.AuthorizeInteractiveSearchSource(ctx, db.New(tx), two)
	if !errors.Is(e, ports.ErrForbidden) {
		t.Fatalf("revoked helper %v", e)
	}
	if e = tx.Rollback(ctx); e != nil {
		t.Fatal(e)
	}
}
func TestInteractiveSearchFinishTailAndAfterBeginRecoveryPG(t *testing.T) {
	p, _, a, s := interactiveFixture(t)
	ctx := t.Context()
	site := interactiveSite(t, p, "Site", "offline", true, false)
	h := interactiveCreate(t, s, a, "finish-tail")
	l := interactiveLoad(t, p, s, h, site)
	interactiveExec(t, p, `CREATE FUNCTION fail_interactive_finish() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.state='failed' THEN RAISE EXCEPTION 'test finish tail'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_interactive_finish BEFORE UPDATE ON job FOR EACH ROW EXECUTE FUNCTION fail_interactive_finish()`)
	before := interactiveState(t, p)
	if e := s.FinishInteractiveSearchSource(ctx, l, "failed"); !errors.Is(e, ports.ErrUnavailable) {
		t.Fatalf("finish fault %v", e)
	}
	if string(before) != string(interactiveState(t, p)) {
		t.Fatal("finish partial commit")
	}
	interactiveExec(t, p, `DROP TRIGGER fail_interactive_finish ON job`)
	interactivePublish(t, p, l, 1, false)
	interactiveExec(t, p, `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, l.Task.Lease.ID)
	if n, e := s.ExpireInteractiveSearches(ctx, 10); e != nil || n != 1 {
		t.Fatalf("begun recovery %d %v", n, e)
	}
	snap, e := s.ReadInteractiveSearch(ctx, a, h.ID)
	if e != nil || snap.State != "partial" || len(snap.Candidates) != 1 || snap.Sources[0].Reason != "lease_expired" {
		t.Fatalf("begun history lost %v %+v", e, snap)
	}
}
