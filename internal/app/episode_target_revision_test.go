package app

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	transferstore "github.com/dalamudx/PulseMedia/internal/store/transfer"
	"github.com/google/uuid"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestAppRunSameEpisodeTargetRevision(t *testing.T) {
	for _, mapped := range []bool{false, true} {
		name := "standard"
		if mapped {
			name = "mapped"
		}
		t.Run(name, func(t *testing.T) {
			runReviewedEpisodeFullPipeline(t, mapped, episodePipelineProbe{manualRevision: true})
		})
	}
}

// The original runtime has joined. Seed a historical expired review and remove
// only its test-owned, verified hard link; production still requires expiry.
func reviseHistoricalEpisodeTarget(t *testing.T, ctx context.Context, f *organizationFixture, repo *transferstore.Repository, actor *ports.Actor, planID uuid.UUID, browser bool) string {
	t.Helper()
	q := db.New(f.pool)
	steps, e := q.ListStepsForPlan(ctx, planID)
	if e != nil || len(steps) != 1 {
		t.Fatal("old steps", e)
	}
	old := steps[0]
	target := filepath.Join(f.target, old.TargetRel.String)
	src, e := os.Stat(f.path)
	if e != nil {
		t.Fatal(e)
	}
	dst, e := os.Stat(target)
	if e != nil || !os.SameFile(src, dst) {
		t.Fatal("expected owned original hardlink", e)
	}
	if e = os.Remove(target); e != nil {
		t.Fatal(e)
	}
	if _, e = f.pool.Exec(ctx, "UPDATE transfer_plan SET state='review_required',lease_until=clock_timestamp()-interval '1 second' WHERE id=$1", planID); e != nil {
		t.Fatal(e)
	}
	original, e := q.GetTransferPlanByID(ctx, planID)
	if e != nil {
		t.Fatal(e)
	}
	next := "Fixture (2020)/Season 01/Fixture.S01E01.revised.mkv"
	for _, bad := range []string{"Fixture.S01E02.mkv", "Fixture.S01E01E02.mkv", "[Team] Fixture - 25 [1080p].mkv"} {
		p, e := repo.PreviewTransferRevision(ctx, actor, planID, bad)
		if e == nil && p.Revisable {
			t.Fatal("wrong canonical coverage allowed", bad)
		}
	}
	p, e := repo.PreviewTransferRevision(ctx, actor, planID, next)
	if e != nil || !p.Revisable || p.MediaType != "tv" || p.UnitKey != "S01E01" {
		t.Fatalf("episode revision preview %+v %v", p, e)
	}
	c := ports.TransferRevisionCommand{PlanID: planID, TargetRel: next, ExpectedInputStamp: p.InputStamp, Confirmed: true, Reason: "修订原单集目标，保留冻结编号与来源"}
	var candidate uuid.UUID
	var title string
	if e = f.pool.QueryRow(ctx, "SELECT c.id,c.title FROM release_candidate c JOIN download_task t ON t.candidate_id=c.id WHERE t.operation_id=$1", original.OperationID).Scan(&candidate, &title); e != nil {
		t.Fatal(e)
	}
	if _, e = f.pool.Exec(ctx, "UPDATE release_candidate SET title='Changed.source.S01E01' WHERE id=$1", candidate); e != nil {
		t.Fatal(e)
	}
	if _, e = repo.CommitTransferRevision(ctx, actor, c, "episode-revision"); !errors.Is(e, ports.ErrTransferReviewRequired) {
		t.Fatal("source version accepted", e)
	}
	if _, e = f.pool.Exec(ctx, "UPDATE release_candidate SET title=$2 WHERE id=$1", candidate, title); e != nil {
		t.Fatal(e)
	}
	p, e = repo.PreviewTransferRevision(ctx, actor, planID, next)
	if e != nil || !p.Revisable {
		t.Fatal("valid recovery", p, e)
	}
	c.ExpectedInputStamp = p.InputStamp
	key := "episode-revision"
	var r *ports.TransferRevisionReceipt
	if browser {
		r, c, key = runEpisodeRevisionCookieBrowser(t, ctx, f, repo, actor, planID, next)
	} else {
		r, e = repo.CommitTransferRevision(ctx, actor, c, key)
		if e != nil {
			t.Fatal("episode successor", e)
		}
	}
	again, e := repo.CommitTransferRevision(ctx, actor, c, key)
	if e != nil || *again != *r {
		t.Fatal("lost receipt", e)
	}
	after, e := q.GetTransferPlanByID(ctx, planID)
	afterSteps, se := q.ListStepsForPlan(ctx, planID)
	if e != nil || se != nil || after != original || !reflect.DeepEqual(steps, afterSteps) {
		t.Fatal("old episode plan rewritten", e, se)
	}
	newSteps, e := q.ListStepsForPlan(ctx, r.PlanID)
	if e != nil || len(newSteps) != 1 || !reflect.DeepEqual(newSteps[0].TargetEvidence, old.TargetEvidence) || !reflect.DeepEqual(newSteps[0].QualityProfile, old.QualityProfile) {
		t.Fatal("episode evidence re-signed", e)
	}
	return next
}
