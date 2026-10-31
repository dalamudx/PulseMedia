package postgres_test

import (
	"encoding/json"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/store/postgres"
)

func TestUntrackedReviewCanonicalAbsoluteMigrationPG(t *testing.T) {
	history := repositoryMigrationsThrough(t, 138)
	t.Run("upgrade_empty_downgrade_reupgrade", func(t *testing.T) {
		f := newCheckinFixtureAt(t, repositoryMigrationsThrough(t, 137), 137)
		if v, err := postgres.Migrate(t.Context(), f.dsn, history); err != nil || v != 138 {
			t.Fatal(v, err)
		}
		if v, err := postgres.Rollback(t.Context(), f.dsn, history); err != nil || v != 137 {
			t.Fatal(v, err)
		}
		if v, err := postgres.Migrate(t.Context(), f.dsn, history); err != nil || v != 138 {
			t.Fatal(v, err)
		}
	})
	t.Run("standard_history_preserved", func(t *testing.T) {
		f := newOrganizationFixture(t, "hardlink")
		canonicalAbsoluteMigrationAt138(t, f.dsn)
		receipt := f.commit(t, "standard-history")
		if v, err := postgres.Rollback(t.Context(), f.dsn, history); err != nil || v != 137 {
			t.Fatal(v, err)
		}
		if v, err := postgres.Migrate(t.Context(), f.dsn, history); err != nil || v != 138 {
			t.Fatal(v, err)
		}
		encoded, err := json.Marshal(receipt)
		if err != nil {
			t.Fatal(err)
		}
		f.count(t, `SELECT count(*) FROM untracked_organization_operation WHERE id=$1 AND receipt=$2::jsonb`, 1, receipt.Operation.ID, encoded)
	})
	t.Run("absolute_history_blocks_downgrade", func(t *testing.T) {
		f := absoluteCanonicalReviewFixture(t)
		canonicalAbsoluteMigrationAt138(t, f.dsn)
		org := organizationConfirm(t, f, canonicalReviewEvidence(), "hardlink")
		receipt := org.commit(t, "absolute-history")
		if _, err := postgres.Rollback(t.Context(), f.dsn, history); err == nil {
			t.Fatal("incompatible absolute-number organization history was downgraded")
		}
		if v, err := postgres.Migrate(t.Context(), f.dsn, history); err != nil || v != 138 {
			t.Fatal("failed downgrade changed schema", v, err)
		}
		encoded, err := json.Marshal(receipt)
		if err != nil {
			t.Fatal(err)
		}
		f.count(t, `SELECT count(*) FROM untracked_organization_operation WHERE id=$1 AND receipt=$2::jsonb`, 1, receipt.Operation.ID, encoded)
	})
}

func canonicalAbsoluteMigrationAt138(t *testing.T, dsn string) {
	t.Helper()
	// Exercise schema138's historical guard, independently of later additions.
	for version := int64(postgres.CurrentMigrationVersion); version > 138; version-- {
		if got, err := postgres.Rollback(t.Context(), dsn, "../../../migrations"); err != nil || got != version-1 {
			t.Fatalf("prepare historical schema138 from %d: got=%d err=%v", version, got, err)
		}
	}
}
