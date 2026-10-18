package postgres_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Historical result assertions must keep exercising schema67 as migrations advance.
func sendResultMigrationDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	source := filepath.Join("..", "..", "..", "migrations")
	entries, e := os.ReadDir(source)
	if e != nil {
		t.Fatal(e)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".sql") || name >= "00068" {
			continue
		}
		data, e := os.ReadFile(filepath.Join(source, name))
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(dir, name), data, 0600); e != nil {
			t.Fatal(e)
		}
	}
	return dir
}
