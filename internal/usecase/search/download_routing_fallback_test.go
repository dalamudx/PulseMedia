package search_test

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/google/uuid"
	"testing"
)

func TestMovieRoutingExplicitSingleClientFallbackPG(t *testing.T) {
	f := runtimeFixture(t)
	ctx := context.Background()
	before, err := f.q.GetSubscriptionByID(ctx, f.sub)
	if err != nil {
		t.Fatal(err)
	}
	svc := subscription.NewBindingService(postgres.NewSubscriptionBindingStore(f.pool, idgen.New()))
	if _, err = svc.Configure(ctx, f.actor, f.sub, ports.SubscriptionBindingsCommand{ExpectedVersion: before.IntentVersion, IndexerIDs: []uuid.UUID{f.idx}}, "single-route"); err != nil {
		t.Fatal(err)
	}
	f.coord = routedMovieCoordinator(f, nil)
	round := f.collect(t)
	job := f.finalJob(t, round)
	result, err := f.coord.FinalizeLeasedRound(ctx, round.ID, authority(job))
	if err != nil || result == nil || result.State != "completed" {
		t.Fatal("single-client fallback could not reserve", err)
	}
	var selected uuid.UUID
	if err = f.pool.QueryRow(ctx, `SELECT (payload->>'client_id')::uuid FROM job WHERE kind='download_submission' AND payload->>'operation_id'=$1`, result.OperationID.String()).Scan(&selected); err != nil || selected != f.downloader {
		t.Fatal("single client was not frozen into the send job", err)
	}
}
