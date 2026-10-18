package postgres_test

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"sync"
	"testing"
	"time"
)

func readyMissingResolution(t *testing.T) (observationFixture, ports.ScanSeal, ports.ScanMissingFile) {
	t.Helper()
	f := newObservationFixture(t)
	first := missingSeal(f)
	finishMissing(t, f, first)
	second := nextMissingSeal(t, f)
	finishMissing(t, f, second)
	if _, err := f.pool.Exec(context.Background(), "UPDATE scan_run SET finished_at=finished_at-interval '10 minutes' WHERE id=$1", first.RunID); err != nil {
		t.Fatal(err)
	}
	rows, err := f.store.ListMissingChecks(context.Background(), second, [16]byte{})
	if err != nil || len(rows) != 1 {
		t.Fatalf("qualified: %v %v", rows, err)
	}
	return f, second, rows[0]
}
func TestMissingResolutionReceiptFailureAtomicReplay(t *testing.T) {
	f, seal, file := readyMissingResolution(t)
	ctx := context.Background()
	before := missingProtectedSnapshot(t, f)
	if _, err := f.pool.Exec(ctx, `CREATE FUNCTION fail_missing_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic receipt failure'; END $$; CREATE TRIGGER fail_missing_receipt BEFORE INSERT ON scan_missing_receipt FOR EACH ROW EXECUTE FUNCTION fail_missing_receipt();`); err != nil {
		t.Fatal(err)
	}
	if err := f.store.ResolveMissing(ctx, seal, file, ports.ScanFileMissing); err == nil {
		t.Fatal("receipt failure committed")
	}
	if got := missingProtectedSnapshot(t, f); got != before {
		t.Fatal("receipt fault left partial business state")
	}
	var receipts int
	if err := f.pool.QueryRow(ctx, "SELECT count(*) FROM scan_missing_receipt").Scan(&receipts); err != nil || receipts != 0 {
		t.Fatalf("receipts=%d %v", receipts, err)
	}
	if _, err := f.pool.Exec(ctx, "DROP TRIGGER fail_missing_receipt ON scan_missing_receipt"); err != nil {
		t.Fatal(err)
	}
	rows, err := f.store.ListMissingChecks(ctx, seal, [16]byte{})
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	file = rows[0]
	if err = f.store.ResolveMissing(ctx, seal, file, ports.ScanFileMissing); err != nil {
		t.Fatal(err)
	}
	after := missingProtectedSnapshot(t, f)
	file.ValidUntil = time.Time{}
	if err = f.store.ResolveMissing(ctx, seal, file, ports.ScanFileMissing); err != nil {
		t.Fatal("lost receipt replay", err)
	}
	if got := missingProtectedSnapshot(t, f); got != after {
		t.Fatal("receipt replay changed state")
	}
	if err = f.store.ResolveMissing(ctx, seal, file, ports.ScanFilePresent); !errors.Is(err, ports.ErrScanObservationStale) {
		t.Fatal("conflicting receipt", err)
	}
	var retired, revoked bool
	if err = f.pool.QueryRow(ctx, "SELECT retired_at IS NOT NULL FROM library_file WHERE id=$1", f.file).Scan(&retired); err != nil || !retired {
		t.Fatal(retired, err)
	}
	if err = f.pool.QueryRow(ctx, "SELECT bool_and(revoked_at IS NOT NULL) FROM unit_fact WHERE source_file_id=$1", f.file).Scan(&revoked); err != nil || !revoked {
		t.Fatal(revoked, err)
	}
	if _, err = f.pool.Exec(ctx, "UPDATE scan_missing_receipt SET decision='present'"); err == nil {
		t.Fatal("receipt mutable")
	}
}
func TestMissingResolutionEightWayAndFreshness(t *testing.T) {
	t.Run("replay", func(t *testing.T) {
		f, seal, file := readyMissingResolution(t)
		var wg sync.WaitGroup
		errs := make(chan error, 8)
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs <- f.store.ResolveMissing(context.Background(), seal, file, ports.ScanFileMissing)
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		var n int
		if err := f.pool.QueryRow(context.Background(), "SELECT count(*) FROM scan_missing_receipt").Scan(&n); err != nil || n != 1 {
			t.Fatal(n, err)
		}
	})
	t.Run("expired", func(t *testing.T) {
		f, seal, file := readyMissingResolution(t)
		before := missingProtectedSnapshot(t, f)
		file.ValidUntil = time.Now().Add(-time.Second)
		if err := f.store.ResolveMissing(context.Background(), seal, file, ports.ScanFileMissing); !errors.Is(err, ports.ErrScanObservationStale) {
			t.Fatal(err)
		}
		if after := missingProtectedSnapshot(t, f); after != before {
			t.Fatal("expired proof wrote state")
		}
	})
	t.Run("present", func(t *testing.T) {
		f, seal, file := readyMissingResolution(t)
		before := missingProtectedSnapshot(t, f)
		if err := f.store.ResolveMissing(context.Background(), seal, file, ports.ScanFilePresent); err != nil {
			t.Fatal(err)
		}
		if after := missingProtectedSnapshot(t, f); after != before {
			t.Fatal("presence revoked file or facts")
		}
	})
}

func TestMissingResolutionRechecksAfterRootWait(t *testing.T) {
	for _, kind := range []string{"offline", "token", "expired"} {
		t.Run(kind, func(t *testing.T) {
			f, seal, file := readyMissingResolution(t)
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			if kind == "expired" {
				cfg := f.pool.Config()
				prior := cfg.AfterConnect
				cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
					if err := prior(ctx, conn); err != nil {
						return err
					}
					_, err := conn.Exec(ctx, "SET lock_timeout='10s'")
					return err
				}
				consumer, err := pgxpool.NewWithConfig(ctx, cfg)
				if err != nil {
					t.Fatal(err)
				}
				defer consumer.Close()
				f.store = postgres.NewScanObservationStore(consumer)
			}
			blocker, err := f.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = blocker.Rollback(ctx) }()
			if _, err = blocker.Exec(ctx, "SELECT id FROM library_root WHERE id=$1 FOR UPDATE", f.root); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- f.store.ResolveMissing(ctx, seal, file, ports.ScanFileMissing) }()
			for {
				var waiting bool
				if err = f.pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%LockAcquisitionRoot%')").Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				select {
				case err := <-done:
					t.Fatalf("not blocked: %v", err)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-time.After(10 * time.Millisecond):
				}
			}
			switch kind {
			case "offline":
				_, err = blocker.Exec(ctx, "UPDATE library_root SET health='offline' WHERE id=$1", f.root)
			case "token":
				_, err = blocker.Exec(ctx, "UPDATE library_root SET scan_token = (scan_token + 1) WHERE id=$1", f.root)
			case "expired":
				_, err = blocker.Exec(ctx, "SELECT pg_sleep(GREATEST(0,EXTRACT(EPOCH FROM($1::timestamptz-clock_timestamp())))+0.05)", file.ValidUntil)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = blocker.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case err = <-done:
				if !errors.Is(err, ports.ErrScanObservationStale) {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			var n int
			if err = f.pool.QueryRow(ctx, "SELECT count(*) FROM scan_missing_receipt").Scan(&n); err != nil || n != 0 {
				t.Fatal(n, err)
			}
			var active bool
			if err = f.pool.QueryRow(ctx, "SELECT retired_at IS NULL FROM library_file WHERE id=$1", f.file).Scan(&active); err != nil || !active {
				t.Fatal(active, err)
			}
		})
	}
}
func TestMissingResolutionHistoryBlocksDowngrade(t *testing.T) {
	f, seal, file := readyMissingResolution(t)
	ctx := context.Background()
	before := missingProtectedSnapshot(t, f)
	if err := f.store.ResolveMissing(ctx, seal, file, ports.ScanFilePresent); err != nil {
		t.Fatal(err)
	}
	for v := int64(postgres.CurrentMigrationVersion); v > 50; {
		var err error
		v, err = postgres.Rollback(ctx, f.dsn, migrationsDir)
		if err != nil {
			t.Fatal(err)
		}
		if missingProtectedSnapshot(t, f) != before {
			t.Fatal("unrelated downgrade changed missing history")
		}
	}
	if _, err := postgres.Rollback(ctx, f.dsn, migrationsDir); err == nil {
		t.Fatal("receipt history dropped")
	}
	if err := postgres.CheckSchemaVersion(ctx, f.pool, 50); err != nil {
		t.Fatal(err)
	}
	if after := missingProtectedSnapshot(t, f); after != before {
		t.Fatal("downgrade changed history")
	}
}

func TestMissingResolutionRejectsMappingChangedDuringUnitWait(t *testing.T) {
	f, seal, file := readyMissingResolution(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	blocker, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback(ctx) }()
	if _, err = blocker.Exec(ctx, "SELECT id FROM media_unit WHERE id=$1 FOR UPDATE", f.first); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- f.store.ResolveMissing(ctx, seal, file, ports.ScanFileMissing) }()
	for {
		var waiting bool
		if err = f.pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%LockAcquisitionUnit%')").Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("not blocked: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	if _, err = blocker.Exec(ctx, "UPDATE library_file SET mapping_revision=mapping_revision+1 WHERE id=$1", f.file); err != nil {
		t.Fatal(err)
	}
	if err = blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	before := missingProtectedSnapshot(t, f)
	select {
	case err = <-done:
		if !errors.Is(err, ports.ErrScanObservationStale) {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if after := missingProtectedSnapshot(t, f); after != before {
		t.Fatal("stale mapping revoked sources")
	}
}
