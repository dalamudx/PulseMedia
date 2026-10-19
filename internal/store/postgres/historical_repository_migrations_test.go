package postgres_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Historical repository cases target their own DDL; current App fixtures still
// migrate to the latest schema before opening HTTP or workers.
func repositoryMigrationsThrough(t *testing.T, through ...int64) string {
	t.Helper()
	if len(through) == 0 {
		return migrationsDir
	}
	target := t.TempDir()
	entries, err := os.ReadDir(migrationsDir)
	if err != nil {
		t.Fatal(err)
	}
	maximum := fmt.Sprintf("%05d", through[0])
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".sql") || len(name) < 6 || name[:5] > maximum {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(migrationsDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(target, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return target
}
