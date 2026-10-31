package app

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/outbox"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	transferstore "github.com/dalamudx/PulseMedia/internal/store/transfer"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	usecase "github.com/dalamudx/PulseMedia/internal/usecase/transfer"
	"github.com/jackc/pgx/v5/pgtype"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func revisionSetup(t *testing.T) (*organizationFixture, *usecase.RevisionService, *ports.Actor, ports.TransferPlan) {
	t.Helper()
	ctx := context.Background()
	f := organizationSetup(t)
	who, _, e := auth.NewService(postgres.NewAuthenticationStore(f.pool), crypto.AuthenticationSecrets{}, clock.New(), f.gen).SetupAdmin(ctx, "revision-owner", "RevisionFixtureOwned123!")
	if e != nil {
		t.Fatal(e)
	}
	user := who.ID
	if _, e := f.pool.Exec(ctx, "UPDATE download_requirement SET actor_id=$2 WHERE operation_id=$1", f.op, user.String()); e != nil {
		t.Fatal(e)
	}
	f.observe(t)
	repo := transferstore.NewRepository(f.pool, f.gen, outbox.New(f.pool, f.gen, clock.New()), rootfs.NewFilesystem(), usecase.ExecutePreparedPlan)
	made, e := repo.PrepareMovieTransfer(ctx, ports.MovieTransferRequest{OperationID: f.op, RootID: f.root, UnitID: f.unit, SourcePath: f.path, Title: "Movie", Year: 2020, Quality: quality.Profile{Resolution: quality.Res1080p}})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.q.AttachDownloadTransferPlan(ctx, db.AttachDownloadTransferPlanParams{ID: f.taskID, TransferPlanID: pgtype.UUID{Bytes: made.Plan.ID, Valid: true}}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.q.ReviewDownloadOrganization(ctx, db.ReviewDownloadOrganizationParams{ID: f.taskID, OrganizationReason: pgtype.Text{String: "fixture review", Valid: true}}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.q.UpdatePlanState(ctx, db.UpdatePlanStateParams{ID: made.Plan.ID, State: "review_required"}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.pool.Exec(ctx, "UPDATE job SET state='dead' WHERE kind='download_organization' AND payload->>'task_id'=$1", f.taskID.String()); e != nil {
		t.Fatal(e)
	}
	return f, usecase.NewRevisionService(repo), &ports.Actor{Type: ports.ActorTypeUser, ID: user.String(), Role: ports.RoleAdmin}, made.Plan
}
func TestManualMovieRevisionAtomicSuccessor(t *testing.T) {
	ctx := context.Background()
	f, s, a, old := revisionSetup(t)
	original, _ := f.q.GetTransferPlanByID(ctx, old.ID)
	steps, _ := f.q.ListStepsForPlan(ctx, old.ID)
	p, e := s.Preview(ctx, a, old.ID, "Edited/Movie revised.mkv")
	if e != nil || !p.Revisable {
		t.Fatalf("preview %+v %v", p, e)
	}
	c := ports.TransferRevisionCommand{PlanID: old.ID, TargetRel: p.TargetRel, ExpectedInputStamp: p.InputStamp, Confirmed: true, Reason: "Correct target name"}
	var wg sync.WaitGroup
	out := make(chan *ports.TransferRevisionReceipt, 8)
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); r, e := s.Commit(ctx, a, c, "same-revision"); out <- r; errs <- e }()
	}
	wg.Wait()
	close(out)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var first *ports.TransferRevisionReceipt
	for r := range out {
		if first == nil {
			first = r
		} else if *first != *r {
			t.Fatal("different receipts")
		}
	}
	after, _ := f.q.GetTransferPlanByID(ctx, old.ID)
	afterSteps, _ := f.q.ListStepsForPlan(ctx, old.ID)
	if after != original || !reflect.DeepEqual(afterSteps, steps) {
		t.Fatal("original rewritten")
	}
	if first.PlanID == old.ID || f.count(t, "transfer_plan") != 2 || f.count(t, "unit_fact") != 0 {
		t.Fatal("revision executed or duplicate plans")
	}
	active, e := f.q.GetTransferPlanByOpID(ctx, f.op)
	if e != nil || active.ID != first.PlanID {
		t.Fatal("active plan", e)
	}
	job := f.claim(t, "revision-worker")
	// A valid task lease cannot borrow another successor plan's scope.
	badPayload, _ := json.Marshal(map[string]any{"task_id": first.TaskID, "revision_plan_id": old.ID})
	if _, e = f.pool.Exec(ctx, "UPDATE job SET payload=$2 WHERE id=$1", job.ID, badPayload); e != nil {
		t.Fatal(e)
	}
	if e = f.svc.OrganizeDownload(ctx, job); !errors.Is(e, transferstore.ErrExecutionRejected) {
		t.Fatal("borrowed revision scope", e)
	}
	if f.count(t, "unit_fact") != 0 {
		t.Fatal("wrong plan imported")
	}
	goodPayload, _ := json.Marshal(map[string]any{"task_id": first.TaskID, "revision_plan_id": first.PlanID})
	if _, e = f.pool.Exec(ctx, "UPDATE job SET payload=$2 WHERE id=$1", job.ID, goodPayload); e != nil {
		t.Fatal(e)
	}
	if e = f.svc.OrganizeDownload(ctx, job); e != nil {
		t.Fatal(e)
	}
	src, _ := os.Stat(f.path)
	dst, e := os.Stat(filepath.Join(f.target, p.TargetRel))
	if e != nil || !os.SameFile(src, dst) || f.count(t, "unit_fact") != 1 {
		t.Fatal("revised hardlink/import", e)
	}
	again, e := s.Commit(ctx, a, c, "same-revision")
	if e != nil || *again != *first {
		t.Fatal("lost receipt recovery", e)
	}
	c.TargetRel = "Other/Movie.mkv"
	if _, e = s.Commit(ctx, a, c, "same-revision"); e == nil {
		t.Fatal("key reused for different path")
	}
}
