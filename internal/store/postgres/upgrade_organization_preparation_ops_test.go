package postgres_test

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/jackc/pgx/v5/pgxpool"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMovieUpgradeOrganizationPhysicalMigrationPG(t *testing.T) {
	dsn := pgtest.GetCluster(t).NewTestDB(t)
	ctx := context.Background()
	if v, e := postgres.Migrate(ctx, dsn, organization74MigrationDir(t)); e != nil || v != 74 {
		t.Fatal(v, e)
	}
	if v, e := postgres.Migrate(ctx, dsn, organization75MigrationDir(t)); e != nil || v != 75 {
		t.Fatal(v, e)
	}
	if v, e := postgres.Rollback(ctx, dsn, organization75MigrationDir(t)); e != nil || v != 74 {
		t.Fatal(v, e)
	}
	if v, e := postgres.Migrate(ctx, dsn, organization75MigrationDir(t)); e != nil || v != 75 {
		t.Fatal(v, e)
	}
}
func TestMovieUpgradeOrganizationPhysicalBusinessRestorePG(t *testing.T) {
	f := physicalOrganizationFixture75(t)
	ctx := context.Background()
	_, dsn := pgtest.GetCluster(t).CreateRoles(t, mustFrozenDB(t, f.dsn))
	p, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	r := physicalPlanRepository(t, p, rootfs.NewFilesystem())
	out, e := r.PrepareMovieUpgradeOrganization(ctx, f.actor, f.command, "business-plan")
	if e != nil || out == nil || out.ExecutionAllowed {
		t.Fatal(out, e)
	}
	for _, query := range []string{`ALTER TABLE movie_upgrade_organization_preparation ADD COLUMN forbidden int`, `TRUNCATE movie_upgrade_organization_preparation CASCADE`, `DELETE FROM movie_upgrade_organization_preparation`, `UPDATE movie_upgrade_organization_preparation SET prepared_at=prepared_at+interval '1 second'`, `UPDATE transfer_plan SET state='approved',requires_approval=false`, `UPDATE transfer_step SET source_inode=source_inode+1`} {
		if _, e = p.Exec(ctx, query); e == nil {
			t.Fatal("business changed frozen history", query)
		}
	}
	dump := filepath.Join(t.TempDir(), "physical75.dump")
	frozenPGCommand(t, ctx, "pg_dump", f.dsn, "--no-password", "-Fc", "-f", dump)
	restored := pgtest.GetCluster(t).NewTestDB(t)
	frozenPGCommand(t, ctx, "pg_restore", restored, "--no-password", "--exit-on-error", "--no-owner", "--no-privileges", "-d", mustFrozenDB(t, restored), dump)
	rp, e := pgxpool.New(ctx, restored)
	if e != nil {
		t.Fatal(e)
	}
	defer rp.Close()
	if e = postgres.CheckSchemaVersion(ctx, rp, 75); e != nil {
		t.Fatal(e)
	}
	rr := physicalPlanRepository(t, rp, rootfs.NewFilesystem())
	again, e := rr.ReadMovieUpgradeOrganizationPreparationReceipt(ctx, f.actor, f.operation, "business-plan")
	if e != nil || again == nil || *again != *out {
		t.Fatal(again, e)
	}
	if _, e = postgres.Rollback(ctx, restored, organization75MigrationDir(t)); e == nil || !strings.Contains(e.Error(), "upgrade physical preparation history prevents downgrade") {
		t.Fatal("history downgrade", e)
	}
}
func TestMovieUpgradeOrganizationPhysicalDownConcurrentPG(t *testing.T) {
	f := physicalOrganizationFixture75(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	holder, e := f.p.Acquire(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer holder.Release()
	const key int64 = 175885012734
	if _, e = holder.Exec(ctx, `SELECT pg_advisory_lock($1)`, key); e != nil {
		t.Fatal(e)
	}
	f.exec(t, `CREATE FUNCTION pulse_test_physical_gate() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN PERFORM pg_advisory_xact_lock(175885012734);RETURN NEW;END$$;CREATE TRIGGER pulse_test_physical_gate AFTER INSERT ON movie_upgrade_organization_preparation FOR EACH ROW EXECUTE FUNCTION pulse_test_physical_gate()`)
	written := make(chan error, 1)
	go func() {
		_, e := f.preparation.PrepareMovieUpgradeOrganization(ctx, f.actor, f.command, "concurrent-down")
		written <- e
	}()
	drained := false
	defer func() {
		_, _ = holder.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, key)
		cancel()
		if !drained {
			<-written
		}
	}()
	waitPreparationBlock(t, f.p, holder.Conn().PgConn().PID())
	var writer uint32
	if e = f.p.QueryRow(ctx, `SELECT pid FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid))`, holder.Conn().PgConn().PID()).Scan(&writer); e != nil {
		t.Fatal(e)
	}
	down := make(chan error, 1)
	go func() { _, e := postgres.Rollback(ctx, f.dsn, organization75MigrationDir(t)); down <- e }()
	downDrained := false
	defer func() {
		if !downDrained {
			cancel()
			<-down
		}
	}()
	waitPreparationBlock(t, f.p, writer)
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
	if e == nil || !strings.Contains(e.Error(), "upgrade physical preparation history prevents downgrade") {
		t.Fatal("concurrent downgrade", e)
	}
	f.planCount(t, 1)
}
