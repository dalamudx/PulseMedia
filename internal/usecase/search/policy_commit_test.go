package search_test

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/policy"
	"github.com/dalamudx/PulseMedia/internal/usecase/search"
	"github.com/google/uuid"
	"testing"
)

func TestAcquireAllowedZeroScoreIgnoresUpgradeCutoff(t *testing.T) {
	f := runtimeFixture(t)
	ctx := context.Background()
	svc := policy.NewService(postgres.NewPolicyStore(f.pool, idgen.New()))
	p, e := svc.Read(ctx, "default", 0)
	if e != nil {
		t.Fatal(e)
	}
	rules := quality.PolicyRules{ResolutionOrder: map[quality.Resolution]int{quality.Res1080p: 0}, SourceOrder: map[quality.Source]int{quality.SourceBluray: 0}, CutoffScore: 200015}
	_, e = svc.Command(ctx, f.actor, ports.PolicyCommand{Kind: "publish", ID: "default", ExpectedRevision: p.Revision, Rules: &rules}, "zero-rank")
	if e != nil {
		t.Fatal(e)
	}
	round := f.collect(t)
	job := f.finalJob(t, round)
	result, e := f.coord.FinalizeLeasedRound(ctx, round.ID, authority(job))
	if e != nil || result.State != "completed" || result.OperationID == uuid.Nil {
		t.Fatal("allowed acquire incorrectly required upgrade cutoff or positive rank", result, e)
	}
}

func TestPolicyPublicationFencesCollectedRound(t *testing.T) {
	f := runtimeFixture(t)
	round := f.collect(t)
	job := f.finalJob(t, round)
	ctx := context.Background()
	svc := policy.NewService(postgres.NewPolicyStore(f.pool, idgen.New()))
	p, e := svc.Read(ctx, "default", 0)
	if e != nil {
		t.Fatal(e)
	}
	rules := quality.PolicyRules{ResolutionOrder: p.Rules.ResolutionOrder, SourceOrder: p.Rules.SourceOrder, CutoffScore: 0, RejectTerms: []string{"blocked"}}
	admin := f.actor
	receipt, e := svc.Command(ctx, admin, ports.PolicyCommand{Kind: "publish", ID: "default", ExpectedRevision: p.Revision, Rules: &rules}, "publish-fence")
	if e != nil || receipt.Revision != p.Revision+1 {
		t.Fatal(receipt, e)
	}
	updated, e := f.q.GetSubscriptionByID(ctx, f.sub)
	if e != nil || updated.IntentVersion <= round.SnapshotIntentVersion || updated.QualityPolicyRevision != receipt.Revision {
		t.Fatal("policy publication failed to update intent/ref", updated, e)
	}
	result, e := f.coord.FinalizeLeasedRound(ctx, round.ID, authority(job))
	if !errors.Is(e, search.ErrRoundAborted) || result.State != "aborted" {
		t.Fatal("published policy did not fence old evidence", result, e)
	}
	var count int
	if e = f.pool.QueryRow(ctx, "SELECT count(*) FROM download_task").Scan(&count); e != nil || count != 0 {
		t.Fatal("old policy acquired", count, e)
	}
	frozen, e := svc.GetRevision(ctx, "default", p.Revision)
	if e != nil || len(frozen.RejectTerms) != len(p.Rules.RejectTerms) {
		t.Fatal("publication mutated frozen revision", e)
	}
}
