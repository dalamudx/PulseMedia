package transfer

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/jackc/pgx/v5/pgtype"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func (f *safetyFixture) observationTime(t *testing.T) pgtype.Timestamptz {
	t.Helper()
	stamp, err := f.q.GetSearchDatabaseTime(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return stamp
}
func TestStaleImportObservationRollsBackAllFacts(t *testing.T) {
	f := safetySetup(t)
	plan, step := f.plan(t)
	ctx := context.Background()
	roots, release, err := f.svc.files.LockRoots(ctx, f.source, f.target)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	claimed, err := f.q.ClaimTransferPlan(ctx, db.ClaimTransferPlanParams{ID: plan.ID, Owner: text("proof-worker")})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.svc.startStep(ctx, claimed, step); err != nil {
		t.Fatal(err)
	}
	if err = f.svc.files.Hardlink(ctx, roots[f.source], step.SourceRel.String, roots[f.target], step.TargetRel.String, frozen(step)); err != nil {
		t.Fatal(err)
	}
	observedAt := f.observationTime(t)
	observedAt.Time = observedAt.Time.Add(-6 * time.Second)
	if err = f.svc.commitStep(ctx, claimed, step, observedAt); !errors.Is(err, ErrReviewRequired) {
		t.Fatal("expired observation accepted", err)
	}
	f.noImport(t)
	bytes, err := os.ReadFile(step.TargetPath)
	if err != nil || string(bytes) != "frozen video bytes" {
		t.Fatal("original hardlink lost", err)
	}
}

type afterLinkFilesystem struct {
	ports.TransferFilesystem
	after func() error
}

func (f afterLinkFilesystem) Hardlink(ctx context.Context, src ports.TransferRoot, source string, dst ports.TransferRoot, target string, identity ports.FileIdentity) error {
	if err := f.TransferFilesystem.Hardlink(ctx, src, source, dst, target, identity); err != nil {
		return err
	}
	return f.after()
}
func TestPostLinkNamespaceReplacementDoesNotImport(t *testing.T) {
	for _, attack := range []string{"target_root", "source_root", "target_file"} {
		t.Run(attack, func(t *testing.T) {
			f := safetySetup(t)
			plan, step := f.plan(t)
			var savedLink string
			f.svc.files = afterLinkFilesystem{TransferFilesystem: f.svc.files, after: func() error {
				if attack == "target_file" {
					savedLink = filepath.Join(t.TempDir(), "original-link.mkv")
					if err := os.Rename(step.TargetPath, savedLink); err != nil {
						return err
					}
					return os.WriteFile(step.TargetPath, []byte("unrelated replacement"), 0600)
				}
				root := f.target
				if attack == "source_root" {
					root = f.source
				}
				moved := filepath.Join(t.TempDir(), "original-root")
				if err := os.Rename(root, moved); err != nil {
					return err
				}
				if attack == "target_root" {
					savedLink = filepath.Join(moved, step.TargetRel.String)
				} else {
					savedLink = step.TargetPath
				}
				if err := os.Mkdir(root, 0700); err != nil {
					return err
				}
				if attack == "target_root" {
					return os.WriteFile(filepath.Join(root, ".pulsemedia_root"), []byte("root"), 0600)
				}
				return os.WriteFile(f.sourcePath, []byte("unrelated replacement"), 0600)
			}}
			if err := f.svc.ExecutePlan(context.Background(), plan.ID); !errors.Is(err, ErrReviewRequired) {
				t.Fatal("changed namespace accepted", err)
			}
			f.noImport(t)
			current, err := f.q.GetTransferPlanByID(context.Background(), plan.ID)
			if err != nil || current.State != "review_required" {
				t.Fatal("plan did not require review", current.State, err)
			}
			bytes, err := os.ReadFile(savedLink)
			if err != nil || string(bytes) != "frozen video bytes" {
				t.Fatal("original hardlink lost", err)
			}
		})
	}
}
