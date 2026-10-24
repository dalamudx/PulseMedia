package postgres_test

import (
	"testing"

	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
)

func TestSiteCheckinRuntimeMigrationPG(t *testing.T) {
	t.Run("empty_99_100_99_100", func(t *testing.T) {
		dsn := pgtest.GetCluster(t).NewTestDB(t)
		v, e := postgres.Migrate(t.Context(), dsn, repositoryMigrationsThrough(t, 99))
		if e != nil || v != 99 {
			t.Fatal(v, e)
		}
		migration100 := repositoryMigrationsThrough(t, 100)
		v, e = postgres.Migrate(t.Context(), dsn, migration100)
		if e != nil || v != 100 {
			t.Fatal(v, e)
		}
		v, e = postgres.Rollback(t.Context(), dsn, migration100)
		if e != nil || v != 99 {
			t.Fatal(v, e)
		}
		v, e = postgres.Migrate(t.Context(), dsn, migration100)
		if e != nil || v != 100 {
			t.Fatal(v, e)
		}
	})
	t.Run("history_blocks_downgrade", func(t *testing.T) {
		f := runtimeCheckin(t, 100)
		if e := f.s.EnqueueDue(t.Context()); e != nil {
			t.Fatal(e)
		}
		if _, e := postgres.Rollback(t.Context(), f.dsn, repositoryMigrationsThrough(t, 100)); e == nil {
			t.Fatal("runtime history downgrade accepted")
		}
		f.count(t, `SELECT count(*) FROM site_checkin_day`, 1)
	})
}
