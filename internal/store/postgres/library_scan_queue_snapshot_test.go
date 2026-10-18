package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// An RLS test policy pauses the native SELECT before it locks the root. The
// winning scheduler commits after that SELECT's snapshot, reproducing the
// gap without changing production SQL, lock budgets, or isolation levels.
func TestLibraryScanQueueRechecksSnapshotAfterConcurrentCommit(t *testing.T) {
	for _, kind := range []string{"old_statement_negative_control", "queued", "dead", "succeeded", "future"} {
		legacy := kind == "old_statement_negative_control"
		wantQueued := 0
		if kind == "succeeded" {
			wantQueued = 1
		}
		t.Run(kind, func(t *testing.T) {
			f := newObservationFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := f.q.InitializeLibraryScanSchedules(ctx); err != nil {
				t.Fatal(err)
			}
			role := "pulse_schedule_" + fmt.Sprintf("%x", [16]byte(uuid.New()))
			// Role and policy live only in this task's test database/cluster.
			ddl := fmt.Sprintf(`CREATE ROLE %s;
 GRANT USAGE ON SCHEMA public TO %s;
 GRANT SELECT,INSERT,UPDATE ON ALL TABLES IN SCHEMA public TO %s;
 CREATE FUNCTION fixture_schedule_snapshot_gate() RETURNS boolean LANGUAGE plpgsql VOLATILE COST 1 AS $$
 BEGIN
 IF current_setting('pulse.scan_queue_pause',true)='on' AND current_query() LIKE '%%SELECT r.id FROM library_root r JOIN library_scan_schedule%%' THEN
 PERFORM pg_advisory_xact_lock(485736225999::bigint);
 END IF;
 RETURN true;
 END $$;
 ALTER TABLE library_scan_schedule ENABLE ROW LEVEL SECURITY;
 CREATE POLICY fixture_schedule_select ON library_scan_schedule FOR SELECT TO %s USING (fixture_schedule_snapshot_gate());
 CREATE POLICY fixture_schedule_insert ON library_scan_schedule FOR INSERT TO %s WITH CHECK (true);
 CREATE POLICY fixture_schedule_update ON library_scan_schedule FOR UPDATE TO %s USING (true) WITH CHECK (true);`, role, role, role, role, role, role)
			if _, err := f.pool.Exec(ctx, ddl); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_, err := f.pool.Exec(context.Background(), fmt.Sprintf(`DROP POLICY fixture_schedule_select ON library_scan_schedule;
 DROP POLICY fixture_schedule_insert ON library_scan_schedule;
 DROP POLICY fixture_schedule_update ON library_scan_schedule;
 DROP FUNCTION fixture_schedule_snapshot_gate(); DROP OWNED BY %s; DROP ROLE %s;`, role, role))
				if err != nil {
					t.Error(err)
				}
			})
			cfg, err := pgxpool.ParseConfig(f.dsn)
			if err != nil {
				t.Fatal(err)
			}
			cfg.ConnConfig.RuntimeParams["role"] = role
			cfg.ConnConfig.RuntimeParams["pulse.scan_queue_pause"] = "on"
			cfg.ConnConfig.RuntimeParams["statement_timeout"] = "10000"
			cfg.MaxConns = 1
			paused, err := pgxpool.NewWithConfig(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer paused.Close()
			blocker, err := f.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			// Cancellation/early assertion failure can leave this test-owned lock held.
			defer func() { _ = blocker.Rollback(context.Background()) }()
			if _, err = blocker.Exec(ctx, "SELECT pg_advisory_xact_lock(485736225999::bigint)"); err != nil {
				t.Fatal(err)
			}
			type outcome struct {
				n   int
				err error
			}
			result := make(chan outcome, 1)
			go func() {
				if !legacy {
					n, err := postgres.NewLibraryScanRuntimeStore(paused, idgen.New()).QueueDue(ctx, 20)
					result <- outcome{n, err}
					return
				}
				tx, err := paused.Begin(ctx)
				if err != nil {
					result <- outcome{err: err}
					return
				}
				defer func() { _ = tx.Rollback(context.Background()) }()
				q := db.New(tx)
				roots, err := q.LockDueLibraryScanRoots(ctx, 20)
				if err == nil {
					for _, root := range roots {
						err = q.InsertLibraryScanJob(ctx, db.InsertLibraryScanJobParams{JobID: uuid.New(), RootID: root})
						if err != nil {
							break
						}
					}
				}
				result <- outcome{len(roots), err}
			}()
			deadline := time.Now().Add(5 * time.Second)
			for {
				select {
				case early := <-result:
					t.Fatalf("scheduling SELECT ended before barrier: n=%d err=%v", early.n, early.err)
				default:
				}
				var waiting bool
				err = f.pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event='advisory' AND query LIKE '%SELECT r.id FROM library_root r JOIN library_scan_schedule%' AND pid<>pg_backend_pid())").Scan(&waiting)
				if err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("native scheduling SELECT did not reach snapshot barrier")
				}
				time.Sleep(10 * time.Millisecond)
			}
			winner := postgres.NewLibraryScanRuntimeStore(f.pool, idgen.New())
			if n, err := winner.QueueDue(ctx, 20); err != nil || n != 1 {
				t.Fatal(n, err)
			}
			switch kind {
			case "dead", "succeeded", "future":
				state := kind
				if kind == "future" {
					state = "succeeded"
				}
				if _, err := f.pool.Exec(ctx, "UPDATE job SET state=$2 WHERE kind='library_scan' AND payload->>'root_id'=$1", f.root.String(), state); err != nil {
					t.Fatal(err)
				}
				if kind == "future" {
					if _, err := f.pool.Exec(ctx, "UPDATE library_scan_schedule SET next_run_at=clock_timestamp()+interval '1 hour' WHERE root_id=$1", f.root); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err = blocker.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			var got outcome
			select {
			case got = <-result:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if legacy {
				var pgErr *pgconn.PgError
				if !errors.As(got.err, &pgErr) || pgErr.Code != "23505" || pgErr.ConstraintName != "job_library_root_lane" {
					t.Fatalf("old snapshot did not reproduce lane conflict: n=%d err=%v", got.n, got.err)
				}
			} else if got.err != nil || got.n != wantQueued {
				t.Fatalf("native scheduler did not skip committed work: n=%d err=%v", got.n, got.err)
			}
			var jobs, bindings, schedules int
			if err = f.pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM job WHERE kind='library_scan'),(SELECT count(*) FROM library_scan_job),(SELECT count(*) FROM library_scan_schedule s JOIN job j ON j.id=s.last_scan_job_id)").Scan(&jobs, &bindings, &schedules); err != nil {
				t.Fatal(err)
			}
			if jobs != 1+wantQueued || bindings != 1+wantQueued || schedules != 1 {
				t.Fatal(jobs, bindings, schedules)
			}
			if n, err := winner.QueueDue(ctx, 20); err != nil || n != 0 {
				t.Fatal(n, err)
			}
		})
	}
}
