package postgres_test

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Historical schema68 tests must keep exercising schema68's original Up/Down.
func dispatchMigrationDir(t *testing.T) string {
	t.Helper()
	source := filepath.Join("..", "..", "..", "migrations")
	dir := t.TempDir()
	entries, e := os.ReadDir(source)
	if e != nil {
		t.Fatal(e)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		v, e := strconv.Atoi(strings.SplitN(entry.Name(), "_", 2)[0])
		if e != nil {
			t.Fatal(e)
		}
		if v > 68 {
			continue
		}
		b, e := os.ReadFile(filepath.Join(source, entry.Name()))
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(dir, entry.Name()), b, 0600); e != nil {
			t.Fatal(e)
		}
	}
	return dir
}
