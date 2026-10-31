package postgres_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	usecaseSearch "github.com/dalamudx/PulseMedia/internal/usecase/search"
	"github.com/google/uuid"
)

func movieDiscoveryFixture(t *testing.T) (*frozenUpgradeFixture, *postgres.MovieDiscoveryStore) {
	t.Helper()
	f := frozenUpgrade(t)
	f.enable(t)
	if v, e := postgres.Migrate(t.Context(), f.dsn, repositoryMigrationsThrough(t, 134)); e != nil || v != 134 {
		t.Fatalf("migrate134: %d %v", v, e)
	}
	f.project(t)
	return f, postgres.NewMovieDiscoveryStore(f.p, idgen.New())
}
func discoveryState(t *testing.T, f *frozenUpgradeFixture) string {
	t.Helper()
	var out string
	if e := f.p.QueryRow(t.Context(), `SELECT jsonb_build_array((SELECT count(*) FROM download_task),(SELECT count(*) FROM unit_reservation),(SELECT count(*) FROM acquisition_reservation),(SELECT count(*) FROM download_requirement),(SELECT count(*) FROM search_round))::text`).Scan(&out); e != nil {
		t.Fatal(e)
	}
	return out + f.oldState(t)
}
func TestMovieDiscoveryCadenceRestartPG(t *testing.T) {
	f, s := movieDiscoveryFixture(t)
	ctx := t.Context()
	before := discoveryState(t, f)
	const n = 8
	var wg sync.WaitGroup
	results := make(chan bool, n)
	errs := make(chan error, n)
	for range n {
		wg.Add(1)
		go func() { defer wg.Done(); ok, e := s.ScheduleMovieDiscovery(ctx, f.sub); results <- ok; errs <- e }()
	}
	wg.Wait()
	close(results)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	count := 0
	for ok := range results {
		if ok {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("created %d searches", count)
	}
	status, e := s.ReadMovieDiscovery(ctx, f.actor, f.sub)
	if e != nil || status.SearchID == nil || status.CurrentFileID == nil || *status.CurrentFileID != f.file || status.ExecutionAllowed {
		t.Fatalf("status %+v %v", status, e)
	}
	if wait := time.Until(*status.NextDiscoveryAt); wait < 5*time.Hour || wait > 6*time.Hour {
		t.Fatalf("cadence %s", wait)
	}
	restarted := postgres.NewMovieDiscoveryStore(f.p, idgen.New())
	svc := usecaseSearch.NewMovieDiscoveryService(restarted)
	if n, e := svc.RunBatch(ctx, 20); e != nil || n != 0 {
		t.Fatalf("restart %d %v", n, e)
	}
	var jobs int
	if e = f.p.QueryRow(ctx, `SELECT count(*) FROM job WHERE kind='interactive_search_source'`).Scan(&jobs); e != nil || jobs != 1 {
		t.Fatalf("jobs=%d %v", jobs, e)
	}
	if after := discoveryState(t, f); after != before {
		t.Fatal("discovery created acquisition authority or changed current file")
	}
	// Database cadence expiry, not a process-local timer, admits exactly one next run.
	f.exec(t, `UPDATE subscription_movie_discovery SET next_discovery_at=clock_timestamp()-interval '1 second' WHERE subscription_id=$1`, f.sub)
	if n, e := svc.RunBatch(ctx, 20); e != nil || n != 1 {
		t.Fatalf("due batch %d %v", n, e)
	}
	next, e := s.ReadMovieDiscovery(ctx, f.actor, f.sub)
	if e != nil || next.SearchID == nil || *next.SearchID == *status.SearchID {
		t.Fatalf("new run %+v %v", next, e)
	}
	if _, e = f.p.Exec(ctx, `DELETE FROM subscription_movie_discovery_run WHERE search_id=$1`, *status.SearchID); e == nil {
		t.Fatal("run history deleted")
	}
	if after := discoveryState(t, f); after != before {
		t.Fatal("periodic repeat created acquisition authority")
	}
}

func TestMovieDiscoveryCurrentInputsPG(t *testing.T) {
	for _, name := range []string{"intent", "policy", "root", "permission", "mode", "file"} {
		t.Run(name, func(t *testing.T) {
			f, s := movieDiscoveryFixture(t)
			ctx := t.Context()
			if ok, e := s.ScheduleMovieDiscovery(ctx, f.sub); e != nil || !ok {
				t.Fatalf("schedule %v %v", ok, e)
			}
			status, e := s.ReadMovieDiscovery(ctx, f.actor, f.sub)
			if e != nil || status.SearchID == nil {
				t.Fatal(status, e)
			}
			id := *status.SearchID
			switch name {
			case "intent":
				f.exec(t, `UPDATE subscription SET intent_version=intent_version+1 WHERE id=$1`, f.sub)
			case "policy":
				f.exec(t, `UPDATE policy SET current_revision=current_revision+1 WHERE id='frozen-upgrade'`)
			case "root":
				f.exec(t, `UPDATE library_root SET health='offline' WHERE id=$1`, f.root)
			case "permission":
				f.exec(t, `UPDATE app_user SET is_active=false WHERE id=$1`, uuid.MustParse(f.actor.ID))
			case "mode":
				f.exec(t, `UPDATE subscription SET upgrade_mode='disabled' WHERE id=$1`, f.sub)
			case "file":
				f.exec(t, `UPDATE library_file SET mapping_revision=mapping_revision+1 WHERE id=$1`, f.file)
			}
			tx, e := f.p.Begin(ctx)
			if e != nil {
				t.Fatal(e)
			}
			defer rollbackFrozenUpgradeTest(t, tx)
			if e = postgres.AuthorizeMovieDiscoverySearch(ctx, db.New(tx), id); e == nil {
				t.Fatal("stale search authorized")
			}
			_ = tx.Rollback(ctx)
			searches := postgres.NewInteractiveSearchStore(f.p, idgen.New())
			if _, e = searches.ReadInteractiveSearch(ctx, f.actor, id); e == nil {
				t.Fatal("stale candidates exposed")
			}
			task := interactiveClaim(t, f.p, &ports.InteractiveSearchHeader{ID: id}, f.source)
			if _, e = searches.LoadInteractiveSearchSource(ctx, task); e == nil {
				t.Fatal("stale job admitted")
			}
		})
	}
}

func TestMovieDiscoveryPermissionWaitPG(t *testing.T) {
	f, s := movieDiscoveryFixture(t)
	ctx := t.Context()
	tx, e := f.p.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer rollbackFrozenUpgradeTest(t, tx)
	if _, e = tx.Exec(ctx, `UPDATE app_user SET is_active=false WHERE id=$1`, f.actor.ID); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { _, e := s.ScheduleMovieDiscovery(ctx, f.sub); done <- e }()
	select {
	case e := <-done:
		t.Fatalf("ignored account lock %v", e)
	case <-time.After(50 * time.Millisecond):
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	select {
	case e := <-done:
		if e != nil && !errors.Is(e, ports.ErrForbidden) {
			t.Fatal(e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("schedule stuck")
	}
	var n int
	if e = f.p.QueryRow(ctx, `SELECT count(*) FROM interactive_search`).Scan(&n); e != nil || n != 0 {
		t.Fatalf("revoked search count=%d %v", n, e)
	}
}

var _ = context.Canceled
