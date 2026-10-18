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

func TestAutomaticSharedRoundHasNoNewNetworkExecution(t *testing.T) {
	f, svc, command, rootB := globalCommandFixture(t)
	ctx := context.Background()
	first, e := svc.AcquireMovie(ctx, f.actor, command)
	if e != nil {
		t.Fatal(e)
	}
	ids := idgen.New()
	subService := newTestSubscriptionService(f.pool, ids)
	sub, wanted, e := subService.CreateSubscription(ctx, f.actor, subscription.CreateSubscriptionCommand{MediaID: func() uuid.UUID {
		unit, e := f.q.GetSearchUnit(ctx, command.UnitIDs[0])
		if e != nil {
			t.Fatal(e)
		}
		return unit.MediaID
	}(), RootID: rootB, ScopeType: "all", IndexerIDs: []uuid.UUID{f.idx}, DownloaderID: &f.downloader})
	if e != nil || len(wanted) != 1 {
		t.Fatal(sub, wanted, e)
	}
	report, e := subscription.NewStatusService(postgres.NewSubscriptionStatusStore(f.pool)).Read(ctx, f.actor, sub.ID)
	if e != nil || report.Items[0].WaitReason != "shared_acquisition_available" || !report.Items[0].ReservationProtected || report.Items[0].IsSatisfied {
		t.Fatal("foreign root global protection not explained", report, e)
	}
	var before int
	if e = f.pool.QueryRow(ctx, "SELECT count(*) FROM indexer_query").Scan(&before); e != nil {
		t.Fatal(e)
	}
	round, e := f.coord.StartSharedRound(ctx, wanted[0].ID)
	if e != nil {
		t.Fatal(e)
	}
	job := f.finalJob(t, round)
	result, e := f.coord.FinalizeLeasedRound(ctx, round.ID, authority(job))
	if e != nil || result.State != "completed" || result.OperationID != first.OperationID {
		t.Fatal("shared decision did not attach", result, e)
	}
	var after, downloads, demands int
	if e = f.pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM indexer_query),(SELECT count(*) FROM job WHERE kind='download_submission'),(SELECT count(*) FROM download_requirement WHERE operation_id=$1 AND state='pending')", first.OperationID).Scan(&after, &downloads, &demands); e != nil || before != after || downloads != 1 || demands != 2 {
		t.Fatal("automatic share generated query or physical job", before, after, downloads, demands, e)
	}
	report, e = subscription.NewStatusService(postgres.NewSubscriptionStatusStore(f.pool)).Read(ctx, f.actor, sub.ID)
	if e != nil || report.Items[0].IsSatisfied || report.Items[0].CurrentFactStage != "" {
		t.Fatal("attached root falsely completed or submitted", report, e)
	}
	_ = ports.RoleMember
}
