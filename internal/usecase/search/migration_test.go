package search_test

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"testing"
	"time"
)

func TestSearchMigrationQuarantinesLegacyRound(t *testing.T) {
	f := runtimeFixture(t)
	ctx := context.Background()
	round, err := f.coord.StartRound(ctx, f.wanted, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	dsn := f.pool.Config().ConnConfig.ConnString()
	for expected := int64(postgres.CurrentMigrationVersion - 1); expected >= 18; expected-- {
		if version, err := postgres.Rollback(ctx, dsn, migrationsDir); err != nil || version != expected {
			t.Fatal(version, err)
		}
	}
	// schema18 has no frozen snapshot/query evidence: migration must not infer it.
	if version, err := postgres.Migrate(ctx, dsn, migrationsDir); err != nil || version != postgres.CurrentMigrationVersion {
		t.Fatal(version, err)
	}
	saved, err := f.q.GetSearchRoundByID(ctx, round.ID)
	if err != nil || saved.State != "aborted" || saved.AbortReason.String != "legacy snapshot missing" {
		t.Fatal(saved, err)
	}
	var jobs int
	if err = f.pool.QueryRow(ctx, "SELECT count(*) FROM job WHERE kind='download_submission'").Scan(&jobs); err != nil || jobs != 0 {
		t.Fatal("migration replayed acquisition", jobs, err)
	}
}
