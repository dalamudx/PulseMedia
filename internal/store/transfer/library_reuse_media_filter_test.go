package transfer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type mediaFilterReuseFilesystem struct {
	ports.VerifiedTransferFilesystem
	links, transfers, contents, observations int
}

func (f *mediaFilterReuseFilesystem) Hardlink(c context.Context, s ports.TransferRoot, sr string, d ports.TransferRoot, dr string, i ports.FileIdentity) error {
	f.links++
	return f.VerifiedTransferFilesystem.Hardlink(c, s, sr, d, dr, i)
}
func (f *mediaFilterReuseFilesystem) InspectContent(c context.Context, r ports.TransferRoot, p string, i ports.FileIdentity) (ports.TransferContent, error) {
	f.contents++
	return f.VerifiedTransferFilesystem.InspectContent(c, r, p, i)
}
func (f *mediaFilterReuseFilesystem) TransferVerified(c context.Context, s ports.TransferRoot, sr string, d ports.TransferRoot, dr string, p ports.TransferContent, a string, id uuid.UUID) (ports.VerifiedTransferResult, error) {
	f.transfers++
	return f.VerifiedTransferFilesystem.TransferVerified(c, s, sr, d, dr, p, a, id)
}
func (f *mediaFilterReuseFilesystem) ObserveTransfer(c context.Context, s ports.TransferRoot, sr string, d ports.TransferRoot, dr string, p ports.TransferContent, a string, id uuid.UUID) (ports.VerifiedTransferResult, error) {
	f.observations++
	return f.VerifiedTransferFilesystem.ObserveTransfer(c, s, sr, d, dr, p, a, id)
}
func reuseFilterInventory(t *testing.T, f *safetyFixture) string {
	t.Helper()
	var out string
	err := f.pool.QueryRow(t.Context(), `SELECT jsonb_build_array(
 (SELECT jsonb_agg(to_jsonb(r) ORDER BY id) FROM library_root r),
 (SELECT jsonb_agg(to_jsonb(f) ORDER BY id) FROM library_file f),
 (SELECT jsonb_agg(to_jsonb(m) ORDER BY file_id,unit_id) FROM library_file_unit m),
 (SELECT jsonb_agg(to_jsonb(f) ORDER BY id) FROM unit_fact f))::text`).Scan(&out)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func TestLibraryReuseMediaFilterConfirmationPG(t *testing.T) {
	for _, mode := range []string{"minimum", "sample_target"} {
		t.Run(mode, func(t *testing.T) {
			f, r, a, target, file := reusePreviewFixture(t)
			c := reuseCommand(t, r, a, target, file, "hardlink")
			source, err := f.q.LockTransferOldFile(t.Context(), file)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "minimum" {
				f.svc.minimumMediaBytes = source.SizeBytes + 1
			} else {
				c.TargetRel = "Samples/Movie.mkv"
			}
			before := reuseFilterInventory(t, f)
			beforeJobs, beforePlans, beforeAudits, beforeEvents := f.count(t, "job"), f.count(t, "library_reuse_plan"), f.count(t, "audit_log"), f.count(t, "outbox_event")
			p := reusePreview(t, r, a, target)
			if mode == "minimum" && (p.CanConfirm || len(p.Candidates) != 0) {
				t.Fatal("excluded source remained confirmable", p)
			}
			if receipt, err := r.ConfirmLibraryReuse(t.Context(), a, c, "filtered-new-key"); !errors.Is(err, ports.ErrLibraryReuseChanged) || receipt != nil {
				t.Fatal("filtered reuse accepted new confirmation", err)
			}
			ownedTarget := target
			ownedTarget.RootID = f.root
			if !reusePreview(t, r, a, ownedTarget).AlreadyOwned {
				t.Fatal("new deployment filter erased existing target ownership")
			}
			if before != reuseFilterInventory(t, f) || beforeJobs != f.count(t, "job") || beforePlans != f.count(t, "library_reuse_plan") || beforeAudits != f.count(t, "audit_log") || beforeEvents != f.count(t, "outbox_event") {
				t.Fatal("rejected confirmation changed inventory or created work")
			}
		})
	}
}
func TestLibraryReuseMediaFilterPendingPlanPG(t *testing.T) {
	for _, action := range []string{"hardlink", "copy"} {
		t.Run(action, func(t *testing.T) {
			f, r, a, target, file := reusePreviewFixture(t)
			c := reuseCommand(t, r, a, target, file, action)
			receipt, err := r.ConfirmLibraryReuse(t.Context(), a, c, "pending-original")
			if err != nil {
				t.Fatal(err)
			}
			source, err := f.q.LockTransferOldFile(t.Context(), file)
			if err != nil {
				t.Fatal(err)
			}
			f.svc.minimumMediaBytes = source.SizeBytes + 1
			counts := &mediaFilterReuseFilesystem{VerifiedTransferFilesystem: f.svc.files.(ports.VerifiedTransferFilesystem)}
			f.svc.files = counts
			before := reuseFilterInventory(t, f)
			if err = r.ExecuteLibraryReuse(t.Context(), claimReuse(t, f)); err != nil {
				t.Fatal(err)
			}
			p, err := f.q.GetLibraryReusePlan(t.Context(), receipt.PlanID)
			if err != nil || p.State != "review_required" {
				t.Fatal("excluded pending work did not stop", err)
			}
			if counts.links != 0 || counts.transfers != 0 || counts.contents != 0 || before != reuseFilterInventory(t, f) {
				t.Fatal("excluded pending work wrote a physical file/content permit or facts")
			}
			root, err := f.q.GetLibraryRootByID(t.Context(), target.RootID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = os.Lstat(filepath.Join(root.Path, c.TargetRel)); !os.IsNotExist(err) {
				t.Fatal("excluded work published a target", err)
			}
			again, err := r.ConfirmLibraryReuse(t.Context(), a, c, "pending-original")
			if err != nil || again == nil || *again != *receipt {
				t.Fatal("rule tightening invalidated original receipt", err)
			}
		})
	}
}
func TestLibraryReuseMediaFilterPublishedRecoveryPG(t *testing.T) {
	for _, action := range []string{"hardlink", "copy"} {
		for _, mode := range []string{"recover", "replaced_target"} {
			t.Run(action+"/"+mode, func(t *testing.T) {
				f, r, a, target, file := reusePreviewFixture(t)
				c := reuseCommand(t, r, a, target, file, action)
				receipt, err := r.ConfirmLibraryReuse(t.Context(), a, c, "published-original")
				if err != nil {
					t.Fatal(err)
				}
				if _, err = f.pool.Exec(t.Context(), `CREATE FUNCTION reject_filter_reuse_finish() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.state='completed' THEN RAISE EXCEPTION 'injected commit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_filter_reuse_finish BEFORE UPDATE ON library_reuse_plan FOR EACH ROW EXECUTE FUNCTION reject_filter_reuse_finish()`); err != nil {
					t.Fatal(err)
				}
				lease := claimReuse(t, f)
				if err = r.ExecuteLibraryReuse(t.Context(), lease); err == nil {
					t.Fatal("expected finalization rollback")
				}
				planBefore, err := f.q.GetLibraryReusePlan(t.Context(), receipt.PlanID)
				if err != nil {
					t.Fatal(err)
				}
				contentBefore, contentErr := f.q.GetLibraryReuseContent(t.Context(), receipt.PlanID)
				root, err := f.q.GetLibraryRootByID(t.Context(), target.RootID)
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(root.Path, c.TargetRel)
				beforeTarget, err := os.Stat(path)
				if err != nil {
					t.Fatal("target not published before rollback", err)
				}
				beforeInventory := reuseFilterInventory(t, f)
				if _, err = f.pool.Exec(t.Context(), "DROP TRIGGER reject_filter_reuse_finish ON library_reuse_plan"); err != nil {
					t.Fatal(err)
				}
				source, err := f.q.LockTransferOldFile(t.Context(), file)
				if err != nil {
					t.Fatal(err)
				}
				f.svc.minimumMediaBytes = source.SizeBytes + 1
				counts := &mediaFilterReuseFilesystem{VerifiedTransferFilesystem: f.svc.files.(ports.VerifiedTransferFilesystem)}
				f.svc.files = counts
				if mode == "replaced_target" {
					if err = os.Remove(path); err != nil {
						t.Fatal(err)
					}
					if err = os.WriteFile(path, []byte("unrelated target bytes"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if err = r.ExecuteLibraryReuse(t.Context(), lease); err != nil {
					t.Fatal(err)
				}
				planAfter, err := f.q.GetLibraryReusePlan(t.Context(), receipt.PlanID)
				if err != nil {
					t.Fatal(err)
				}
				contentAfter, afterErr := f.q.GetLibraryReuseContent(t.Context(), receipt.PlanID)
				if counts.links != 0 || counts.transfers != 0 || counts.contents != 0 || string(contentBefore) != string(contentAfter) || !errors.Is(afterErr, contentErr) || string(planBefore.Frozen) != string(planAfter.Frozen) {
					t.Fatal("recovery acquired new write permission or changed frozen evidence")
				}
				if mode == "recover" {
					afterTarget, err := os.Stat(path)
					if err != nil || !os.SameFile(beforeTarget, afterTarget) || planAfter.State != "completed" {
						t.Fatal("original target was not observed and adopted", err)
					}
					if action == "copy" && counts.observations < 1 {
						t.Fatal("copy recovery did not verify original full digest")
					}
					owned := reusePreview(t, r, a, target)
					if !owned.AlreadyOwned {
						t.Fatal("recovered target ownership missing")
					}
				} else if planAfter.State != "review_required" || beforeInventory != reuseFilterInventory(t, f) {
					t.Fatal("replacement target produced false ingestion")
				}
				again, err := r.ConfirmLibraryReuse(t.Context(), a, c, "published-original")
				if err != nil || again == nil || *again != *receipt {
					t.Fatal("old receipt changed after filter tightening", err)
				}
			})
		}
	}
}
