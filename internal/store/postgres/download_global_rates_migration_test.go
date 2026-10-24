package postgres_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
)

func TestDownloadGlobalRatesMigrationPG(t *testing.T) {
	history := t.TempDir()
	entries, err := os.ReadDir("../../../migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".sql") || len(name) < 5 || name[:5] > "00106" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join("../../../migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(history, name), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	dsn := pgtest.GetCluster(t).NewTestDB(t)
	if version, err := postgres.Migrate(t.Context(), dsn, history); err != nil || version != 106 {
		t.Fatal("global rate schema migration", version, err)
	}
	if version, err := postgres.Rollback(t.Context(), dsn, history); err != nil || version != 105 {
		t.Fatal("empty global rate downgrade", version, err)
	}
	if version, err := postgres.Migrate(t.Context(), dsn, history); err != nil || version != 106 {
		t.Fatal("global rate remigration", version, err)
	}
}
