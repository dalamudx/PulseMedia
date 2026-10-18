package postgres_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Historical migration checks retain schema70's original Up/Down boundary.
func observation70MigrationDir(t *testing.T) string {
	t.Helper()
	target := t.TempDir()
	entries, err := os.ReadDir(filepath.Join("..", "..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".sql") || len(name) < 6 || name[:5] > "00070" {
			continue
		}
		content, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(target, name), content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return target
}
