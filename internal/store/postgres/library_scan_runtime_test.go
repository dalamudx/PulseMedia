package postgres_test

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/jobs"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"sync"
	"testing"
	"time"
)

func claimScanWork(t *testing.T, f observationFixture) (*postgres.ScanObservationStore, ports.LibraryScanWork) {
	t.Helper()
	ctx := context.Background()
	gen := idgen.New()
	runtime := postgres.NewLibraryScanRuntimeStore(f.pool, gen)
	if n, err := runtime.QueueDue(ctx, 20); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	job, err := jobs.NewQueue(f.pool, gen, clock.New()).Claim(ctx, []string{"library_scan"}, "scan-owner", 5*time.Minute)
	if err != nil || job == nil {
		t.Fatal(job, err)
	}
	if n, err := f.q.BindLibraryScanRun(ctx, db.BindLibraryScanRunParams{JobID: job.ID, RunID: f.input.RunID}); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	w, err := runtime.Work(ctx, ports.JobLease{ID: job.ID, Owner: "scan-owner", Token: job.LeaseToken})
	if err != nil {
		t.Fatal(err)
	}
	return postgres.NewLeasedScanObservationStore(f.pool, gen, w), w
}
func TestLibraryScanQueueEightWayAndFinalWriteRollback(t *testing.T) {
	for _, fault := range []bool{false, true} {
		t.Run(map[bool]string{false: "eight_way", true: "rollback"}[fault], func(t *testing.T) {
			f := newObservationFixture(t)
			ctx := context.Background()
			runtime := postgres.NewLibraryScanRuntimeStore(f.pool, idgen.New())
			if fault {
				if _, err := f.pool.Exec(ctx, `CREATE FUNCTION fail_library_schedule() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture'; END $$; CREATE TRIGGER fail_library_schedule BEFORE UPDATE ON library_scan_schedule FOR EACH ROW EXECUTE FUNCTION fail_library_schedule()`); err != nil {
					t.Fatal(err)
				}
			}
			var wg sync.WaitGroup
			errs := make(chan error, 8)
			for i := 0; i < 8; i++ {
				wg.Add(1)
				go func() { defer wg.Done(); _, err := runtime.QueueDue(ctx, 20); errs <- err }()
			}
			wg.Wait()
			close(errs)
			failures := 0
			for err := range errs {
				if err != nil {
					failures++
				}
				if !fault && err != nil {
					t.Fatal(err)
				}
			}
			if fault && failures == 0 {
				t.Fatal("fault trigger was not exercised")
			}
			var n int
			if err := f.pool.QueryRow(ctx, "SELECT count(*) FROM job WHERE kind='library_scan'").Scan(&n); err != nil {
				t.Fatal(err)
			}
			want := 1
			if fault {
				want = 0
			}
			if n != want {
				t.Fatal(n, want)
			}
			if fault {
				if _, err := f.pool.Exec(ctx, "DROP TRIGGER fail_library_schedule ON library_scan_schedule;DROP FUNCTION fail_library_schedule()"); err != nil {
					t.Fatal(err)
				}
				if n, err := runtime.QueueDue(ctx, 20); err != nil || n != 1 {
					t.Fatal(n, err)
				}
			}
		})
	}
}
func TestLibraryScanOwnedWritesRejectStaleLease(t *testing.T) {
	for _, kind := range []string{"owner", "token", "state", "expiry", "generation", "scope"} {
		t.Run(kind, func(t *testing.T) {
			f := newObservationFixture(t)
			repo, w := claimScanWork(t, f)
			ctx := context.Background()
			root, err := repo.LoadRoot(ctx, f.root)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "owner":
				_, err = f.pool.Exec(ctx, "UPDATE job SET lease_owner='replacement' WHERE id=$1", w.Lease.ID)
			case "token":
				_, err = f.pool.Exec(ctx, "UPDATE job SET lease_token = lease_token + 1 WHERE id=$1", w.Lease.ID)
			case "state":
				_, err = f.pool.Exec(ctx, "UPDATE job SET state='queued' WHERE id=$1", w.Lease.ID)
			case "expiry":
				_, err = f.pool.Exec(ctx, "UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1", w.Lease.ID)
			case "generation":
				_, err = f.pool.Exec(ctx, "UPDATE library_root SET generation = generation + 1 WHERE id=$1", f.root)
			case "scope":
				w.RootID = uuid.New()
				repo = postgres.NewLeasedScanObservationStore(f.pool, idgen.New(), w)
			}
			if err != nil {
				t.Fatal(err)
			}
			before := f.snapshot(t)
			if _, err = repo.UpdateProbedHealth(ctx, root, "online"); !errors.Is(err, ports.ErrScanObservationStale) {
				t.Fatal(err)
			}
			if _, err = repo.Record(ctx, f.input); !errors.Is(err, ports.ErrScanObservationStale) {
				t.Fatal(err)
			}
			if after := f.snapshot(t); after != before {
				t.Fatal("stale task wrote scan facts")
			}
		})
	}
}
func TestLibraryScanBaselineAndTaskCommitAtomically(t *testing.T) {
	f := newObservationFixture(t)
	repo, w := claimScanWork(t, f)
	ctx := context.Background()
	if _, err := repo.Record(ctx, f.input); err != nil {
		t.Fatal(err)
	}
	in := f.input
	seal := ports.ScanSeal{RootID: in.RootID, RunID: in.RunID, Path: in.Path, MediaType: in.MediaType, MarkerFilename: in.MarkerFilename, Generation: in.Generation, HealthRevision: in.HealthRevision, ScanToken: in.ScanToken, StartMutationSeq: in.StartMutationSeq, RootIdentity: ports.FileIdentity{Device: 1, Inode: 99}}
	if err := repo.Seal(ctx, seal); err != nil {
		t.Fatal(err)
	}
	before := finalizationSnapshot(t, f)
	if _, err := f.pool.Exec(ctx, `CREATE FUNCTION fail_scan_job_finish() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.state='succeeded' THEN RAISE EXCEPTION 'fixture'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_scan_job_finish BEFORE UPDATE ON job FOR EACH ROW EXECUTE FUNCTION fail_scan_job_finish()`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ReconcileBatch(ctx, seal); err == nil {
		t.Fatal("job completion failure committed baseline")
	}
	if after := finalizationSnapshot(t, f); after != before {
		t.Fatal("partial cursor or baseline")
	}
	if _, err := f.pool.Exec(ctx, "DROP TRIGGER fail_scan_job_finish ON job;DROP FUNCTION fail_scan_job_finish()"); err != nil {
		t.Fatal(err)
	}
	p, err := repo.ReconcileBatch(ctx, seal)
	if err != nil || !p.Done {
		t.Fatal(p, err)
	}
	var state string
	var missing int
	if err = f.pool.QueryRow(ctx, "SELECT state FROM job WHERE id=$1", w.Lease.ID).Scan(&state); err != nil || state != "succeeded" {
		t.Fatal(state, err)
	}
	if err = f.pool.QueryRow(ctx, "SELECT count(*) FROM job WHERE kind='library_missing'").Scan(&missing); err != nil || missing != 1 {
		t.Fatal(missing, err)
	}
}

func TestLibraryScanRunningRetryAtomicUnderPhysicalSlot(t *testing.T) {
	f := newObservationFixture(t)
	repo, w := claimScanWork(t, f)
	ctx := context.Background()
	physical := t.TempDir()
	if _, err := f.pool.Exec(ctx, "UPDATE library_root SET path=$2 WHERE id=$1", f.root, physical); err != nil {
		t.Fatal(err)
	}
	f.input.Path = physical
	_, release, err := rootfs.NewFilesystem().LockRoots(ctx, f.input.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	root, err := repo.LoadRoot(ctx, f.root)
	if err != nil {
		t.Fatal(err)
	}
	before := finalizationSnapshot(t, f)
	fresh := uuid.New()
	if _, err = f.pool.Exec(ctx, `CREATE FUNCTION fail_retry_scan() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic new scan failure'; END $$; CREATE TRIGGER fail_retry_scan BEFORE INSERT ON scan_run FOR EACH ROW EXECUTE FUNCTION fail_retry_scan();`); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.Start(ctx, root, fresh); err == nil {
		t.Fatal("new traversal fault committed")
	}
	if got := finalizationSnapshot(t, f); got != before {
		t.Fatal("old scan/root changed after failed retry")
	}
	linked, err := f.q.GetLibraryScanWork(ctx, w.Lease.ID)
	if err != nil || !linked.RunID.Valid || uuid.UUID(linked.RunID.Bytes) != f.input.RunID {
		t.Fatal("failed retry rebound job", err)
	}
	if _, err = f.pool.Exec(ctx, "DROP TRIGGER fail_retry_scan ON scan_run;DROP FUNCTION fail_retry_scan()"); err != nil {
		t.Fatal(err)
	}
	started, err := repo.Start(ctx, root, fresh)
	if err != nil {
		t.Fatal(err)
	}
	old, err := f.q.LockScanObservationRun(ctx, f.input.RunID)
	if err != nil || old.State != "partial" {
		t.Fatal(old.State, err)
	}
	linked, err = f.q.GetLibraryScanWork(ctx, w.Lease.ID)
	if err != nil || uuid.UUID(linked.RunID.Bytes) != fresh {
		t.Fatal("retry not bound", err)
	}
	in := f.input
	in.RunID = fresh
	in.ScanToken = started.ScanToken
	in.Generation = started.Generation
	in.StartMutationSeq = started.StartMutationSeq
	in.HealthRevision = root.HealthRevision
	if _, err = repo.Record(ctx, in); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.Record(ctx, f.input); !errors.Is(err, ports.ErrScanObservationStale) {
		t.Fatal("old run wrote", err)
	}
}
func TestLibraryScanRunBindingCannotAdoptLiveRun(t *testing.T) {
	f := newObservationFixture(t)
	repo, w := claimScanWork(t, f)
	ctx := context.Background()
	other := uuid.New()
	if _, err := f.pool.Exec(ctx, `INSERT INTO scan_run(id,root_id,generation,scan_token,start_mutation_seq,state) SELECT $1,root_id,generation,scan_token,start_mutation_seq,'running' FROM scan_run WHERE id=$2`, other, f.input.RunID); err != nil {
		t.Fatal(err)
	}
	in := f.input
	in.RunID = other
	if _, err := repo.Record(ctx, in); !errors.Is(err, ports.ErrScanObservationStale) {
		t.Fatal("unbound run wrote", err)
	}
	if n, err := f.q.BindLibraryScanRun(ctx, db.BindLibraryScanRunParams{JobID: w.Lease.ID, RunID: other}); err != nil || n != 0 {
		t.Fatal("live run replaced", n, err)
	}
	if n, err := f.q.BindLibraryScanRun(ctx, db.BindLibraryScanRunParams{JobID: w.Lease.ID, RunID: uuid.New()}); err != nil || n != 0 {
		t.Fatal("unrelated run bound", n, err)
	}
}
func TestLibraryScanLeaseRecheckedAfterRootWait(t *testing.T) {
	f := newObservationFixture(t)
	repo, w := claimScanWork(t, f)
	ctx := context.Background()
	blocker, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback(ctx) }()
	if _, err = blocker.Exec(ctx, "SELECT id FROM library_root WHERE id=$1 FOR UPDATE", f.root); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, e := repo.Record(ctx, f.input); done <- e }()
	deadline := time.Now().Add(time.Second)
	for {
		var n int
		if err = f.pool.QueryRow(ctx, "SELECT count(*) FROM pg_stat_activity WHERE wait_event_type='Lock' AND query LIKE '%LockAcquisitionRoot%'").Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no root wait")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err = f.pool.Exec(ctx, "UPDATE job SET lease_until=clock_timestamp()+interval '80 milliseconds' WHERE id=$1", w.Lease.ID); err != nil {
		t.Fatal(err)
	}
	for {
		var expired bool
		if err = f.pool.QueryRow(ctx, "SELECT lease_until<=clock_timestamp() FROM job WHERE id=$1", w.Lease.ID).Scan(&expired); err != nil {
			t.Fatal(err)
		}
		if expired {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err = blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-done; !errors.Is(err, ports.ErrScanObservationStale) {
		t.Fatal("post-wait expired write", err)
	}
	var n int
	if err = f.pool.QueryRow(ctx, "SELECT count(*) FROM scan_observation WHERE scan_run_id=$1", f.input.RunID).Scan(&n); err != nil || n != 0 {
		t.Fatal("expired observation committed", n, err)
	}
}
func TestLibraryMissingLastReceiptAndTaskAtomic(t *testing.T) {
	f, seal, file := readyMissingResolution(t)
	ctx := context.Background()
	gen := idgen.New()
	id := uuid.New()
	if err := f.q.InsertLibraryMissingJob(ctx, db.InsertLibraryMissingJobParams{JobID: id, RootID: f.root, RunID: seal.RunID}); err != nil {
		t.Fatal(err)
	}
	job, err := jobs.NewQueue(f.pool, gen, clock.New()).Claim(ctx, []string{"library_missing"}, "missing-owner", 5*time.Minute)
	if err != nil || job == nil {
		t.Fatal(job, err)
	}
	runtime := postgres.NewLibraryScanRuntimeStore(f.pool, gen)
	work, err := runtime.Work(ctx, ports.JobLease{ID: job.ID, Owner: "missing-owner", Token: job.LeaseToken})
	if err != nil {
		t.Fatal(err)
	}
	repo := postgres.NewLeasedScanObservationStore(f.pool, gen, work)
	before := missingProtectedSnapshot(t, f)
	if _, err = f.pool.Exec(ctx, `CREATE FUNCTION fail_missing_task_finish() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.state='succeeded' THEN RAISE EXCEPTION 'synthetic task finish fault'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_missing_task_finish BEFORE UPDATE ON job FOR EACH ROW EXECUTE FUNCTION fail_missing_task_finish();`); err != nil {
		t.Fatal(err)
	}
	if err = repo.ResolveMissing(ctx, seal, file, ports.ScanFileMissing); err == nil {
		t.Fatal("task finish fault committed")
	}
	if got := missingProtectedSnapshot(t, f); got != before {
		t.Fatal("partial missing commit")
	}
	var receipts int
	if err = f.pool.QueryRow(ctx, "SELECT count(*) FROM scan_missing_receipt").Scan(&receipts); err != nil || receipts != 0 {
		t.Fatal(receipts, err)
	}
	if _, err = f.pool.Exec(ctx, "DROP TRIGGER fail_missing_task_finish ON job;DROP FUNCTION fail_missing_task_finish()"); err != nil {
		t.Fatal(err)
	}
	if err = repo.ResolveMissing(ctx, seal, file, ports.ScanFileMissing); err != nil {
		t.Fatal(err)
	}
	saved, err := f.q.GetJobByID(ctx, id)
	if err != nil || saved.State != "succeeded" {
		t.Fatal(saved.State, err)
	}
	if err = repo.FinishMissingWork(ctx); err != nil {
		t.Fatal("completion receipt did not replay", err)
	}
}

func TestLibraryScanActiveRuntimePreventsDowngrade(t *testing.T) {
	f := newObservationFixture(t)
	_, _ = claimScanWork(t, f)
	ctx := context.Background()
	for v := int64(postgres.CurrentMigrationVersion); v > 51; {
		var err error
		v, err = postgres.Rollback(ctx, f.pool.Config().ConnConfig.ConnString(), migrationsDir)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := postgres.Rollback(ctx, f.pool.Config().ConnConfig.ConnString(), migrationsDir); err == nil {
		t.Fatal("active scoped work discarded")
	}
	var v int64
	if err := f.pool.QueryRow(ctx, "SELECT max(version_id) FROM goose_db_version WHERE is_applied").Scan(&v); err != nil || v != 51 {
		t.Fatal(v, err)
	}
}
func TestLibraryScanDeadMissingBlocksRootSchedule(t *testing.T) {
	f := newObservationFixture(t)
	repo, _ := claimScanWork(t, f)
	ctx := context.Background()
	seal := missingSeal(f)
	if err := repo.Seal(ctx, seal); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ReconcileBatch(ctx, seal); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, "UPDATE job SET state='dead' WHERE kind='library_missing'"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, "UPDATE library_scan_schedule SET next_run_at=clock_timestamp() WHERE root_id=$1", f.root); err != nil {
		t.Fatal(err)
	}
	if n, err := postgres.NewLibraryScanRuntimeStore(f.pool, idgen.New()).QueueDue(ctx, 20); err != nil || n != 0 {
		t.Fatal("dead missing task bypassed", n, err)
	}
}
