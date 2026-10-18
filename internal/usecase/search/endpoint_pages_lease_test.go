package search_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/store/postgres"
)

func schema54Migrations(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	paths, err := filepath.Glob(filepath.Join(migrationsDir, "*.sql"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		name := filepath.Base(path)
		if name[:5] > "00054" {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func rollbackToSchema54(t *testing.T, f *roundFixture) {
	t.Helper()
	ctx := context.Background()
	dsn := f.pool.Config().ConnConfig.ConnString()
	for expected := int64(postgres.CurrentMigrationVersion - 1); expected >= 54; expected-- {
		if version, err := postgres.Rollback(ctx, dsn, migrationsDir); err != nil || version != expected {
			t.Fatalf("rollback to schema54 at %d: version=%d err=%v", expected, version, err)
		}
	}
}

func TestPersistentEndpointPageFinalYieldLeaseExpires(t *testing.T) {
	for _, version := range []string{"current", "schema54"} {
		t.Run(version, func(t *testing.T) {
			migrationDir := migrationsDir
			if version == "schema54" {
				migrationDir = schema54Migrations(t)
			}
			f := runtimeFixture(t)
			ctx := context.Background()
			f.svc = freshPageService(f, &persistentPageClient{})
			if _, e := f.coord.StartScheduledRound(ctx, f.wanted, time.Minute); e != nil {
				t.Fatal(e)
			}
			job, e := f.queue.Claim(ctx, []string{"search_endpoint"}, "final-yield-owner", time.Minute)
			if e != nil || job == nil {
				t.Fatal(e)
			}
			repo := postgres.NewSearchEndpointStore(f.pool)
			scope, e := repo.Load(ctx, endpointTask(t, job))
			if e != nil {
				t.Fatal(e)
			}
			result, e := f.svc.ExecuteSearch(ctx, f.idx, "Fight Club 1999", 0)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = f.pool.Exec(ctx, "CREATE FUNCTION page_yield_delay() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.state='queued' THEN PERFORM pg_sleep(0.3); END IF; RETURN NEW; END $$; CREATE TRIGGER page_yield_delay BEFORE UPDATE ON job FOR EACH ROW EXECUTE FUNCTION page_yield_delay()"); e != nil {
				t.Fatal(e)
			}
			if _, e = f.pool.Exec(ctx, "UPDATE job SET lease_until=clock_timestamp()+interval '180 milliseconds' WHERE id=$1", job.ID); e != nil {
				t.Fatal(e)
			}
			before := endpointState(t, f, scope)
			if e = repo.Complete(ctx, scope, result.QueryID); e == nil {
				t.Fatal("post-yield expiration accepted")
			}
			var saved int
			if e = f.pool.QueryRow(ctx, "SELECT count(*) FROM search_round_endpoint_page").Scan(&saved); e != nil || saved != 0 || endpointState(t, f, scope) != before {
				t.Fatal("expiration changed page/job", saved, e)
			}
			if _, e = f.pool.Exec(ctx, "DROP TRIGGER page_yield_delay ON job"); e != nil {
				t.Fatal(e)
			}
			if _, e = f.pool.Exec(ctx, "UPDATE job SET lease_until=clock_timestamp()+interval '1 minute',lease_token=lease_token+1 WHERE id=$1", job.ID); e != nil {
				t.Fatal(e)
			}
			scope.Task.Lease.Token++
			if e = repo.Complete(ctx, scope, result.QueryID); e != nil {
				t.Fatal(e)
			}
			if version == "schema54" {
				rollbackToSchema54(t, f)
				if _, e = postgres.Rollback(ctx, f.pool.Config().ConnConfig.ConnString(), migrationDir); e == nil {
					t.Fatal("page history permitted schema54 rollback")
				}
			}
			if _, e = f.pool.Exec(ctx, "UPDATE search_round_endpoint_page SET source_token=source_token+1"); e == nil {
				t.Fatal("page history changed")
			}
			if _, e = f.pool.Exec(ctx, "DELETE FROM search_round_endpoint_page"); e == nil {
				t.Fatal("page history deleted")
			}
		})
	}
}

func TestPersistentEndpointPageEightWayYield(t *testing.T) {
	f := runtimeFixture(t)
	ctx := context.Background()
	f.svc = freshPageService(f, &persistentPageClient{})
	if _, e := f.coord.StartScheduledRound(ctx, f.wanted, time.Minute); e != nil {
		t.Fatal(e)
	}
	job, e := f.queue.Claim(ctx, []string{"search_endpoint"}, "yield-race-owner", time.Minute)
	if e != nil || job == nil {
		t.Fatal(e)
	}
	repo := postgres.NewSearchEndpointStore(f.pool)
	scope, e := repo.Load(ctx, endpointTask(t, job))
	if e != nil {
		t.Fatal(e)
	}
	result, e := f.svc.ExecuteSearch(ctx, f.idx, "Fight Club 1999", 0)
	if e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 8)
	for range 8 {
		go func() { done <- repo.Complete(ctx, scope, result.QueryID) }()
	}
	success := 0
	for range 8 {
		if <-done == nil {
			success++
		}
	}
	var count, attempts int
	var state string
	if e = f.pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM search_round_endpoint_page),j.attempts,j.state FROM job j WHERE j.id=$1", job.ID).Scan(&count, &attempts, &state); e != nil || success != 1 || count != 1 || attempts != 0 || state != "queued" {
		t.Fatal(success, count, attempts, state, e)
	}
}

func TestPersistentEndpointPageEmptyMigrationPreservesLegacy(t *testing.T) {
	migrationDir := schema54Migrations(t)
	f := runtimeFixture(t)
	ctx := context.Background()
	rollbackToSchema54(t, f)
	var before string
	if e := f.pool.QueryRow(ctx, "SELECT to_jsonb(i)::text FROM indexer i WHERE id=$1", f.idx).Scan(&before); e != nil {
		t.Fatal(e)
	}
	if version, e := postgres.Rollback(ctx, f.pool.Config().ConnConfig.ConnString(), migrationDir); e != nil || version != 53 {
		t.Fatal(version, e)
	}
	if _, e := postgres.Migrate(ctx, f.pool.Config().ConnConfig.ConnString(), migrationDir); e != nil {
		t.Fatal(e)
	}
	var after string
	var count int
	if e := f.pool.QueryRow(ctx, "SELECT to_jsonb(i)::text,(SELECT count(*) FROM search_round_endpoint_page) FROM indexer i WHERE id=$1", f.idx).Scan(&after, &count); e != nil || after != before || count != 0 {
		t.Fatal("legacy changed/backfilled", count, e)
	}
}
