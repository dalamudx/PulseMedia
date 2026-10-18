package postgres_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/jobs"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMovieUpgradeOrganizationExecutionBusinessRestorePG(t *testing.T) {
	f := organizationJobFixture76(t)
	ctx := context.Background()
	if v, e := postgres.Migrate(ctx, f.dsn, organization77MigrationDir(t)); e != nil || v != 77 {
		t.Fatal(v, e)
	}
	_, dsn := pgtest.GetCluster(t).CreateRoles(t, mustFrozenDB(t, f.dsn))
	p, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	logical := orgJobRepository(t, p)
	queued, e := logical.QueueMovieUpgradeOrganization(ctx, f.actor, f.command, "business77")
	if e != nil || queued == nil {
		t.Fatal(queued, e)
	}
	q := jobs.NewQueue(p, idgen.New(), nativeUpgradeClock{})
	j, e := q.Claim(ctx, []string{ports.MovieUpgradeOrganizationJobKind}, "physical-business", 2*time.Minute)
	if e != nil || j == nil {
		t.Fatal(j, e)
	}
	lease := ports.JobLease{ID: j.ID, Owner: j.LeaseOwner.String, Token: j.LeaseToken}
	if out, e := logical.CaptureMovieUpgradeOrganizationJob(ctx, f.actor, lease); e != nil || out == nil {
		t.Fatal(out, e)
	}
	files := &nativeLinkFS{TransferFilesystem: rootfs.NewFilesystem()}
	r := executionRepository(t, p, files)
	out, e := r.ExecuteMovieUpgradeOrganization(ctx, f.actor, lease)
	if e != nil || out == nil || out.Status != "linked_pending_ingestion" || files.calls.Load() != 1 {
		t.Fatal(out, e)
	}
	for _, query := range []string{`ALTER TABLE movie_upgrade_organization_execution_permit ADD COLUMN forbidden int`, `TRUNCATE movie_upgrade_organization_execution_result CASCADE`, `DELETE FROM movie_upgrade_organization_execution_permit`, `UPDATE movie_upgrade_organization_execution_result SET returned_at=returned_at+interval '1 second'`} {
		if _, e = p.Exec(ctx, query); e == nil {
			t.Fatal("business mutated frozen execution", query)
		}
	}
	dump := filepath.Join(t.TempDir(), "physical77.dump")
	frozenPGCommand(t, ctx, "pg_dump", f.dsn, "--no-password", "-Fc", "-f", dump)
	restored := pgtest.GetCluster(t).NewTestDB(t)
	frozenPGCommand(t, ctx, "pg_restore", restored, "--no-password", "--exit-on-error", "--no-owner", "--no-privileges", "-d", mustFrozenDB(t, restored), dump)
	rp, e := pgxpool.New(ctx, restored)
	if e != nil {
		t.Fatal(e)
	}
	defer rp.Close()
	if e = postgres.CheckSchemaVersion(ctx, rp, 77); e != nil {
		t.Fatal(e)
	}
	rr := executionRepository(t, rp, files)
	got, e := rr.ReadMovieUpgradeOrganizationExecutionReceipt(ctx, f.actor, lease.ID)
	if e != nil || got == nil || *got != *out {
		t.Fatal(got, e)
	}
	got, e = rr.ExecuteMovieUpgradeOrganization(ctx, f.actor, lease)
	if e != nil || got == nil || *got != *out || files.calls.Load() != 1 {
		t.Fatal("restored execution repeated", got, e)
	}
	if _, e = postgres.Rollback(ctx, restored, organization77MigrationDir(t)); e == nil || !strings.Contains(e.Error(), "upgrade organization physical history prevents downgrade") {
		t.Fatal(e)
	}
}
func TestMovieUpgradeOrganizationExecutionOrphanRestorePG(t *testing.T) {
	f, lease, _, _ := executionFixture77(t)
	files := newHeldNativeLinkFS()
	r := executionRepository(t, f.p, files)
	finished := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		_, e := r.ExecuteMovieUpgradeOrganization(context.Background(), f.actor, lease)
		finished <- e
		close(done)
	}()
	t.Cleanup(func() {
		files.resume()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Error("original physical call did not drain")
		}
	})
	select {
	case <-files.entered:
	case e := <-finished:
		t.Fatal(e)
	case <-time.After(20 * time.Second):
		t.Fatal("native call missing")
	}
	var pid int
	if e := f.p.QueryRow(context.Background(), `SELECT backend_pid FROM movie_upgrade_organization_execution_permit WHERE job_id=$1`, lease.ID).Scan(&pid); e != nil {
		t.Fatal(e)
	}
	f.exec(t, `SELECT pg_terminate_backend($1)`, pid)
	ctx := context.Background()
	dump := filepath.Join(t.TempDir(), "orphan77.dump")
	frozenPGCommand(t, ctx, "pg_dump", f.dsn, "--no-password", "-Fc", "-f", dump)
	restored := pgtest.GetCluster(t).NewTestDB(t)
	frozenPGCommand(t, ctx, "pg_restore", restored, "--no-password", "--exit-on-error", "--no-owner", "--no-privileges", "-d", mustFrozenDB(t, restored), dump)
	rp, e := pgxpool.New(ctx, restored)
	if e != nil {
		t.Fatal(e)
	}
	defer rp.Close()
	forbidden := &nativeLinkFS{TransferFilesystem: rootfs.NewFilesystem()}
	rr := executionRepository(t, rp, forbidden)
	if out, e := rr.ExecuteMovieUpgradeOrganization(ctx, f.actor, lease); out != nil || !errors.Is(e, ports.ErrMovieUpgradeOrganizationReview) || forbidden.calls.Load() != 0 {
		t.Fatal(out, e)
	}
	files.resume()
	if e = <-finished; e == nil {
		t.Fatal("lost original session recorded success")
	}
	if permits, results := executionCounts(t, f); permits != 1 || results != 0 {
		t.Fatal(permits, results)
	}
	if _, e = postgres.Rollback(ctx, restored, organization77MigrationDir(t)); e == nil || !strings.Contains(e.Error(), "upgrade organization physical history prevents downgrade") {
		t.Fatal(e)
	}
}
func TestMovieUpgradeOrganizationExecutionConcurrentDownPG(t *testing.T) {
	f, lease, _, r := executionFixture77(t)
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	holder, e := f.p.Acquire(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer holder.Release()
	const key int64 = 177885012734
	if _, e = holder.Exec(ctx, `SELECT pg_advisory_lock($1)`, key); e != nil {
		t.Fatal(e)
	}
	f.exec(t, `CREATE FUNCTION native77_gate()RETURNS TRIGGER LANGUAGE plpgsql AS $$BEGIN PERFORM pg_advisory_xact_lock(177885012734);RETURN NEW;END $$;CREATE TRIGGER native77_gate AFTER INSERT ON movie_upgrade_organization_execution_permit FOR EACH ROW EXECUTE FUNCTION native77_gate()`)
	written := make(chan error, 1)
	go func() { _, e := r.ExecuteMovieUpgradeOrganization(ctx, f.actor, lease); written <- e }()
	drained := false
	defer func() {
		_, _ = holder.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, key)
		cancel()
		if !drained {
			<-written
		}
	}()
	// Wait for the actual lock event within the operation's deadline; race runs
	// with several private clusters can take longer than the shared 3s helper.
	waitBlocked := func(blocker uint32) {
		t.Helper()
		work, stop := context.WithTimeout(ctx, 12*time.Second)
		defer stop()
		for work.Err() == nil {
			var blocked bool
			if err := f.p.QueryRow(work, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND $1::integer=ANY(pg_blocking_pids(pid)))`, blocker).Scan(&blocked); err != nil {
				t.Fatal(err)
			}
			if blocked {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatal("execution did not reach held lock", work.Err())
	}
	waitBlocked(holder.Conn().PgConn().PID())
	var writer uint32
	if e = f.p.QueryRow(ctx, `SELECT pid FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid))`, holder.Conn().PgConn().PID()).Scan(&writer); e != nil {
		t.Fatal(e)
	}
	down := make(chan error, 1)
	go func() { _, e := postgres.Rollback(ctx, f.dsn, organization77MigrationDir(t)); down <- e }()
	downDrained := false
	defer func() {
		if !downDrained {
			cancel()
			<-down
		}
	}()
	waitBlocked(writer)
	if _, e = holder.Exec(ctx, `SELECT pg_advisory_unlock($1)`, key); e != nil {
		t.Fatal(e)
	}
	e = <-written
	drained = true
	if e != nil {
		t.Fatal(e)
	}
	e = <-down
	downDrained = true
	// PostgreSQL may choose DDL as the deadlock victim while the writer commits.
	// The failed DDL must roll back, leaving the committed permit protected.
	var conflict *pgconn.PgError
	if errors.As(e, &conflict) && conflict.Code == "40P01" {
		_, e = postgres.Rollback(ctx, f.dsn, organization77MigrationDir(t))
	}
	if e == nil || !strings.Contains(e.Error(), "upgrade organization physical history prevents downgrade") {
		t.Fatal("concurrent downgrade", e)
	}
	if permits, results := executionCounts(t, f); permits != 1 || results != 1 {
		t.Fatal("concurrent downgrade lost execution history", permits, results)
	}
}
