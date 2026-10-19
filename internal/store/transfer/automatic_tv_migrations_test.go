package transfer

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// These cases exercise schema91 organization history, not future display tables.
func automaticTVOrganizationMigrations(t *testing.T) string {
	t.Helper()
	source := "../../../migrations"
	entries, err := os.ReadDir(source)
	if err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".sql") {
			continue
		}
		prefix, _, ok := strings.Cut(name, "_")
		version, parseErr := strconv.Atoi(prefix)
		if !ok || parseErr != nil || version < 1 || version > 91 {
			continue
		}
		content, readErr := os.ReadFile(filepath.Join(source, name))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if err := os.WriteFile(filepath.Join(target, name), content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return target
}
