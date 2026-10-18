package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"sync"
	"testing"
	"time"
)

func sealedFixture(t *testing.T) (observationFixture, ports.ScanSeal) {
	t.Helper()
	f := newObservationFixture(t)
	ctx := context.Background()
	_, err := f.pool.Exec(ctx, `INSERT INTO library_file(id,root_id,rel_path,size_bytes,mtime_ns,created_at)
 SELECT gen_random_uuid(),$1,'pending-'||i||'.mkv',1,1,(SELECT created_at FROM scan_run WHERE id=$2)-interval '1 second' FROM generate_series(1,130) i`, f.root, f.input.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.Record(ctx, f.input); err != nil {
		t.Fatal(err)
	}
	in := f.input
	seal := ports.ScanSeal{RunID: in.RunID, RootID: in.RootID, Path: in.Path, MediaType: in.MediaType, MarkerFilename: in.MarkerFilename, Generation: in.Generation, HealthRevision: in.HealthRevision, ScanToken: in.ScanToken, StartMutationSeq: in.StartMutationSeq, RootIdentity: ports.FileIdentity{Device: 1, Inode: 99}}
	if err = f.store.Seal(ctx, seal); err != nil {
		t.Fatal(err)
	}
	return f, seal
}
func finalizationSnapshot(t *testing.T, f observationFixture) string {
	t.Helper()
	var value string
	if err := f.pool.QueryRow(context.Background(), `SELECT jsonb_build_object('root',(SELECT to_jsonb(r) FROM library_root r WHERE id=$1),'run',(SELECT to_jsonb(s) FROM scan_run s WHERE id=$2),'cursor',(SELECT to_jsonb(f) FROM scan_finalization f WHERE scan_run_id=$2),'missing',(SELECT jsonb_agg(to_jsonb(c) ORDER BY file_id,first_run_id) FROM missing_candidate c WHERE root_id=$1))::text`, f.root, f.input.RunID).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}
func TestScanFinalizationCursorResumesWithoutDoubleCounting(t *testing.T) {
	f, seal := sealedFixture(t)
	ctx := context.Background()
	p, err := f.store.ReconcileBatch(ctx, seal)
	if err != nil || p.Done || p.Missing != 128 || p.Observed != 1 || p.Updated != 1 {
		t.Fatalf("first batch: %+v %v", p, err)
	}
	root, err := f.q.GetLibraryRootByID(ctx, f.root)
	if err != nil || root.BaselineCompleted {
		t.Fatal("incomplete reconciliation published baseline", err)
	}
	if _, err = f.q.FinishScanRunSuccess(ctx, db.FinishScanRunSuccessParams{ID: seal.RunID}); err == nil {
		t.Fatal("legacy completion bypassed finalizing")
	}
	beforeDowngrade := finalizationSnapshot(t, f)
	v := int64(postgres.CurrentMigrationVersion)
	for v > 49 {
		v, err = postgres.Rollback(ctx, f.dsn, migrationsDir)
		if err != nil {
			t.Fatal(err)
		}
		if finalizationSnapshot(t, f) != beforeDowngrade {
			t.Fatal("unrelated migration downgrade changed pending cursor")
		}
	}
	if _, err = postgres.Rollback(ctx, f.dsn, migrationsDir); err == nil {
		t.Fatal("downgrade discarded pending cursor")
	}
	if err = postgres.CheckSchemaVersion(ctx, f.pool, 49); err != nil {
		t.Fatal(err)
	}
	if v, err = postgres.Migrate(ctx, f.dsn, migrationsDir); err != nil || v != postgres.CurrentMigrationVersion {
		t.Fatal(v, err)
	}
	if finalizationSnapshot(t, f) != beforeDowngrade {
		t.Fatal("migration recovery changed pending cursor")
	}
	recovered := postgres.NewScanObservationStore(f.pool)
	pending, err := recovered.FindFinalizing(ctx, f.root)
	if err != nil || pending == nil || pending.RunID != seal.RunID {
		t.Fatal("cursor missing after reconstruction", err)
	}
	if err = recovered.Seal(ctx, *pending); err != nil {
		t.Fatal(err)
	}
	p, err = recovered.ReconcileBatch(ctx, *pending)
	if err != nil || !p.Done || p.Missing != 130 {
		t.Fatalf("resume: %+v %v", p, err)
	}
	before := finalizationSnapshot(t, f)
	for i := 0; i < 3; i++ {
		p, err = recovered.ReconcileBatch(ctx, *pending)
		if err != nil || !p.Done || p.Missing != 130 {
			t.Fatal(p, err)
		}
	}
	if after := finalizationSnapshot(t, f); after != before {
		t.Fatal("completed replay wrote counts or timestamps")
	}
	root, err = f.q.GetLibraryRootByID(ctx, f.root)
	if err != nil || !root.BaselineCompleted {
		t.Fatal("last batch did not publish baseline", err)
	}
	if _, err = f.store.Record(ctx, f.input); !errors.Is(err, ports.ErrScanObservationStale) {
		t.Fatal("sealed observation set was modified", err)
	}
}
func TestScanFinalizationFaultRollsBackCursorAndBaseline(t *testing.T) {
	for _, table := range []string{"scan_run", "scan_finalization", "library_root"} {
		t.Run(table, func(t *testing.T) {
			f, seal := sealedFixture(t)
			ctx := context.Background()
			if table == "library_root" {
				if _, err := f.store.ReconcileBatch(ctx, seal); err != nil {
					t.Fatal(err)
				}
			}
			before := finalizationSnapshot(t, f)
			if _, err := f.pool.Exec(ctx, `CREATE FUNCTION reject_finalize() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic finalization fault'; END $$`); err != nil {
				t.Fatal(err)
			}
			if _, err := f.pool.Exec(ctx, fmt.Sprintf("CREATE TRIGGER reject_finalize BEFORE UPDATE ON %s FOR EACH ROW EXECUTE FUNCTION reject_finalize()", table)); err != nil {
				t.Fatal(err)
			}
			if _, err := f.store.ReconcileBatch(ctx, seal); err == nil {
				t.Fatal("fault accepted")
			}
			if after := finalizationSnapshot(t, f); after != before {
				t.Fatal("fault left partial count/cursor/baseline")
			}
			if _, err := f.pool.Exec(ctx, "DROP TRIGGER reject_finalize ON "+table); err != nil {
				t.Fatal(err)
			}
			for {
				p, err := f.store.ReconcileBatch(ctx, seal)
				if err != nil {
					t.Fatal(err)
				}
				if p.Done {
					if p.Missing != 130 {
						t.Fatal("bad recovery count", p)
					}
					break
				}
			}
		})
	}
}
func TestScanFinalizationEightWayAndInvalidStamp(t *testing.T) {
	f, seal := sealedFixture(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := f.store.ReconcileBatch(ctx, seal); errs <- err }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	before := finalizationSnapshot(t, f)
	for _, kind := range []string{"health", "inode", "device", "token", "generation", "watermark", "path"} {
		bad := seal
		switch kind {
		case "health":
			bad.HealthRevision++
		case "inode":
			bad.RootIdentity.Inode++
		case "device":
			bad.RootIdentity.Device++
		case "token":
			bad.ScanToken++
		case "generation":
			bad.Generation++
		case "watermark":
			bad.StartMutationSeq++
		case "path":
			bad.Path += "-replacement"
		}
		if _, err := f.store.ReconcileBatch(ctx, bad); !errors.Is(err, ports.ErrScanObservationStale) {
			t.Fatalf("%s accepted: %v", kind, err)
		}
	}
	if after := finalizationSnapshot(t, f); after != before {
		t.Fatal("invalid stamp changed completed run")
	}
	p, err := f.store.ReconcileBatch(ctx, seal)
	if err != nil || p.Missing != 130 || !p.Done {
		t.Fatal(p, err)
	}
}

func TestScanFinalizationProtectsPostWatermarkAndSealedObservations(t *testing.T) {
	f, seal := sealedFixture(t)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, "UPDATE library_file SET last_mutation_seq=$2 WHERE root_id=$1 AND rel_path='pending-1.mkv'", f.root, seal.StartMutationSeq+1); err != nil {
		t.Fatal(err)
	}
	if _, err := f.q.CreateLibraryFile(ctx, db.CreateLibraryFileParams{ID: uuid.New(), RootID: f.root, RelPath: "late.mkv", SizeBytes: 1, MtimeNs: 1}); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{"DELETE FROM scan_observation WHERE id=$1", "UPDATE scan_observation SET size_bytes=size_bytes+1 WHERE id=$1"} {
		if _, err := f.pool.Exec(ctx, sql, f.input.ObservationID); err == nil {
			t.Fatal("sealed observation changed")
		}
	}
	var p ports.ScanFinalizationProgress
	for {
		var err error
		p, err = f.store.ReconcileBatch(ctx, seal)
		if err != nil {
			t.Fatal(err)
		}
		if p.Done {
			break
		}
	}
	if p.Missing != 129 {
		t.Fatal("post-watermark or late file counted absent", p)
	}
	var active int
	if err := f.pool.QueryRow(ctx, "SELECT count(*) FROM library_file WHERE root_id=$1 AND retired_at IS NULL", f.root).Scan(&active); err != nil || active != 132 {
		t.Fatal("reconciliation retired unseen/protected files", active, err)
	}
}
func TestScanFinalizationRechecksAfterRootWait(t *testing.T) {
	for _, kind := range []string{"health", "generation", "token"} {
		t.Run(kind, func(t *testing.T) {
			f, seal := sealedFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			blocker, err := f.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = blocker.Rollback(ctx) }()
			sql := "UPDATE library_root SET scan_token=scan_token+1 WHERE id=$1"
			if kind == "health" {
				sql = "UPDATE library_root SET health='offline' WHERE id=$1"
			}
			if kind == "generation" {
				sql = "UPDATE library_root SET generation=generation+1 WHERE id=$1"
			}
			if _, err = blocker.Exec(ctx, sql, f.root); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { _, e := f.store.ReconcileBatch(ctx, seal); done <- e }()
			for {
				var waiting bool
				if err = f.pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%LockAcquisitionRoot%')").Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				select {
				case e := <-done:
					t.Fatalf("batch did not wait: %v", e)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-time.After(10 * time.Millisecond):
				}
			}
			if err = blocker.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			before := finalizationSnapshot(t, f)
			select {
			case e := <-done:
				if !errors.Is(e, ports.ErrScanObservationStale) {
					t.Fatal("stale batch accepted", e)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if err = f.store.FailFinalization(ctx, seal); !errors.Is(err, ports.ErrScanObservationStale) {
				t.Fatal("stale owner changed root health", err)
			}
			if after := finalizationSnapshot(t, f); after != before {
				t.Fatal("stale batch/abort changed cursor or root")
			}
		})
	}
}
