package transfer

import (
	"errors"
	"os"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
)

func TestAutomaticTVOrganizationMediaSizeBoundaryPG(t *testing.T) {
	for _, mode := range []string{"at_threshold", "below_threshold"} {
		t.Run(mode, func(t *testing.T) {
			f := newAutomaticTVOrganizationFixture(t)
			before, err := os.ReadFile(f.sourcePath)
			if err != nil {
				t.Fatal(err)
			}
			f.svc.minimumMediaBytes = int64(len(before))
			if mode == "below_threshold" {
				f.svc.minimumMediaBytes++
			}
			if mode == "at_threshold" {
				if err := f.svc.OrganizeDownload(t.Context(), f.lease); err != nil {
					t.Fatal(err)
				}
				f.assertImported(t)
			} else {
				if err := f.svc.OrganizeDownload(t.Context(), f.lease); err == nil {
					t.Fatal("below-minimum automatic TV advanced organization")
				}
				f.assertUnpublished(t)
				if automaticTVOrganizationCount(t, f.pool, `SELECT count(*) FROM transfer_plan`) != 0 || automaticTVOrganizationCount(t, f.pool, `SELECT count(*) FROM automatic_tv_organization_plan`) != 0 {
					t.Fatal("excluded automatic TV created physical work")
				}
			}
			after, err := os.ReadFile(f.sourcePath)
			if err != nil || string(after) != string(before) {
				t.Fatal("organization changed downloaded source", err)
			}
		})
	}
}

func TestMediaFilterRunningStepOnlyObservesOriginalTargetPG(t *testing.T) {
	for _, action := range []string{"hardlink", "copy", "move"} {
		for _, mode := range []string{"unpublished", "published"} {
			t.Run(action+"/"+mode, func(t *testing.T) {
				var f *safetyFixture
				var plan db.TransferPlan
				var step db.TransferStep
				if action == "hardlink" {
					f = safetySetup(t)
					plan, step = f.plan(t)
				} else {
					f, plan, step, _ = verifiedFixture(t, action)
				}
				roots, release, err := f.svc.files.LockRoots(t.Context(), f.source, f.target)
				if err != nil {
					t.Fatal(err)
				}
				defer release()
				claimed, err := f.q.ClaimTransferPlan(t.Context(), db.ClaimTransferPlanParams{ID: plan.ID, Owner: text("filter-recovery-owner")})
				if err != nil {
					t.Fatal(err)
				}
				if err = f.svc.startStep(t.Context(), claimed, step); err != nil {
					t.Fatal(err)
				}
				if mode == "published" {
					if action == "hardlink" {
						err = f.svc.files.Hardlink(t.Context(), roots[f.source], step.SourceRel.String, roots[f.target], step.TargetRel.String, frozen(step))
					} else {
						cap := f.svc.files.(ports.VerifiedTransferFilesystem)
						proof, e := cap.InspectContent(t.Context(), roots[f.source], step.SourceRel.String, frozen(step))
						if e != nil {
							t.Fatal(e)
						}
						step, e = f.svc.freezeFileActionContent(t.Context(), claimed, step, proof, nil)
						if e != nil {
							t.Fatal(e)
						}
						_, err = cap.TransferVerified(t.Context(), roots[f.source], step.SourceRel.String, roots[f.target], step.TargetRel.String, proof, action, step.ID)
					}
					if err != nil {
						t.Fatal("initial physical publication", err)
					}
				}
				f.svc.minimumMediaBytes = step.SourceSize + 1
				counts := &mediaFilterReuseFilesystem{VerifiedTransferFilesystem: f.svc.files.(ports.VerifiedTransferFilesystem)}
				f.svc.files = counts
				if action == "hardlink" {
					err = f.svc.executeHardlinkStep(t.Context(), roots, claimed, step, ".pulsemedia_root", nil)
				} else {
					err = f.svc.executeVerifiedStep(t.Context(), roots, claimed, step, ".pulsemedia_root", nil)
				}
				if counts.links != 0 || counts.transfers != 0 || counts.contents != 0 {
					t.Fatal("filter tightening allowed a new physical write or content permit")
				}
				if mode == "unpublished" {
					if !errors.Is(err, ErrReviewRequired) {
						t.Fatal("unpublished running work bypassed filter", err)
					}
					if _, e := os.Lstat(step.TargetPath); !os.IsNotExist(e) {
						t.Fatal("unpublished running work created target", e)
					}
					f.noImport(t)
				} else {
					if err != nil {
						t.Fatal("published original result could not recover", err)
					}
					if f.count(t, "library_file_unit") != 1 || f.count(t, "unit_fact") != 1 {
						t.Fatal("observed result not atomically imported")
					}
					if action != "hardlink" && counts.observations == 0 {
						t.Fatal("recovery did not verify original byte digest")
					}
				}
			})
		}
	}
}
