package postgres_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
)

func TestSiteCheckinRuntimeMigrationPG(t *testing.T) {
	t.Run("empty_99_100_99_100", func(t *testing.T) {
		dsn := pgtest.GetCluster(t).NewTestDB(t)
		// Only this task's temporary migration set is removed by testing cleanup.
		dir := t.TempDir()
		files, e := filepath.Glob("../../../migrations/*.sql")
		if e != nil {
			t.Fatal(e)
		}
		for _, path := range files {
			if filepath.Base(path) > "00099_site_checkin_config.sql" {
				continue
			}
			b, e := os.ReadFile(path)
			if e != nil {
				t.Fatal(e)
			}
			if e = os.WriteFile(filepath.Join(dir, filepath.Base(path)), b, 0600); e != nil {
				t.Fatal(e)
			}
		}
		v, e := postgres.Migrate(t.Context(), dsn, dir)
		if e != nil || v != 99 {
			t.Fatal(v, e)
		}
		v, e = postgres.Migrate(t.Context(), dsn, "../../../migrations")
		if e != nil || v != 100 {
			t.Fatal(v, e)
		}
		v, e = postgres.Rollback(t.Context(), dsn, "../../../migrations")
		if e != nil || v != 99 {
			t.Fatal(v, e)
		}
		v, e = postgres.Migrate(t.Context(), dsn, "../../../migrations")
		if e != nil || v != 100 {
			t.Fatal(v, e)
		}
	})
	t.Run("history_blocks_downgrade", func(t *testing.T) {
		f := runtimeCheckin(t)
		if e := f.s.EnqueueDue(t.Context()); e != nil {
			t.Fatal(e)
		}
		if _, e := postgres.Rollback(t.Context(), f.dsn, "../../../migrations"); e == nil {
			t.Fatal("runtime history downgrade accepted")
		}
		f.count(t, `SELECT count(*) FROM site_checkin_day`, 1)
	})
}
