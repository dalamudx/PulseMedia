package library_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestScanCompletionRechecksAuthorityAfterRootWait(t *testing.T) {
	for _, change := range []string{"health='offline'", "generation=generation+1", "scan_token=scan_token+1"} {
		t.Run(change, func(t *testing.T) {
			_, q, dsn := setupScannerTest(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			pool, err := postgres.NewPool(ctx, dsn, postgres.DefaultPoolConfig())
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			root, err := q.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: uuid.New(), Path: t.TempDir(), MediaType: "movie", Name: "Movies"})
			if err != nil {
				t.Fatal(err)
			}
			run, err := q.StartScanRun(ctx, db.StartScanRunParams{ID: root.ID, ID_2: uuid.New()})
			if err != nil {
				t.Fatal(err)
			}
			var before string
			if err = pool.QueryRow(ctx, "SELECT to_jsonb(s)::text FROM scan_run s WHERE id=$1", run.ID).Scan(&before); err != nil {
				t.Fatal(err)
			}
			blocker, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = blocker.Rollback(context.Background()) }()
			if _, err = blocker.Exec(ctx, "UPDATE library_root SET "+change+" WHERE id=$1", root.ID); err != nil {
				t.Fatal(err)
			}
			worker, err := pool.Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer worker.Release()
			pid := int32(worker.Conn().PgConn().PID())
			done := make(chan error, 1)
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				_, e := db.New(worker).FinishScanRunSuccess(ctx, db.FinishScanRunSuccessParams{ID: run.ID})
				done <- e
			}()
			defer func() {
				_ = blocker.Rollback(context.Background())
				cancel()
				<-finished
			}()
			deadline := time.Now().Add(time.Second)
			for {
				var waiting bool
				if err = blocker.QueryRow(ctx, "SELECT cardinality(pg_blocking_pids($1))>0", pid).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("completion did not wait for locked root")
				}
				time.Sleep(5 * time.Millisecond)
			}
			if err = blocker.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err = <-done; !errors.Is(err, pgx.ErrNoRows) {
				t.Fatalf("stale scan succeeded after root authority changed: %v", err)
			}
			var after string
			if err = pool.QueryRow(ctx, "SELECT to_jsonb(s)::text FROM scan_run s WHERE id=$1", run.ID).Scan(&after); err != nil || before != after {
				t.Fatalf("rejected scan changed: %v", err)
			}
			row, err := q.GetLibraryRootByID(ctx, root.ID)
			if err != nil || row.BaselineCompleted {
				t.Fatalf("invalidated root acquired baseline: %v", err)
			}
		})
	}
}

func TestFailedScanReplayCannotRewriteSuccessfulRun(t *testing.T) {
	_, q, dsn := setupScannerTest(t)
	ctx := context.Background()
	pool, err := postgres.NewPool(ctx, dsn, postgres.DefaultPoolConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	root, err := q.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: uuid.New(), Path: t.TempDir(), MediaType: "movie", Name: "Movies"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := q.StartScanRun(ctx, db.StartScanRunParams{ID: root.ID, ID_2: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = q.FinishScanRunSuccess(ctx, db.FinishScanRunSuccessParams{ID: run.ID, FilesObserved: 1}); err != nil {
		t.Fatal(err)
	}
	var before, after string
	if err = pool.QueryRow(ctx, "SELECT to_jsonb(s)::text FROM scan_run s WHERE id=$1", run.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err = q.FinishScanRunFailed(ctx, db.FinishScanRunFailedParams{ID: run.ID, State: "partial", Error: pgtype.Text{String: "late cancelled writer", Valid: true}}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("late failure rewrote terminal scan: %v", err)
	}
	if err = pool.QueryRow(ctx, "SELECT to_jsonb(s)::text FROM scan_run s WHERE id=$1", run.ID).Scan(&after); err != nil || before != after {
		t.Fatalf("terminal record changed: %v", err)
	}
}

func TestScanCompletionFailureRollsBackAndCanRecover(t *testing.T) {
	_, q, dsn := setupScannerTest(t)
	ctx := context.Background()
	pool, err := postgres.NewPool(ctx, dsn, postgres.DefaultPoolConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	root, err := q.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: uuid.New(), Path: t.TempDir(), MediaType: "movie", Name: "Movies"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := q.StartScanRun(ctx, db.StartScanRunParams{ID: root.ID, ID_2: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	var before, after string
	snapshot := "SELECT jsonb_build_object('run',to_jsonb(s),'root',to_jsonb(r))::text FROM scan_run s JOIN library_root r ON r.id=s.root_id WHERE s.id=$1"
	if err = pool.QueryRow(ctx, snapshot, run.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `CREATE FUNCTION reject_scan_baseline() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic baseline write failure'; END $$; CREATE TRIGGER reject_scan_baseline BEFORE UPDATE OF baseline_completed ON library_root FOR EACH ROW EXECUTE FUNCTION reject_scan_baseline()`); err != nil {
		t.Fatal(err)
	}
	if _, err = q.FinishScanRunSuccess(ctx, db.FinishScanRunSuccessParams{ID: run.ID, FilesObserved: 7, FilesAdded: 3}); err == nil {
		t.Fatal("baseline fault did not fail scan completion")
	}
	if err = pool.QueryRow(ctx, snapshot, run.ID).Scan(&after); err != nil || before != after {
		t.Fatalf("completion fault left partial publication: %v", err)
	}
	if _, err = pool.Exec(ctx, "DROP TRIGGER reject_scan_baseline ON library_root; DROP FUNCTION reject_scan_baseline()"); err != nil {
		t.Fatal(err)
	}
	row, err := q.FinishScanRunSuccess(ctx, db.FinishScanRunSuccessParams{ID: run.ID, FilesObserved: 7, FilesAdded: 3})
	if err != nil || !row.BaselineCompleted {
		t.Fatalf("valid completion after rollback failed: %v", err)
	}
	var state string
	var observed, added int64
	if err = pool.QueryRow(ctx, "SELECT state,files_observed,files_added FROM scan_run WHERE id=$1", run.ID).Scan(&state, &observed, &added); err != nil || state != "succeeded" || observed != 7 || added != 3 {
		t.Fatalf("successful publication lost counters: %v", err)
	}
	if _, err = q.FinishScanRunSuccess(ctx, db.FinishScanRunSuccessParams{ID: run.ID, FilesObserved: 100}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("successful completion replay accepted: %v", err)
	}
}

func TestScanFailureOnlyTerminatesRunningAsFailure(t *testing.T) {
	_, q, dsn := setupScannerTest(t)
	ctx := context.Background()
	pool, err := postgres.NewPool(ctx, dsn, postgres.DefaultPoolConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	root, err := q.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: uuid.New(), Path: t.TempDir(), MediaType: "movie", Name: "Movies"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := q.StartScanRun(ctx, db.StartScanRunParams{ID: root.ID, ID_2: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	var before, after string
	if err = pool.QueryRow(ctx, "SELECT to_jsonb(s)::text FROM scan_run s WHERE id=$1", run.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"running", "succeeded"} {
		if _, err = q.FinishScanRunFailed(ctx, db.FinishScanRunFailedParams{ID: run.ID, State: state}); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("failure writer accepted state %s: %v", state, err)
		}
	}
	if err = pool.QueryRow(ctx, "SELECT to_jsonb(s)::text FROM scan_run s WHERE id=$1", run.ID).Scan(&after); err != nil || before != after {
		t.Fatalf("invalid failure changed running record: %v", err)
	}
	if _, err = q.FinishScanRunFailed(ctx, db.FinishScanRunFailedParams{ID: run.ID, State: "partial"}); err != nil {
		t.Fatal(err)
	}
	if _, err = q.FinishScanRunFailed(ctx, db.FinishScanRunFailedParams{ID: run.ID, State: "failed"}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("terminal failure replay accepted: %v", err)
	}
	row, err := q.GetLibraryRootByID(ctx, root.ID)
	if err != nil || row.BaselineCompleted {
		t.Fatalf("failure unlocked baseline: %v", err)
	}
}
