package postgres_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMovieUpgradeRetirementBusinessRestorePG(t *testing.T) {
	f, _, files, sub := retirementFixture79(t)
	ctx := context.Background()
	_, dsn := pgtest.GetCluster(t).CreateRoles(t, mustFrozenDB(t, f.dsn))
	p, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	r, e := postgres.NewMovieUpgradeRetirementStore(nativePreparationStore(t, p), files)
	if e != nil {
		t.Fatal(e)
	}
	input, receipt := confirmRetirement79(t, f, r, sub)
	if e = r.ExecuteMovieUpgradeRetirement(ctx, f.actor, f.operation); e != nil {
		t.Fatal(e)
	}
	if files.calls.Load() != 1 || retirementStatus79(t, f) != "retired" {
		t.Fatal("business role did not retire exactly once")
	}
	for _, statement := range []string{`ALTER TABLE movie_upgrade_retirement_command ADD COLUMN forbidden int`, `TRUNCATE movie_upgrade_retirement_result`, `DELETE FROM movie_upgrade_retirement_permit`, `UPDATE movie_upgrade_retirement_result SET retained_until=retained_until+interval '1 day'`} {
		if _, e = p.Exec(ctx, statement); e == nil {
			t.Fatal("business mutated frozen retirement", statement)
		}
	}
	dump := filepath.Join(t.TempDir(), "retirement79.dump")
	frozenPGCommand(t, ctx, "pg_dump", f.dsn, "--no-password", "-Fc", "-f", dump)
	restored := pgtest.GetCluster(t).NewTestDB(t)
	frozenPGCommand(t, ctx, "pg_restore", restored, "--no-password", "--exit-on-error", "--no-owner", "--no-privileges", "-d", mustFrozenDB(t, restored), dump)
	rp, e := pgxpool.New(ctx, restored)
	if e != nil {
		t.Fatal(e)
	}
	defer rp.Close()
	rr, e := postgres.NewMovieUpgradeRetirementStore(nativePreparationStore(t, rp), files)
	if e != nil {
		t.Fatal(e)
	}
	got, e := rr.ReadMovieUpgradeRetirementReceipt(ctx, f.actor, sub, f.operation, "retire-79")
	if e != nil || got == nil || *got != *receipt {
		t.Fatal("restored receipt", got, e)
	}
	got, e = rr.ConfirmMovieUpgradeRetirement(ctx, f.actor, sub, "retire-79", input)
	if e != nil || got == nil || *got != *receipt {
		t.Fatal("restored confirmation", got, e)
	}
	if e = rr.ExecuteMovieUpgradeRetirement(ctx, f.actor, f.operation); e != nil || files.calls.Load() != 1 {
		t.Fatal("restored retirement repeated", e, files.calls.Load())
	}
	rollbackUpgradeTestTo(t, ctx, restored, rp, 79)
	if _, e = postgres.Rollback(ctx, restored, "../../../migrations"); e == nil || !strings.Contains(e.Error(), "upgrade retirement history prevents downgrade") {
		t.Fatal("retirement history lost", e)
	}
}
