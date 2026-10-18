package postgres_test

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"testing"
)

func frozenProjectRevision(t *testing.T, f *frozenUpgradeFixture, revision int64) {
	t.Helper()
	ctx := context.Background()
	rows, e := db.New(f.p).ListWantedItemsForSubscription(ctx, f.sub)
	if e != nil {
		t.Fatal(e)
	}
	for _, w := range rows {
		if _, e = subscription.NewEvaluator(postgres.NewWantedProjectionStore(f.p)).EvaluateWantedItem(ctx, w.ID, quality.Policy{ID: "frozen-upgrade", Revision: revision}); e != nil {
			t.Fatal(e)
		}
	}
}
