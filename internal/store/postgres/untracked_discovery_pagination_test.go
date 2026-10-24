package postgres_test

import (
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/google/uuid"
)

func TestUntrackedDiscoverySourceManifestPaginationPG(t *testing.T) {
	f := newUntrackedDiscoveryFixture(t)
	for range 65 {
		f.scope = uuid.New()
		f.save(t, f.in, "additional-source")
	}
	first, err := f.discovery.ListUntrackedDiscoveryScopes(t.Context(), "", 64)
	if err != nil || len(first.Scopes) != 64 || first.Complete || first.NextCursor == "" || first.ManifestAt.IsZero() {
		t.Fatal(first, err)
	}
	seen := make(map[uuid.UUID]bool, 66)
	for _, scope := range first.Scopes {
		seen[scope.ID] = true
	}
	f.scope = uuid.New()
	newScope := f.scope
	f.save(t, f.in, "after-manifest")
	second, err := f.discovery.ListUntrackedDiscoveryScopes(t.Context(), first.NextCursor, 64)
	if err != nil || !second.Complete || second.NextCursor != "" || len(second.Scopes) != 2 || !second.ManifestAt.Equal(first.ManifestAt) {
		t.Fatal(second, err)
	}
	for _, scope := range second.Scopes {
		if scope.ID == newScope || seen[scope.ID] {
			t.Fatal("duplicate or horizon moved", scope)
		}
		seen[scope.ID] = true
	}
	if len(seen) != 66 {
		t.Fatal(len(seen))
	}
	_, err = f.discovery.ListUntrackedDiscoveryScopes(t.Context(), first.NextCursor+"x", 64)
	checkinErr(t, err, ports.ErrRejected)
	// Invalid configurations still advance the bounded candidate cursor.
	f.exec(t, `UPDATE download_client SET remote_path_map='{}' WHERE id=$1`, f.client)
	invalid, err := f.discovery.ListUntrackedDiscoveryScopes(t.Context(), "", 64)
	if err != nil || len(invalid.Scopes) != 0 || invalid.Complete || invalid.NextCursor == "" {
		t.Fatal(invalid, err)
	}
	last, err := f.discovery.ListUntrackedDiscoveryScopes(t.Context(), invalid.NextCursor, 64)
	if err != nil || len(last.Scopes) != 0 || !last.Complete || !last.ManifestAt.Equal(invalid.ManifestAt) {
		t.Fatal(last, err)
	}
}

func TestUntrackedDiscoveryMigrationEmptyAndProtectedDownPG(t *testing.T) {
	t.Run("empty_roundtrip", func(t *testing.T) {
		f := newCheckinFixtureAt(t, repositoryMigrationsThrough(t, 103), 103)
		v, err := postgres.Rollback(t.Context(), f.dsn, repositoryMigrationsThrough(t, 103))
		if err != nil || v != 102 {
			t.Fatal(v, err)
		}
		v, err = postgres.Migrate(t.Context(), f.dsn, repositoryMigrationsThrough(t, 103))
		if err != nil || v != 103 {
			t.Fatal(v, err)
		}
	})
	for _, kind := range []string{"queued", "running", "observed"} {
		t.Run(kind, func(t *testing.T) {
			f := newUntrackedDiscoveryFixtureAt(t, 103)
			f.request(t)
			if kind != "queued" {
				c := f.claim(t)
				if kind == "observed" {
					if err := f.discovery.RecordUntrackedDiscovery(t.Context(), c, discoverySample()); err != nil {
						t.Fatal(err)
					}
					if err := f.discovery.CompleteUntrackedDiscovery(t.Context(), c); err != nil {
						t.Fatal(err)
					}
				}
			}
			before := discoveryBusinessFacts(t, f)
			if _, err := postgres.Rollback(t.Context(), f.dsn, repositoryMigrationsThrough(t, 103)); err == nil {
				t.Fatal("destructive downgrade accepted")
			}
			f.count(t, `SELECT count(*) FROM untracked_discovery_state`, 1)
			if kind == "observed" {
				f.count(t, `SELECT count(*) FROM untracked_discovered_file`, 1)
			}
			if discoveryBusinessFacts(t, f) != before {
				t.Fatal("schema102 history changed during rejected downgrade")
			}
		})
	}
}
