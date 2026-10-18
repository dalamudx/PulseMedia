package postgres_test

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/adapter/torrent"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/jobs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/jackc/pgx/v5/pgxpool"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func orgJobRepository(t *testing.T, p *pgxpool.Pool) ports.MovieUpgradeOrganizationJobRepository {
	t.Helper()
	s, e := postgres.NewUpgradePreparationStore(p, idgen.New(), torrent.Inspector{})
	if e != nil {
		t.Fatal(e)
	}
	r, e := postgres.NewMovieUpgradeOrganizationJobRepository(s)
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func TestMovieUpgradeOrganizationJobBusinessRestorePG(t *testing.T) {
	f := organizationJobFixture76(t)
	ctx := context.Background()
	_, dsn := pgtest.GetCluster(t).CreateRoles(t, mustFrozenDB(t, f.dsn))
	p, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	r := orgJobRepository(t, p)
	out, e := r.QueueMovieUpgradeOrganization(ctx, f.actor, f.command, "business-command")
	if e != nil || out == nil {
		t.Fatal(out, e)
	}
	q := jobs.NewQueue(p, idgen.New(), nativeUpgradeClock{})
	j, e := q.Claim(ctx, []string{ports.MovieUpgradeOrganizationJobKind}, "business-org-executor", time.Minute)
	if e != nil || j == nil {
		t.Fatal(j, e)
	}
	lease := ports.JobLease{ID: j.ID, Owner: j.LeaseOwner.String, Token: j.LeaseToken}
	claim, e := r.CaptureMovieUpgradeOrganizationJob(ctx, f.actor, lease)
	if e != nil || claim == nil || claim.ExecutionAllowed {
		t.Fatal(claim, e)
	}
	for _, query := range []string{`ALTER TABLE movie_upgrade_organization_job ADD COLUMN forbidden int`, `TRUNCATE movie_upgrade_organization_job CASCADE`, `DELETE FROM movie_upgrade_organization_job_claim`, `UPDATE movie_upgrade_organization_job SET commanded_at=commanded_at+interval '1 second'`} {
		if _, e = p.Exec(ctx, query); e == nil {
			t.Fatal("business mutation", query)
		}
	}
	dump := filepath.Join(t.TempDir(), "organization76.dump")
	frozenPGCommand(t, ctx, "pg_dump", f.dsn, "--no-password", "-Fc", "-f", dump)
	restored := pgtest.GetCluster(t).NewTestDB(t)
	frozenPGCommand(t, ctx, "pg_restore", restored, "--no-password", "--exit-on-error", "--no-owner", "--no-privileges", "-d", mustFrozenDB(t, restored), dump)
	rp, e := pgxpool.New(ctx, restored)
	if e != nil {
		t.Fatal(e)
	}
	defer rp.Close()
	rr := orgJobRepository(t, rp)
	if e = postgres.CheckSchemaVersion(ctx, rp, 76); e != nil {
		t.Fatal(e)
	}
	recovered, e := rr.ReadMovieUpgradeOrganizationJobReceipt(ctx, f.actor, f.operation, "business-command")
	if e != nil || recovered == nil || *recovered != *out {
		t.Fatal(recovered, e)
	}
	if _, e = rr.CaptureMovieUpgradeOrganizationJob(ctx, f.actor, lease); e == nil {
		t.Fatal("restored claim taken over")
	}
	if _, e = postgres.Rollback(ctx, restored, organization76MigrationDir(t)); e == nil || !strings.Contains(e.Error(), "upgrade organization job history prevents downgrade") {
		t.Fatal("history downgrade", e)
	}
}
func TestMovieUpgradeOrganizationJobConcurrentDownPG(t *testing.T) {
	f := organizationJobFixture76(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	holder, e := f.p.Acquire(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer holder.Release()
	const key int64 = 176885012734
	if _, e = holder.Exec(ctx, `SELECT pg_advisory_lock($1)`, key); e != nil {
		t.Fatal(e)
	}
	f.exec(t, `CREATE FUNCTION org_command_gate() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(176885012734);RETURN NEW;END $$;CREATE TRIGGER org_command_gate AFTER INSERT ON movie_upgrade_organization_job FOR EACH ROW EXECUTE FUNCTION org_command_gate()`)
	written := make(chan error, 1)
	go func() {
		_, e := f.repo.QueueMovieUpgradeOrganization(ctx, f.actor, f.command, "concurrent-command")
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
	go func() { _, e := postgres.Rollback(ctx, f.dsn, organization76MigrationDir(t)); down <- e }()
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
	if e == nil || !strings.Contains(e.Error(), "upgrade organization job history prevents downgrade") {
		t.Fatal("concurrent downgrade", e)
	}
}
