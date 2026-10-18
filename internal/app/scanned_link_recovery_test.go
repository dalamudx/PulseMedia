package app

import (
	"context"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"os"
	"testing"
)

func TestScannedLinkRecoveryRejectsChangedIdentityAndExistingMapping(t *testing.T) {
	for _, branch := range []string{"identity", "mapping"} {
		t.Run(branch, func(t *testing.T) {
			f := organizationSetup(t)
			ctx := context.Background()
			f.observe(t)
			lease := f.claim(t, "before-failure")
			if _, e := f.pool.Exec(ctx, `CREATE FUNCTION reject_scanned_link() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RAISE EXCEPTION 'injected';END$$;CREATE TRIGGER reject_scanned_link BEFORE INSERT ON unit_fact FOR EACH ROW EXECUTE FUNCTION reject_scanned_link()`); e != nil {
				t.Fatal(e)
			}
			if e := f.svc.OrganizeDownload(ctx, lease); e == nil {
				t.Fatal("expected import commit failure")
			}
			if _, e := f.pool.Exec(ctx, `DROP TRIGGER reject_scanned_link ON unit_fact;DROP FUNCTION reject_scanned_link()`); e != nil {
				t.Fatal(e)
			}
			task, e := f.q.GetDownloadTaskByOpID(ctx, f.op)
			if e != nil {
				t.Fatal(e)
			}
			plan := uuid.UUID(task.TransferPlanID.Bytes)
			steps, e := f.q.ListStepsForPlan(ctx, plan)
			if e != nil || len(steps) != 1 {
				t.Fatal(steps, e)
			}
			step := steps[0]
			before, e := os.Stat(step.TargetPath)
			if e != nil {
				t.Fatal(e)
			}
			fingerprint := fmt.Sprintf("stat.v1:%d:%d:%d", step.SourceInode.Int64, step.SourceSize, step.SourceMtime)
			if branch == "identity" {
				fingerprint = "stat.v1:0:0:0"
			}
			file, e := f.q.CreateLibraryFile(ctx, db.CreateLibraryFileParams{ID: uuid.New(), RootID: f.root, RelPath: step.TargetRel.String, SizeBytes: step.SourceSize, MtimeNs: step.SourceMtime, FileFingerprint: pgtype.Text{String: fingerprint, Valid: true}})
			if e != nil {
				t.Fatal(e)
			}
			if branch == "mapping" {
				if e = f.q.LinkFileUnit(ctx, db.LinkFileUnitParams{FileID: file.ID, UnitID: f.unit}); e != nil {
					t.Fatal(e)
				}
			}
			if _, e = f.pool.Exec(ctx, `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, lease.ID); e != nil {
				t.Fatal(e)
			}
			if _, e = f.pool.Exec(ctx, `UPDATE transfer_plan SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, plan); e != nil {
				t.Fatal(e)
			}
			if _, e = f.q.RecoverExpiredOrganizationJobs(ctx); e != nil {
				t.Fatal(e)
			}
			next := f.claim(t, "recovery")
			if e = f.newService().OrganizeDownload(ctx, next); e != nil {
				t.Fatal(e)
			}
			after, e := os.Stat(step.TargetPath)
			if e != nil || !os.SameFile(before, after) {
				t.Fatal("rejection altered original hardlink", e)
			}
			current, e := f.q.GetDownloadTaskByOpID(ctx, f.op)
			if e != nil || current.OrganizationState != "review_required" {
				t.Fatal(current, e)
			}
			if f.count(t, "unit_fact") != 0 || f.count(t, "unit_reservation") != 1 {
				t.Fatal("rejection changed facts or protection")
			}
			preserved, e := f.q.GetActiveFileByPath(ctx, db.GetActiveFileByPathParams{RootID: f.root, RelPath: step.TargetRel.String})
			if e != nil || preserved.ID != file.ID || preserved.FileFingerprint.String != fingerprint {
				t.Fatal("rejection replaced scanned identity", e)
			}
		})
	}
}
