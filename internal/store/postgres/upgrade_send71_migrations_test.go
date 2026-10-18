package postgres_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// SEND repository tests keep their original71 boundary; tracking explicitly
// migrates their completed native sends to72.
func send71MigrationDir(t *testing.T) string {
	t.Helper()
	target := t.TempDir()
	entries, e := os.ReadDir("../../../migrations")
	if e != nil {
		t.Fatal(e)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".sql") || len(name) < 6 || name[:5] > "00071" {
			continue
		}
		raw, e := os.ReadFile(filepath.Join("../../../migrations", name))
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(target, name), raw, 0600); e != nil {
			t.Fatal(e)
		}
	}
	return target
}
