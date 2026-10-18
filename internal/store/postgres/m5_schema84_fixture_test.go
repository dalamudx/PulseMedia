package postgres_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/store/postgres"
)

// Keep the original encrypted-storage and frozen-history contract at schema84.
// Lifecycle/management coverage independently applies schema85 in its own tests.
func m5IndexerSchema84(t *testing.T, dsn string) {
	t.Helper()
	root := filepath.Join("..", "..", "..", "migrations")
	dir := t.TempDir()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal("read migration directory failed")
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".sql") || len(name) < 6 || name[:5] > "00084" {
			continue
		}
		data, e := os.ReadFile(filepath.Join(root, name))
		if e != nil {
			t.Fatal("read migration failed")
		}
		if e = os.WriteFile(filepath.Join(dir, name), data, 0600); e != nil {
			t.Fatal("copy migration failed")
		}
	}
	if version, e := postgres.Migrate(t.Context(), dsn, dir); e != nil || version != 84 {
		t.Fatal("schema84 fixture migration failed")
	}
}
