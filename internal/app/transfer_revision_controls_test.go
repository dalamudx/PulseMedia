package app

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"os"
	"path/filepath"
	"testing"
)

func TestManualMovieRevisionGuardsAndTailRollback(t *testing.T) {
	for _, mode := range []string{"existing-target", "foreign-admin", "disabled", "withdrawn", "source-version", "pending-job", "receipt-fault", "audit-fault"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			f, s, a, old := revisionSetup(t)
			p, e := s.Preview(ctx, a, old.ID, "Edited/Movie revised.mkv")
			if e != nil || !p.Revisable {
				t.Fatal(p, e)
			}
			c := ports.TransferRevisionCommand{PlanID: old.ID, TargetRel: p.TargetRel, ExpectedInputStamp: p.InputStamp, Confirmed: true, Reason: "Correct target"}
			switch mode {
			case "existing-target":
				if e = os.MkdirAll(filepath.Join(f.target, filepath.Dir(p.TargetRel)), 0700); e != nil {
					t.Fatal(e)
				}
				if e = os.WriteFile(filepath.Join(f.target, p.TargetRel), []byte("protected existing"), 0600); e != nil {
					t.Fatal(e)
				}
			case "foreign-admin":
				user := uuid.New()
				if _, e = f.q.CreateUser(ctx, db.CreateUserParams{ID: user, Username: "other-admin", PasswordHash: "fixture", Role: ports.RoleAdmin, IsActive: true}); e != nil {
					t.Fatal(e)
				}
				a.ID = user.String()
			case "withdrawn":
				if _, e = f.pool.Exec(ctx, "UPDATE download_requirement SET state='released' WHERE operation_id=$1", f.op); e != nil {
					t.Fatal(e)
				}
			case "disabled":
				if _, e = f.pool.Exec(ctx, "UPDATE app_user SET is_active=false WHERE id=$1", a.ID); e != nil {
					t.Fatal(e)
				}
			case "source-version":
				if e = os.WriteFile(f.path, []byte("new source version"), 0600); e != nil {
					t.Fatal(e)
				}
			case "pending-job":
				if _, e = f.pool.Exec(ctx, "UPDATE job SET state='queued' WHERE kind='download_organization'"); e != nil {
					t.Fatal(e)
				}
			default:
				table := "idempotency_record"
				if mode == "audit-fault" {
					table = "audit_log"
				}
				if _, e = f.pool.Exec(ctx, "CREATE FUNCTION revision_fail() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RAISE EXCEPTION 'fixture failure'; END$$; CREATE TRIGGER revision_fail BEFORE INSERT ON "+table+" FOR EACH ROW EXECUTE FUNCTION revision_fail()"); e != nil {
					t.Fatal(e)
				}
			}
			if _, e = s.Commit(ctx, a, c, "rejected-revision"); e == nil {
				t.Fatal("accepted invalid or faulted revision")
			}
			task, e := f.q.GetDownloadTaskByOpID(ctx, f.op)
			if e != nil || uuid.UUID(task.TransferPlanID.Bytes) != old.ID || task.OrganizationState != "review_required" || f.count(t, "transfer_plan") != 1 || f.count(t, "unit_fact") != 0 {
				t.Fatal("partial revision", e)
			}
			if mode == "receipt-fault" || mode == "audit-fault" {
				table := "idempotency_record"
				if mode == "audit-fault" {
					table = "audit_log"
				}
				if _, e = f.pool.Exec(ctx, "DROP TRIGGER revision_fail ON "+table); e != nil {
					t.Fatal(e)
				}
				if _, e = s.Commit(ctx, a, c, "rejected-revision"); e != nil {
					t.Fatal("same key recovery", e)
				}
			}
		})
	}
}
