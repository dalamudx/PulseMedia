package postgres_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The reservation fixture tests the immutable schema64 contract. A later
// migration must not silently turn its Down checks into checks of newer DDL.
func frozenMigrationDir(t *testing.T) string {
	t.Helper()
	if path := os.Getenv("PULSE_UPGRADE_MIGRATIONS"); path != "" {
		return path
	}
	source := filepath.Join("..", "..", "..", "migrations")
	target := t.TempDir()
	entries, err := os.ReadDir(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".sql") || len(name) < 6 || name[:5] > "00064" {
			continue
		}
		content, err := os.ReadFile(filepath.Join(source, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(target, name), content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return target
}
