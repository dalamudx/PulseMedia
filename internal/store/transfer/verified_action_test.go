package transfer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/outbox"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	transfercase "github.com/dalamudx/PulseMedia/internal/usecase/transfer"
	"github.com/google/uuid"
)

// Freeze the same persisted authorization as the public successor transaction;
// use actual roots, account, requirement and PostgreSQL triggers for execution.
func verifiedFixture(t *testing.T, action string, prepare ...func(*safetyFixture)) (*safetyFixture, db.TransferPlan, db.TransferStep, uuid.UUID) {
	t.Helper()
	f := safetySetup(t)
	for _, setup := range prepare {
		setup(f)
	}
	ctx := t.Context()
	prior, source := f.plan(t)
	actor := uuid.New()
	if _, err := f.q.CreateUser(ctx, db.CreateUserParams{ID: actor, Username: "verified-admin", PasswordHash: "unused-password-hash", Role: "admin", IsActive: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, "UPDATE library_root SET baseline_completed=true WHERE id=$1", f.root); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, "INSERT INTO download_requirement(id,operation_id,unit_id,root_id,actor_type,actor_id) VALUES($1,$2,$3,$4,'user',$5)", uuid.New(), prior.OperationID, f.unit, f.root, actor.String()); err != nil {
		t.Fatal(err)
	}
	root, err := f.svc.files.OpenRoot(ctx, f.source)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := root.Identity(ctx)
	root.Close()
	if err != nil {
		t.Fatal(err)
	}
	var evidence transferTargetEvidence
	if err = json.Unmarshal(source.TargetEvidence, &evidence); err != nil {
		t.Fatal(err)
	}
	evidence.FileAction = &fileActionEvidence{Version: 1, ActorID: actor, SourceRootDevice: identity.Device, SourceRootInode: identity.Inode}
	raw, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	p, err := f.q.CreateTransferPlan(ctx, db.CreateTransferPlanParams{ID: uuid.New(), OperationID: prior.OperationID, RootID: prior.RootID, State: "approved", Strategy: action, RequiresApproval: true})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.q.FreezeTransferPlan(ctx, db.FreezeTransferPlanParams{ID: p.ID, RootPath: prior.RootPath, RootGeneration: prior.RootGeneration, RootDevice: prior.RootDevice, RootInode: prior.RootInode, OldFileID: prior.OldFileID}); err != nil {
		t.Fatal(err)
	}
	step, err := f.q.CreateTransferStep(ctx, db.CreateTransferStepParams{ID: uuid.New(), PlanID: p.ID, UnitID: source.UnitID, SourcePath: source.SourcePath, TargetPath: source.TargetPath, Action: action, StepOrder: 1, SourceSize: source.SourceSize, SourceMtime: source.SourceMtime})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.q.FreezeTransferStep(ctx, db.FreezeTransferStepParams{ID: step.ID, SourceRoot: source.SourceRoot, SourceRel: source.SourceRel, TargetRel: source.TargetRel, SourceDevice: source.SourceDevice, SourceInode: source.SourceInode, QualityProfile: source.QualityProfile, TargetEvidence: raw}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(ctx, "UPDATE transfer_plan SET state='review_required' WHERE id=$1", prior.ID); err != nil {
		t.Fatal(err)
	}
	p, err = f.q.GetTransferPlanByID(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	steps, err := f.q.ListStepsForPlan(ctx, p.ID)
	if err != nil || len(steps) != 1 {
		t.Fatal(err, steps)
	}
	return f, p, steps[0], actor
}

func checkVerifiedImport(t *testing.T, f *safetyFixture, p db.TransferPlan, step db.TransferStep, action string) {
	t.Helper()
	f.oneImport(t, p.ID)
	ctx := t.Context()
	root, err := f.svc.files.OpenRoot(ctx, f.target)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	actual, err := root.Inspect(ctx, step.TargetRel.String)
	if err != nil {
		t.Fatal(err)
	}
	file, err := f.q.GetActiveFileByPath(ctx, db.GetActiveFileByPathParams{RootID: f.root, RelPath: step.TargetRel.String})
	expected := fmt.Sprintf("stat.v1:%d:%d:%d", actual.Inode, actual.Size, actual.Mtime)
	if err != nil || file.SizeBytes != actual.Size || file.MtimeNs != actual.Mtime || file.FileFingerprint.String != expected {
		t.Fatalf("destination identity not indexed: %+v %+v %v", file, actual, err)
	}
	bytes, err := os.ReadFile(step.TargetPath)
	if err != nil || string(bytes) != "frozen video bytes" {
		t.Fatalf("destination content %q %v", bytes, err)
	}
	if action == "copy" && actual.Inode == step.SourceInode.Int64 {
		t.Fatal("copy reused source inode")
	}
	_, err = os.Stat(f.sourcePath)
	if action == "move" && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("move source remains: %v", err)
	}
	if action != "move" && err != nil {
		t.Fatal("copy source removed", err)
	}
	steps, err := f.q.ListStepsForPlan(ctx, p.ID)
	if err != nil || len(steps) != 1 {
		t.Fatal(err)
	}
	proof, err := loadFileAction(ctx, f.q, p, steps[0])
	if err != nil || proof == nil || proof.Content == nil {
		t.Fatal("sealed content missing", err)
	}
	if proof.Content.File != frozen(step) {
		t.Fatal("content bound to another source")
	}
}

func TestVerifiedActionCopyAndMovePG(t *testing.T) {
	for _, action := range []string{"copy", "move"} {
		t.Run(action, func(t *testing.T) {
			f, p, step, _ := verifiedFixture(t, action)
			seed := filepath.Join(f.source, "seeding.mkv")
			if err := os.Link(f.sourcePath, seed); err != nil {
				t.Fatal(err)
			}
			if err := f.svc.ExecutePlan(t.Context(), p.ID); err != nil {
				t.Fatal(err)
			}
			checkVerifiedImport(t, f, p, step, action)
			if b, err := os.ReadFile(seed); err != nil || string(b) != "frozen video bytes" {
				t.Fatalf("seed changed %q %v", b, err)
			}
			if err := f.svc.ExecutePlan(t.Context(), p.ID); err != nil {
				t.Fatal("same plan replay", err)
			}
			f.oneImport(t, p.ID)
		})
	}
}

func TestVerifiedActionImportRollbackAndRestartPG(t *testing.T) {
	for _, action := range []string{"copy", "move"} {
		t.Run(action, func(t *testing.T) {
			f, p, step, _ := verifiedFixture(t, action)
			ctx := t.Context()
			if _, err := f.pool.Exec(ctx, "CREATE FUNCTION reject_verified_import() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected import failure'; END $$; CREATE TRIGGER reject_verified_import BEFORE INSERT ON library_file_unit FOR EACH ROW EXECUTE FUNCTION reject_verified_import();"); err != nil {
				t.Fatal(err)
			}
			if err := f.svc.ExecutePlan(ctx, p.ID); err == nil {
				t.Fatal("injected failure succeeded")
			}
			f.noImport(t)
			if _, err := os.Stat(step.TargetPath); err != nil {
				t.Fatal("no physical result to recover", err)
			}
			if _, err := f.pool.Exec(ctx, "DROP TRIGGER reject_verified_import ON library_file_unit"); err != nil {
				t.Fatal(err)
			}
			if _, err := f.pool.Exec(ctx, "UPDATE transfer_plan SET lease_until=statement_timestamp()-interval '1 second' WHERE id=$1", p.ID); err != nil {
				t.Fatal(err)
			}
			gen := idgen.New()
			restarted := NewService(f.pool, gen, outbox.New(f.pool, gen, clock.New()), rootfs.NewFilesystem(), transfercase.ExecutePreparedPlan)
			if err := restarted.ExecutePlan(ctx, p.ID); err != nil {
				t.Fatalf("restart failed to observe %s: %v", action, err)
			}
			checkVerifiedImport(t, f, p, step, action)
		})
	}
}

func TestVerifiedActionRevokedActorNeverPublishesPG(t *testing.T) {
	f, p, step, actor := verifiedFixture(t, "move")
	if _, err := f.pool.Exec(t.Context(), "UPDATE app_user SET is_active=false WHERE id=$1", actor); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.ExecutePlan(t.Context(), p.ID); err == nil {
		t.Fatal("disabled actor authorized move")
	}
	f.noImport(t)
	if _, err := os.Stat(step.TargetPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("destination published", err)
	}
	if b, err := os.ReadFile(f.sourcePath); err != nil || string(b) != "frozen video bytes" {
		t.Fatal("source changed", err)
	}
}

func TestVerifiedActionApprovalAndDigestImmutablePG(t *testing.T) {
	f, p, step, _ := verifiedFixture(t, "copy")
	ctx := t.Context()
	if _, err := f.pool.Exec(ctx, "UPDATE transfer_step SET action='move' WHERE id=$1", step.ID); err == nil {
		t.Fatal("approved action changed")
	}
	if _, err := f.pool.Exec(ctx, "UPDATE transfer_plan SET root_path=$2 WHERE id=$1", p.ID, f.source); err == nil {
		t.Fatal("approved root changed")
	}
	if err := f.svc.ExecutePlan(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, "UPDATE transfer_content_evidence SET content='{}'::jsonb WHERE step_id=$1", step.ID); err == nil {
		t.Fatal("sealed digest cleared")
	}
	if _, err := f.pool.Exec(ctx, "DELETE FROM transfer_plan WHERE id=$1", p.ID); err == nil {
		t.Fatal("historical plan deleted")
	}
}

func TestVerifiedActionConcurrentWorkersPG(t *testing.T) {
	f, p, step, _ := verifiedFixture(t, "copy")
	gen := idgen.New()
	other := NewService(f.pool, gen, outbox.New(f.pool, gen, clock.New()), rootfs.NewFilesystem(), transfercase.ExecutePreparedPlan)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, svc := range []*Service{f.svc, other} {
		wg.Go(func() { errs <- svc.ExecutePlan(context.Background(), p.ID) })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil && !errors.Is(err, ErrExecutionRejected) {
			t.Fatal(err)
		}
	}
	checkVerifiedImport(t, f, p, step, "copy")
}
