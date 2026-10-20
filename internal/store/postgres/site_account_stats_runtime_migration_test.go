package postgres_test

import (
	"testing"

	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
)

func TestSiteAccountStatsRuntimeMigrationPG(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		dsn := pgtest.GetCluster(t).NewTestDB(t)
		if v, e := postgres.Migrate(t.Context(), dsn, "../../../migrations"); e != nil || v != 98 {
			t.Fatal(v, e)
		}
		if v, e := postgres.Rollback(t.Context(), dsn, "../../../migrations"); e != nil || v != 97 {
			t.Fatal(v, e)
		}
		if v, e := postgres.Migrate(t.Context(), dsn, "../../../migrations"); e != nil || v != 98 {
			t.Fatal(v, e)
		}
	})
	t.Run("history", func(t *testing.T) {
		f, s := runtimeStats(t)
		if e := s.EnqueueDue(t.Context()); e != nil {
			t.Fatal(e)
		}
		if _, e := postgres.Rollback(t.Context(), f.dsn, "../../../migrations"); e == nil {
			t.Fatal("nonempty downgrade accepted")
		}
		statsCount(t, f, `SELECT count(*) FROM site_account_stats_attempt`, 1)
	})
}
