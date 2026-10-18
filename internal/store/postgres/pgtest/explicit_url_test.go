package pgtest

import (
	"os"
	"strings"
	"testing"
)

func TestMissingTestDatabaseURLNeverStartsSharedCluster(t *testing.T) {
	for _, value := range []string{"", " \t ", "http://localhost/not-a-database", "postgres://sensitive-credential@"} {
		t.Run(value[:min(len(value), 4)], func(t *testing.T) {
			t.Setenv("PULSE_TEST_DATABASE_URL", value)
			cluster, err := initCluster()
			if err == nil || cluster != nil {
				t.Fatal("invalid test configuration accepted")
			}
			if strings.Contains(err.Error(), "sensitive-credential") {
				t.Fatal("configuration value leaked")
			}
		})
	}
	if path := os.Getenv("PULSE_OWNED_HELPER_PROBE"); path != "" {
		entries, err := os.ReadDir(path)
		if err != nil || len(entries) != 0 {
			t.Fatal("fixture wrote resources without an explicit database")
		}
	}
}
