package app

import (
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/domain/naming"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/outbox"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	transferstore "github.com/dalamudx/PulseMedia/internal/store/transfer"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func reviseFrozenMovieDirectory(t *testing.T, ctx context.Context, f *organizationFixture) {
	t.Helper()
	payload := []byte(strings.Repeat("v", 200))
	source := filepath.Join(f.target, "release", "movie.mkv")
	if e := os.MkdirAll(filepath.Dir(source), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(source, payload, 0600); e != nil {
		t.Fatal(e)
	}
	sidecar := filepath.Join(filepath.Dir(source), "subtitle.srt")
	if e := os.WriteFile(sidecar, []byte("subtitle"), 0600); e != nil {
		t.Fatal(e)
	}
	who, _, e := auth.NewService(postgres.NewAuthenticationStore(f.pool), crypto.AuthenticationSecrets{}, clock.New(), f.gen).SetupAdmin(ctx, "directory-owner", "DirectoryFixtureOwned123!")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.pool.Exec(ctx, "UPDATE download_requirement SET actor_id=$2 WHERE operation_id=$1", f.op, who.ID.String()); e != nil {
		t.Fatal(e)
	}
	inputs, e := f.q.GetDownloadOrganizationInputs(ctx, f.taskID)
	if e != nil || len(inputs) != 1 {
		t.Fatal(inputs, e)
	}
	var profile quality.Profile
	if e = json.Unmarshal(inputs[0].QualityProfile, &profile); e != nil {
		t.Fatal(e)
	}
	originalRel := filepath.Join(naming.FormatMovieDir(inputs[0].Title, int(inputs[0].Year.Int32)), naming.FormatMovieFile(inputs[0].Title, int(inputs[0].Year.Int32), profile, ".mkv"))
	blocker := filepath.Join(f.target, originalRel)
	if e = os.MkdirAll(filepath.Dir(blocker), 0700); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(blocker, []byte("protected unrelated target"), 0600); e != nil {
		t.Fatal(e)
	}
	job := f.claim(t, "directory-original")
	if e = f.svc.OrganizeDownload(ctx, job); e != nil {
		t.Fatal(e)
	}
	task, e := f.q.GetDownloadTaskByOpID(ctx, f.op)
	if e != nil || task.OrganizationState != "review_required" || !task.TransferPlanID.Valid {
		t.Fatal("directory conflict not review", task, e)
	}
	old, e := f.q.GetTransferPlanByID(ctx, task.TransferPlanID.Bytes)
	if e != nil {
		t.Fatal(e)
	}
	oldSteps, e := f.q.ListStepsForPlan(ctx, old.ID)
	if e != nil || len(oldSteps) != 1 {
		t.Fatal(e)
	}
	proof, e := f.q.GetDownloadFileInventory(ctx, f.taskID)
	if e != nil {
		t.Fatal(e)
	}
	if oldSteps[0].TargetRel.String != originalRel {
		t.Fatal("blocker did not cover real target")
	}
	bytes, e := os.ReadFile(blocker)
	if e != nil || string(bytes) != "protected unrelated target" {
		t.Fatal("target overwritten", e)
	}
	// The original worker has returned and released physical ownership; seed its historical expiry only.
	if _, e = f.pool.Exec(ctx, "UPDATE transfer_plan SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1", old.ID); e != nil {
		t.Fatal(e)
	}
	old, e = f.q.GetTransferPlanByID(ctx, old.ID)
	if e != nil {
		t.Fatal(e)
	}
	repo := transferstore.NewRepository(f.pool, f.gen, outbox.New(f.pool, f.gen, clock.New()), rootfs.NewFilesystem())
	actor := &ports.Actor{Type: ports.ActorTypeUser, ID: who.ID.String(), Role: ports.RoleAdmin}
	next := "Edited/Directory movie.mkv"
	p, e := repo.PreviewTransferRevision(ctx, actor, old.ID, next)
	if e != nil || p.Revisable {
		t.Fatal("existing old target allowed", p, e)
	}
	if e = os.Remove(blocker); e != nil {
		t.Fatal(e)
	}
	p, e = repo.PreviewTransferRevision(ctx, actor, old.ID, next)
	if e != nil || !p.Revisable {
		t.Fatal("directory preview", p, e)
	}
	command := ports.TransferRevisionCommand{PlanID: old.ID, TargetRel: next, ExpectedInputStamp: p.InputStamp, Confirmed: true, Reason: "修订唯一目录视频目标，保留原清单"}
	r, e := repo.CommitTransferRevision(ctx, actor, command, "directory-revision")
	if e != nil {
		t.Fatal(e)
	}
	again, e := repo.CommitTransferRevision(ctx, actor, command, "directory-revision")
	if e != nil || *again != *r {
		t.Fatal("directory lost receipt", e)
	}
	newSteps, e := f.q.ListStepsForPlan(ctx, r.PlanID)
	if e != nil || len(newSteps) != 1 || !reflect.DeepEqual(newSteps[0].TargetEvidence, oldSteps[0].TargetEvidence) {
		t.Fatal("directory proof re-signed", e)
	}
	job = f.claim(t, "directory-revised")
	if e = f.svc.OrganizeDownload(ctx, job); e != nil {
		t.Fatal(e)
	}
	original, _ := os.Stat(source)
	target, e := os.Stat(filepath.Join(f.target, next))
	if e != nil || !os.SameFile(original, target) || f.count(t, "unit_fact") != 1 {
		t.Fatal("directory revision hardlink", e)
	}
	after, e := f.q.GetDownloadFileInventory(ctx, f.taskID)
	if e != nil || !reflect.DeepEqual(after, proof) {
		t.Fatal("original inventory rewritten", e)
	}
	afterPlan, e := f.q.GetTransferPlanByID(ctx, old.ID)
	afterSteps, e2 := f.q.ListStepsForPlan(ctx, old.ID)
	if e != nil || e2 != nil || afterPlan != old || !reflect.DeepEqual(afterSteps, oldSteps) {
		t.Fatal("old directory plan rewritten", e, e2)
	}
	bytes, e = os.ReadFile(sidecar)
	if e != nil || string(bytes) != "subtitle" {
		t.Fatal("sidecar changed", e)
	}
}
