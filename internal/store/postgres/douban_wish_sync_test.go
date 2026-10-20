package postgres_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/google/uuid"
)

type wishFixture struct {
	*doubanSyncFixture
	store *postgres.DoubanWishSyncStore
}

func newWishFixture(t *testing.T, tv bool) *wishFixture {
	t.Helper()
	f := newDoubanSyncFixture(t)
	in := f.input()
	if !tv {
		in.TVRootID = nil
	}
	f.save(t, in, "initial")
	return &wishFixture{f, postgres.NewDoubanWishSyncStore(f.p, idgen.New())}
}
func (f *wishFixture) claim(t *testing.T) ports.JobLease {
	t.Helper()
	ctx := context.Background()
	if e := f.store.EnqueueDue(ctx); e != nil {
		t.Fatal(e)
	}
	var id uuid.UUID
	if e := f.p.QueryRow(ctx, `UPDATE job SET state='running',lease_owner='wish-test',lease_token=1,lease_until=clock_timestamp()+interval '5 minutes',attempts=1 WHERE id=(SELECT id FROM job WHERE kind=$1 AND state='queued' ORDER BY created_at,id LIMIT 1) RETURNING id`, ports.DoubanWishSyncJobKind).Scan(&id); e != nil {
		t.Fatal(e)
	}
	return ports.JobLease{ID: id, Owner: "wish-test", Token: 1}
}
func (f *wishFixture) acquire(t *testing.T) (ports.JobLease, ports.DoubanWishSyncPermit) {
	t.Helper()
	l := f.claim(t)
	p, e := f.store.Acquire(context.Background(), l)
	if e != nil {
		t.Fatal("acquire", e)
	}
	t.Cleanup(p.Close)
	return l, p
}
func wishPage(p ports.DoubanWishSyncPermit, n, total int) ports.DoubanWishPage {
	w := p.Snapshot()
	v := ports.DoubanWishPage{RequestedID: w.Config.DoubanUID, CanonicalID: w.Config.DoubanUID, ProfileNumber: "123456", MediaType: w.MediaType, Start: w.Start, Total: total, WindowEnd: min(w.Start+15, total, 15000)}
	for i := range n {
		v.Items = append(v.Items, ports.DoubanWishItem{DoubanID: fmt.Sprint(1000 + w.Start + i), Titles: []string{fmt.Sprint("Movie ", i)}, Year: 2020, MediaType: w.MediaType})
	}
	v.WindowComplete = len(v.Items) == v.WindowEnd-v.Start
	if v.WindowEnd < total && v.WindowEnd < 15000 {
		next := v.WindowEnd
		v.NextStart = &next
	}
	return v
}
func wishCount(t *testing.T, f *wishFixture, query string, want int) {
	t.Helper()
	var n int
	if e := f.p.QueryRow(context.Background(), query).Scan(&n); e != nil || n != want {
		t.Fatalf("%s: %d want %d err %v", query, n, want, e)
	}
}
func wishNoError(t *testing.T, e error) {
	t.Helper()
	if e != nil {
		t.Fatal(e)
	}
}
func wishError(t *testing.T, e error) {
	t.Helper()
	if e == nil {
		t.Fatal("expected rejection")
	}
}

func TestDoubanWishDueScheduling(t *testing.T) {
	ctx := context.Background()
	f := newWishFixture(t, false)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- f.store.EnqueueDue(ctx) }()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		wishNoError(t, e)
	}
	wishCount(t, f, `SELECT count(*) FROM douban_wish_sync_run`, 1)
	wishCount(t, f, `SELECT count(*) FROM job WHERE max_attempts=1 AND kind='douban_wish_sync'`, 1)
	t.Run("isolation", func(t *testing.T) {
		g := newWishFixture(t, false)
		g.exec(t, `UPDATE restore_isolation SET isolated=true,restore_id=gen_random_uuid() WHERE singleton`)
		wishError(t, g.store.EnqueueDue(ctx))
	})
	t.Run("disabled", func(t *testing.T) {
		g := newWishFixture(t, false)
		in := g.input()
		in.ExpectedVersion = 1
		in.Enabled = false
		g.save(t, in, "disabled")
		wishNoError(t, g.store.EnqueueDue(ctx))
		wishCount(t, g, `SELECT count(*) FROM job`, 0)
	})
	t.Run("inactive owner", func(t *testing.T) {
		g := newWishFixture(t, false)
		g.exec(t, `UPDATE app_user SET is_active=false`)
		wishNoError(t, g.store.EnqueueDue(ctx))
		wishCount(t, g, `SELECT count(*) FROM job`, 0)
	})
}
func TestDoubanWishPhysicalPermit(t *testing.T) {
	ctx := context.Background()
	f := newWishFixture(t, false)
	l, p := f.acquire(t)
	wishNoError(t, p.Check(ctx))
	_, e := f.store.Acquire(ctx, l)
	wishError(t, e)
	w := p.Snapshot()
	*w.Config.MovieRootID = uuid.New()
	if *p.Snapshot().Config.MovieRootID != f.movie {
		t.Fatal("snapshot aliases root")
	}
	p.Close()
	p.Close()
	wishError(t, p.Check(ctx))
	_, e = f.store.Acquire(ctx, l)
	wishError(t, e)
	t.Run("session loss refuses reentry", func(t *testing.T) {
		g := newWishFixture(t, false)
		lease, permit := g.acquire(t)
		var pid int
		wishNoError(t, g.p.QueryRow(ctx, `SELECT execution_pid FROM douban_wish_sync_page WHERE job_id=$1`, lease.ID).Scan(&pid))
		g.exec(t, `SELECT pg_terminate_backend($1)`, pid)
		wishError(t, permit.Check(ctx))
		permit.Close()
		_, e := g.store.Acquire(ctx, lease)
		wishError(t, e)
	})
	t.Run("wrong live pid is not permit", func(t *testing.T) {
		var held bool
		wishNoError(t, f.p.QueryRow(ctx, `SELECT pulse_douban_wish_permit_held(pulse_douban_wish_lock_key($1),pg_backend_pid())`, p.Snapshot().Config.ID).Scan(&held))
		if held {
			t.Fatal("unrelated pid accepted")
		}
	})
}
func TestDoubanWishAuthorityFences(t *testing.T) {
	cases := map[string]string{
		"owner revoked":          `UPDATE app_user SET is_active=false`,
		"owner role changed":     `UPDATE app_user SET role='member'`,
		"configuration disabled": `UPDATE douban_sync_config SET enabled=false,version=version+1`,
		"root offline":           `UPDATE library_root SET health='offline'`,
		"root wrong kind":        `UPDATE library_root SET media_type='tv'`,
		"lease expired":          `UPDATE job SET lease_until=clock_timestamp()-interval '1 second'`,
		"lease replaced":         `UPDATE job SET lease_token=2`,
		"payload mismatch":       `UPDATE job SET payload=jsonb_set(payload,'{start}','15')`,
		"restore isolation":      `UPDATE restore_isolation SET isolated=true,restore_id=gen_random_uuid() WHERE singleton`,
	}
	for name, sql := range cases {
		t.Run(name, func(t *testing.T) {
			f := newWishFixture(t, false)
			_, p := f.acquire(t)
			f.exec(t, sql)
			wishError(t, p.Check(context.Background()))
			_, e := p.StorePage(context.Background(), wishPage(p, 1, 1))
			wishError(t, e)
			wishCount(t, f, `SELECT count(*) FROM douban_wish_sync_item`, 0)
		})
	}
}
func TestDoubanWishDurableTraversal(t *testing.T) {
	ctx := context.Background()
	f := newWishFixture(t, true)
	l, p := f.acquire(t)
	v := wishPage(p, 1, 16)
	items, e := p.StorePage(ctx, v)
	wishNoError(t, e)
	if len(items) != 1 {
		t.Fatal(items)
	}
	wishError(t, p.FinishPage(ctx))
	wishNoError(t, p.StoreIdentity(ctx, items[0].ID, ports.DoubanWishMatch{State: "unresolved", MediaType: "movie"}))
	wishNoError(t, p.FinishPage(ctx))
	p.Close()
	wishCount(t, f, `SELECT count(*) FROM job WHERE state='queued'`, 1)
	var done bool
	wishNoError(t, f.p.QueryRow(ctx, `SELECT finished FROM douban_wish_sync_page WHERE job_id=$1`, l.ID).Scan(&done))
	if !done {
		t.Fatal("page not finished")
	}
	_, p2 := f.acquire(t)
	if p2.Snapshot().Start != 15 || p2.Snapshot().MediaType != "movie" {
		t.Fatal(p2.Snapshot())
	}
	items, e = p2.StorePage(ctx, wishPage(p2, 1, 16))
	wishNoError(t, e)
	wishNoError(t, p2.FinishItem(ctx, items[0].ID, "unavailable"))
	wishNoError(t, p2.FinishPage(ctx))
	p2.Close()
	_, p3 := f.acquire(t)
	if p3.Snapshot().MediaType != "tv" || p3.Snapshot().Start != 0 {
		t.Fatal(p3.Snapshot())
	}
	_, e = p3.StorePage(ctx, wishPage(p3, 0, 0))
	wishNoError(t, e)
	wishNoError(t, p3.FinishPage(ctx))
	p3.Close()
	wishCount(t, f, `SELECT count(*) FROM douban_wish_sync_run WHERE status='partial' AND incomplete`, 1)
	wishNoError(t, f.store.EnqueueDue(ctx))
	wishCount(t, f, `SELECT count(*) FROM douban_wish_sync_run`, 1)
	in := f.input()
	in.ExpectedVersion = 1
	f.save(t, in, "new-version")
	wishNoError(t, f.store.EnqueueDue(ctx))
	wishCount(t, f, `SELECT count(*) FROM douban_wish_sync_run`, 2)
	t.Run("empty exact run succeeds", func(t *testing.T) {
		g := newWishFixture(t, false)
		_, p := g.acquire(t)
		_, e := p.StorePage(ctx, wishPage(p, 0, 0))
		wishNoError(t, e)
		wishNoError(t, p.FinishPage(ctx))
		p.Close()
		wishCount(t, g, `SELECT count(*) FROM douban_wish_sync_run WHERE status='succeeded'`, 1)
	})
}
func TestDoubanWishPageValidationAndDuplicates(t *testing.T) {
	ctx := context.Background()
	f := newWishFixture(t, false)
	_, p := f.acquire(t)
	t.Run("reject malformed DTO", func(t *testing.T) {
		mutations := []func(*ports.DoubanWishPage){func(v *ports.DoubanWishPage) { v.RequestedID = "other" }, func(v *ports.DoubanWishPage) { v.CanonicalID = "other" }, func(v *ports.DoubanWishPage) { v.WindowComplete = false }, func(v *ports.DoubanWishPage) { v.WindowEnd = 2 }, func(v *ports.DoubanWishPage) { v.Items[0].DoubanID = "01" }, func(v *ports.DoubanWishPage) { v.Items[0].Titles = []string{"\n"} }, func(v *ports.DoubanWishPage) { v.Items[0].Year = 17 }, func(v *ports.DoubanWishPage) { v.Items[0].MediaType = "tv" }, func(v *ports.DoubanWishPage) { v.MediaType = "tv" }, func(v *ports.DoubanWishPage) { v.Total = -1 }, func(v *ports.DoubanWishPage) { n := 15; v.NextStart = &n }}
		for i, mutate := range mutations {
			v := wishPage(p, 1, 1)
			mutate(&v)
			_, e := p.StorePage(ctx, v)
			if e == nil {
				t.Fatal("accepted malformed DTO", i)
			}
		}
	})
	v := wishPage(p, 1, 16)
	items, e := p.StorePage(ctx, v)
	wishNoError(t, e)
	items[0].Observation.Titles[0] = "mutated caller data"
	again, e := p.StorePage(ctx, v)
	wishNoError(t, e)
	if again[0].Observation.Titles[0] == "mutated caller data" {
		t.Fatal("source aliases caller")
	}
	bad := wishPage(p, 1, 16)
	bad.Items[0].Titles[0] = "changed upstream"
	_, e = p.StorePage(ctx, bad)
	wishError(t, e)
	wishNoError(t, p.FinishItem(ctx, items[0].ID, "incomplete"))
	wishNoError(t, p.FinishPage(ctx))
	p.Close()
	_, p2 := f.acquire(t)
	duplicate := wishPage(p2, 1, 16)
	duplicate.Items[0] = v.Items[0]
	got, e := p2.StorePage(ctx, duplicate)
	wishNoError(t, e)
	if len(got) != 0 {
		t.Fatal("duplicate belongs to original page", got)
	}
	wishNoError(t, p2.FinishPage(ctx))
	p2.Close()
	wishCount(t, f, `SELECT count(*) FROM douban_wish_sync_item`, 1)
	wishCount(t, f, `SELECT count(*) FROM douban_wish_sync_run WHERE incomplete AND status='partial'`, 1)
}
func TestDoubanWishMatchAndHistoryImmutable(t *testing.T) {
	ctx := context.Background()
	f := newWishFixture(t, false)
	_, p := f.acquire(t)
	v := wishPage(p, 2, 2)
	v.Items[1].Year = 0
	items, e := p.StorePage(ctx, v)
	wishNoError(t, e)
	matched := ports.DoubanWishMatch{State: "matched", TMDBID: 123, MediaType: "movie"}
	wishError(t, p.StoreIdentity(ctx, items[1].ID, matched))
	wishNoError(t, p.StoreIdentity(ctx, items[0].ID, matched))
	wishNoError(t, p.StoreIdentity(ctx, items[0].ID, matched))
	matched.TMDBID = 124
	wishError(t, p.StoreIdentity(ctx, items[0].ID, matched))
	wishError(t, p.FinishItem(ctx, items[0].ID, "created"))
	wishNoError(t, p.FinishItem(ctx, items[0].ID, "conflict"))
	wishError(t, p.FinishItem(ctx, items[0].ID, "unavailable"))
	for _, sql := range []string{`UPDATE douban_wish_sync_item SET source='{}'`, `UPDATE douban_wish_sync_item SET match='{}' WHERE match IS NOT NULL`, `UPDATE douban_wish_sync_item SET command_key='changed'`, `UPDATE douban_wish_sync_page SET execution_owner='other'`, `UPDATE douban_wish_sync_page SET execution_id=gen_random_uuid()`, `UPDATE douban_wish_sync_run SET config_version=2`, `DELETE FROM douban_wish_sync_item`, `TRUNCATE douban_wish_sync_item`} {
		_, e := f.p.Exec(ctx, sql)
		wishError(t, e)
	}
	wishNoError(t, p.StoreIdentity(ctx, items[1].ID, ports.DoubanWishMatch{State: "incomplete", MediaType: "movie"}))
	wishNoError(t, p.FinishPage(ctx))
	p.Close()
}
func TestDoubanWishTailRollbackAndRecovery(t *testing.T) {
	ctx := context.Background()
	t.Run("completion tail rolls next page back", func(t *testing.T) {
		f := newWishFixture(t, true)
		l, p := f.acquire(t)
		_, e := p.StorePage(ctx, wishPage(p, 0, 0))
		wishNoError(t, e)
		f.exec(t, `CREATE FUNCTION wish_slow_tail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.state='succeeded' THEN PERFORM pg_sleep(0.2); END IF; RETURN NEW; END $$; CREATE TRIGGER wish_slow_tail BEFORE UPDATE ON job FOR EACH ROW EXECUTE FUNCTION wish_slow_tail()`)
		f.exec(t, `UPDATE job SET lease_until=clock_timestamp()+interval '100 milliseconds' WHERE id=$1`, l.ID)
		wishError(t, p.FinishPage(ctx))
		wishCount(t, f, `SELECT count(*) FROM job`, 1)
		wishCount(t, f, `SELECT count(*) FROM douban_wish_sync_page WHERE finished`, 0)
		wishCount(t, f, `SELECT count(*) FROM job WHERE state='running'`, 1)
		wishNoError(t, f.store.RecoverExpired(ctx))
		wishCount(t, f, `SELECT count(*) FROM douban_wish_sync_run WHERE status='review_required'`, 1)
		wishNoError(t, f.store.EnqueueDue(ctx))
		wishCount(t, f, `SELECT count(*) FROM job`, 1)
	})
	t.Run("deferred commit expiry rolls item back", func(t *testing.T) {
		f := newWishFixture(t, false)
		l, p := f.acquire(t)
		items, e := p.StorePage(ctx, wishPage(p, 1, 1))
		wishNoError(t, e)
		f.exec(t, `CREATE FUNCTION wish_commit_delay() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(0.2); RETURN NEW; END $$; CREATE CONSTRAINT TRIGGER a_wish_commit_delay AFTER UPDATE ON douban_wish_sync_item DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION wish_commit_delay()`)
		f.exec(t, `UPDATE job SET lease_until=clock_timestamp()+interval '100 milliseconds' WHERE id=$1`, l.ID)
		wishError(t, p.FinishItem(ctx, items[0].ID, "unavailable"))
		wishCount(t, f, `SELECT count(*) FROM douban_wish_sync_item WHERE state='pending'`, 1)
	})
	t.Run("dead job review retains physical slot", func(t *testing.T) {
		f := newWishFixture(t, false)
		l, p := f.acquire(t)
		f.exec(t, `UPDATE job SET state='dead' WHERE id=$1`, l.ID)
		wishNoError(t, f.store.RecoverExpired(ctx))
		wishCount(t, f, `SELECT count(*) FROM douban_wish_sync_run WHERE status='review_required'`, 1)
		wishNoError(t, f.store.EnqueueDue(ctx))
		wishCount(t, f, `SELECT count(*) FROM job`, 1)
		wishError(t, p.Check(ctx))
		var held bool
		wishNoError(t, f.p.QueryRow(ctx, `SELECT pulse_douban_wish_permit_held(pulse_douban_wish_lock_key(r.config_id),p.execution_pid) FROM douban_wish_sync_run r JOIN douban_wish_sync_page p ON p.run_id=r.id`).Scan(&held))
		if !held {
			t.Fatal("recovery released physical slot")
		}
		_, e := f.store.Acquire(ctx, l)
		wishError(t, e)
	})
}
func TestDoubanWishMigration96(t *testing.T) {
	ctx := context.Background()
	// Keep the 96 contract independent of later migrations and runtime fixtures.
	history96 := t.TempDir()
	entries, err := os.ReadDir(doubanSyncMigrationsDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") || len(entry.Name()) < 5 || entry.Name()[:5] > "00096" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(doubanSyncMigrationsDir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(history96, entry.Name()), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	f := newDoubanSyncFixtureAt(t, history96, 96)
	v, e := postgres.Rollback(ctx, f.dsn, history96)
	if e != nil || v != 95 {
		t.Fatal(v, e)
	}
	v, e = postgres.Migrate(ctx, f.dsn, history96)
	if e != nil || v != 96 {
		t.Fatal(v, e)
	}
	in := f.input()
	f.save(t, in, "initial")
	s := postgres.NewDoubanWishSyncStore(f.p, idgen.New())
	wishNoError(t, s.EnqueueDue(ctx))
	_, e = postgres.Rollback(ctx, f.dsn, history96)
	wishError(t, e)
	var version int64
	wishNoError(t, f.p.QueryRow(ctx, `SELECT max(version_id) FROM goose_db_version WHERE is_applied`).Scan(&version))
	if version != 96 {
		t.Fatal(version)
	}
	// Check PostgreSQL's deterministic lock key against a separate Go calculation through acquisition.
	p := &wishFixture{f, s}
	_, permit := p.acquire(t)
	wishNoError(t, permit.Check(ctx))
	permit.Close()
}
