package postgres_test

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/adapter/torrent"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/jackc/pgx/v5/pgxpool"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMovieUpgradeOrganizationMigrationPG(t *testing.T) {
	f := nativeTrackingJobFixture(t)
	ctx := context.Background()
	definition := func() string {
		var s string
		if e := f.p.QueryRow(ctx, `SELECT pg_get_functiondef('pulse_assert_upgrade_tracking(uuid)'::regprocedure)`).Scan(&s); e != nil {
			t.Fatal(e)
		}
		return s
	}
	before := definition()
	if v, e := postgres.Migrate(ctx, f.dsn, organization74MigrationDir(t)); e != nil || v != 74 {
		t.Fatal(v, e)
	}
	if v, e := postgres.Rollback(ctx, f.dsn, organization74MigrationDir(t)); e != nil || v != 73 {
		t.Fatal(v, e)
	}
	if definition() != before {
		t.Fatal("historical tracking definition changed")
	}
	if v, e := postgres.Migrate(ctx, f.dsn, organization74MigrationDir(t)); e != nil || v != 74 {
		t.Fatal(v, e)
	}
}
func organizationPool(t *testing.T, p *pgxpool.Pool) ports.MovieUpgradeOrganizationRepository {
	t.Helper()
	s, e := postgres.NewUpgradePreparationStore(p, idgen.New(), torrent.Inspector{})
	if e != nil {
		t.Fatal(e)
	}
	r, e := postgres.NewMovieUpgradeOrganizationRepository(s)
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func TestMovieUpgradeOrganizationBusinessRestorePG(t *testing.T) {
	f := nativeOrganizationFixture(t, "completed")
	ctx := context.Background()
	c := f.command(t)
	_, dsn := pgtest.GetCluster(t).CreateRoles(t, mustFrozenDB(t, f.dsn))
	p, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	r := organizationPool(t, p)
	out, e := r.AuthorizeMovieUpgradeOrganization(ctx, f.actor, c, "business-organization")
	if e != nil || out == nil || out.ExecutionAllowed {
		t.Fatal(out, e)
	}
	for _, sql := range []string{`ALTER TABLE movie_upgrade_organization_authorization ADD COLUMN forbidden int`, `TRUNCATE movie_upgrade_organization_authorization`, `DELETE FROM movie_upgrade_organization_authorization`, `UPDATE movie_upgrade_organization_authorization SET confirmed=false`} {
		if _, e = p.Exec(ctx, sql); e == nil {
			t.Fatal("business mutated DDL/history", sql)
		}
	}
	dump := filepath.Join(t.TempDir(), "organization74.dump")
	frozenPGCommand(t, ctx, "pg_dump", f.dsn, "--no-password", "-Fc", "-f", dump)
	restored := pgtest.GetCluster(t).NewTestDB(t)
	frozenPGCommand(t, ctx, "pg_restore", restored, "--no-password", "--exit-on-error", "--no-owner", "--no-privileges", "-d", mustFrozenDB(t, restored), dump)
	rp, e := pgxpool.New(ctx, restored)
	if e != nil {
		t.Fatal(e)
	}
	defer rp.Close()
	if e = postgres.CheckSchemaVersion(ctx, rp, 74); e != nil {
		t.Fatal(e)
	}
	rr := organizationPool(t, rp)
	again, e := rr.ReadMovieUpgradeOrganizationReceipt(ctx, f.actor, f.operation, "business-organization")
	if e != nil || again == nil || *again != *out {
		t.Fatal(again, e)
	}
	if _, e = postgres.Rollback(ctx, restored, organization74MigrationDir(t)); e == nil || !strings.Contains(e.Error(), "upgrade organization history prevents downgrade") {
		t.Fatal("history downgrade", e)
	}
}
func TestMovieUpgradeOrganizationAccountWaitPG(t *testing.T) {
	for _, receipt := range []bool{false, true} {
		name := "confirmation"
		if receipt {
			name = "receipt"
		}
		t.Run(name, func(t *testing.T) {
			f := nativeOrganizationFixture(t, "completed")
			c := f.command(t)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if receipt {
				if _, e := f.organization.AuthorizeMovieUpgradeOrganization(ctx, f.actor, c, "wait-key"); e != nil {
					t.Fatal(e)
				}
			}
			holder, e := f.p.Acquire(ctx)
			if e != nil {
				t.Fatal(e)
			}
			defer holder.Release()
			tx, e := holder.Begin(ctx)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = tx.Exec(ctx, `SELECT id FROM app_user WHERE id=$1 FOR UPDATE`, f.actor.ID); e != nil {
				t.Fatal(e)
			}
			done := make(chan error, 1)
			go func() {
				if receipt {
					_, e := f.organization.ReadMovieUpgradeOrganizationReceipt(ctx, f.actor, f.operation, "wait-key")
					done <- e
				} else {
					_, e := f.organization.AuthorizeMovieUpgradeOrganization(ctx, f.actor, c, "wait-key")
					done <- e
				}
			}()
			drained := false
			defer func() {
				cancel()
				c, stop := context.WithTimeout(context.Background(), 5*time.Second)
				defer stop()
				_ = tx.Rollback(c)
				if !drained {
					<-done
				}
			}()
			waitPreparationBlock(t, f.p, holder.Conn().PgConn().PID())
			if _, e = tx.Exec(ctx, `UPDATE app_user SET is_active=false WHERE id=$1`, f.actor.ID); e != nil {
				t.Fatal(e)
			}
			if e = tx.Commit(ctx); e != nil {
				t.Fatal(e)
			}
			e = <-done
			drained = true
			if e == nil {
				t.Fatal("revoked actor admitted")
			}
			want := 0
			if receipt {
				want = 1
			}
			f.unchanged(t, nativeProtectedGraph(t, f.preparedUpgradeFixture), want)
		})
	}
}
func TestMovieUpgradeOrganizationInitialRowPG(t *testing.T) {
	f := nativeOrganizationFixture(t, "completed")
	ctx := context.Background()
	c := f.command(t)
	before := nativeProtectedGraph(t, f.preparedUpgradeFixture)
	if _, e := f.p.Exec(ctx, `CREATE FUNCTION pulse_test_org_initial() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN NEW.id:=gen_random_uuid();RETURN NEW;END $$;CREATE TRIGGER pulse_test_org_initial BEFORE INSERT ON movie_upgrade_organization_authorization FOR EACH ROW EXECUTE FUNCTION pulse_test_org_initial()`); e != nil {
		t.Fatal(e)
	}
	if _, e := f.organization.AuthorizeMovieUpgradeOrganization(ctx, f.actor, c, "initial-key"); e == nil {
		t.Fatal("altered initial row accepted")
	}
	f.unchanged(t, before, 0)
}
func TestMovieUpgradeOrganizationDownConcurrentPG(t *testing.T) {
	f := nativeOrganizationFixture(t, "completed")
	c := f.command(t)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	holder, e := f.p.Acquire(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer holder.Release()
	const key int64 = 174885012734
	if _, e = holder.Exec(ctx, `SELECT pg_advisory_lock($1)`, key); e != nil {
		t.Fatal(e)
	}
	if _, e = f.p.Exec(ctx, `CREATE FUNCTION pulse_test_org_gate() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(174885012734);RETURN NEW;END $$;CREATE TRIGGER pulse_test_org_gate AFTER INSERT ON movie_upgrade_organization_authorization FOR EACH ROW EXECUTE FUNCTION pulse_test_org_gate()`); e != nil {
		t.Fatal(e)
	}
	written := make(chan error, 1)
	go func() {
		_, e := f.organization.AuthorizeMovieUpgradeOrganization(ctx, f.actor, c, "concurrent-down-key")
		written <- e
	}()
	drained := false
	defer func() {
		cancel()
		c, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_, _ = holder.Exec(c, `SELECT pg_advisory_unlock($1)`, key)
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
	go func() { _, e := postgres.Rollback(ctx, f.dsn, organization74MigrationDir(t)); down <- e }()
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
	if e == nil || !strings.Contains(e.Error(), "upgrade organization history prevents downgrade") {
		t.Fatal("concurrent downgrade", e)
	}
}
