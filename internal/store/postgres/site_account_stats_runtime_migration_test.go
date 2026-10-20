package postgres_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
)

func TestSiteAccountStatsRuntimeMigrationPG(t *testing.T) {
	history98 := t.TempDir()
	entries, err := os.ReadDir("../../../migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") || len(entry.Name()) < 5 || entry.Name()[:5] > "00098" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join("../../../migrations", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(history98, entry.Name()), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("empty", func(t *testing.T) {
		dsn := pgtest.GetCluster(t).NewTestDB(t)
		if v, e := postgres.Migrate(t.Context(), dsn, history98); e != nil || v != 98 {
			t.Fatal(v, e)
		}
		if v, e := postgres.Rollback(t.Context(), dsn, history98); e != nil || v != 97 {
			t.Fatal(v, e)
		}
		if v, e := postgres.Migrate(t.Context(), dsn, history98); e != nil || v != 98 {
			t.Fatal(v, e)
		}
	})
	t.Run("history", func(t *testing.T) {
		f := newStatsFixtureAt(t, history98, 98)
		f.save(t, f.input(), "runtime")
		s := postgres.NewSiteAccountStatsStore(f.p, idgen.New(), f.secrets)
		if e := s.EnqueueDue(t.Context()); e != nil {
			t.Fatal(e)
		}
		if _, e := postgres.Rollback(t.Context(), f.dsn, history98); e == nil {
			t.Fatal("nonempty downgrade accepted")
		}
		statsCount(t, f, `SELECT count(*) FROM site_account_stats_attempt`, 1)
	})
}
