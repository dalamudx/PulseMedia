package transfer

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
)

func reuseCommand(t *testing.T, r *Repository, a *ports.Actor, target ports.LibraryReuseTarget, file uuid.UUID, action string) ports.LibraryReuseCommand {
	t.Helper()
	p := reusePreview(t, r, a, target)
	for _, c := range p.Candidates {
		if c.FileID == file {
			return ports.LibraryReuseCommand{Target: target, SourceFileID: file, ConfirmedUnitIDs: c.UnitIDs, TargetRel: "Reuse/Movie.mkv", Action: action, InputStamp: p.InputStamp, Confirmed: true}
		}
	}
	t.Fatal("missing source")
	return ports.LibraryReuseCommand{}
}
func claimReuse(t *testing.T, f *safetyFixture) ports.JobLease {
	t.Helper()
	job, e := f.q.ClaimJob(t.Context(), db.ClaimJobParams{Column1: []string{"library_reuse"}, Column2: "reuse-test", Column3: 300000})
	if e != nil {
		t.Fatal(e)
	}
	return ports.JobLease{ID: job.ID, Owner: "reuse-test", Token: job.LeaseToken}
}
func TestLibraryReuseExecutionPG(t *testing.T) {
	for _, action := range []string{"hardlink", "copy"} {
		t.Run(action, func(t *testing.T) {
			f, r, a, target, file := reusePreviewFixture(t)
			c := reuseCommand(t, r, a, target, file, action)
			before := f.count(t, "download_task")
			receipt, e := r.ConfirmLibraryReuse(t.Context(), a, c, "reuse-original")
			if e != nil {
				t.Fatal(e)
			}
			again, e := r.ConfirmLibraryReuse(t.Context(), a, c, "reuse-original")
			if e != nil || *again != *receipt {
				t.Fatal("lost response", again, e)
			}
			if e = r.ExecuteLibraryReuse(t.Context(), claimReuse(t, f)); e != nil {
				t.Fatal(e)
			}
			p, e := f.q.GetLibraryReusePlan(t.Context(), receipt.PlanID)
			if e != nil || p.State != "completed" {
				t.Fatal(p, e)
			}
			root, e := f.q.GetLibraryRootByID(t.Context(), target.RootID)
			if e != nil {
				t.Fatal(e)
			}
			body, e := os.ReadFile(filepath.Join(root.Path, c.TargetRel))
			if e != nil || len(body) == 0 {
				t.Fatal("target file", e)
			}
			source, e := f.q.LockTransferOldFile(t.Context(), file)
			if e != nil {
				t.Fatal(e)
			}
			imported, e := f.q.LockTransferOldFile(t.Context(), uuid.UUID(p.TargetFileID.Bytes))
			if e != nil {
				t.Fatal(e)
			}
			if action == "hardlink" && source.FileFingerprint != imported.FileFingerprint {
				t.Fatal("not a hardlink")
			}
			if action == "copy" && source.FileFingerprint == imported.FileFingerprint {
				t.Fatal("copy reused source identity")
			}
			if root.MutationSeq != imported.LastMutationSeq || root.MutationSeq < 1 {
				t.Fatal("mutation watermark")
			}
			if f.count(t, "download_task") != before {
				t.Fatal("fabricated download")
			}
			owned := reusePreview(t, r, a, target)
			if !owned.AlreadyOwned {
				t.Fatal("target not owned", owned)
			}
			again, e = r.ConfirmLibraryReuse(t.Context(), a, c, "reuse-original")
			if e != nil || *again != *receipt {
				t.Fatal("completed receipt", again, e)
			}
			var active int
			if e = f.pool.QueryRow(t.Context(), "SELECT count(*) FROM library_reuse_unit WHERE plan_id=$1 AND active", p.ID).Scan(&active); e != nil || active != 0 {
				t.Fatal(active, e)
			}
		})
	}
}
func TestLibraryReuseOfflineAndRevocationPG(t *testing.T) {
	for _, mode := range []string{"offline", "revoked", "mapping", "retired", "quality", "identity", "generation"} {
		t.Run(mode, func(t *testing.T) {
			f, r, a, target, file := reusePreviewFixture(t)
			c := reuseCommand(t, r, a, target, file, "hardlink")
			receipt, e := r.ConfirmLibraryReuse(t.Context(), a, c, "guard")
			if e != nil {
				t.Fatal(e)
			}
			switch mode {
			case "offline":
				_, e = f.pool.Exec(t.Context(), "UPDATE library_root SET health='offline',health_revision=health_revision+1 WHERE id=$1", f.root)
			case "revoked":
				_, e = f.pool.Exec(t.Context(), "UPDATE app_user SET is_active=false WHERE id=$1", uuid.MustParse(a.ID))
			case "retired":
				_, e = f.pool.Exec(t.Context(), "UPDATE media_unit SET retired=true WHERE id=$1", f.unit)
			case "quality":
				_, e = f.pool.Exec(t.Context(), "UPDATE unit_fact SET quality_profile='{}'::jsonb WHERE source_file_id=$1", file)
			case "identity":
				_, e = f.pool.Exec(t.Context(), "UPDATE library_file SET mtime_ns=mtime_ns+1 WHERE id=$1", file)
			case "generation":
				_, e = f.pool.Exec(t.Context(), "UPDATE library_root SET generation=generation+1 WHERE id=$1", f.root)
				if e == nil {
					_, e = f.pool.Exec(t.Context(), "UPDATE library_root SET baseline_completed=true WHERE id=$1", f.root)
				}
			case "mapping":
				_, e = f.pool.Exec(t.Context(), "UPDATE library_file_unit SET retired_at=clock_timestamp() WHERE file_id=$1", file)
			}
			if e != nil {
				t.Fatal(e)
			}
			if e = r.ExecuteLibraryReuse(t.Context(), claimReuse(t, f)); e != nil {
				t.Fatal(e)
			}
			p, e := f.q.GetLibraryReusePlan(t.Context(), receipt.PlanID)
			if e != nil {
				t.Fatal(e)
			}
			expected := "review_required"
			if mode == "offline" {
				expected = "waiting"
			}
			if p.State != expected {
				t.Fatal(p.State)
			}
			root, _ := f.q.GetLibraryRootByID(t.Context(), target.RootID)
			if _, e = os.Stat(filepath.Join(root.Path, c.TargetRel)); !errors.Is(e, os.ErrNotExist) {
				t.Fatal("unexpected publication", e)
			}
			if mode == "offline" {
				_, e = f.pool.Exec(t.Context(), "UPDATE library_root SET health='online',health_revision=health_revision+1 WHERE id=$1", f.root)
				if e != nil {
					t.Fatal(e)
				}
				_, e = f.pool.Exec(t.Context(), "UPDATE job SET not_before=clock_timestamp() WHERE id=$1", receipt.JobID)
				if e != nil {
					t.Fatal(e)
				}
				if e = r.ExecuteLibraryReuse(t.Context(), claimReuse(t, f)); e != nil {
					t.Fatal(e)
				}
				p, _ = f.q.GetLibraryReusePlan(t.Context(), receipt.PlanID)
				if p.State != "completed" {
					t.Fatal("restoration", p.State)
				}
			}
		})
	}
}
func TestLibraryReuseCommitFailureRecoveryPG(t *testing.T) {
	f, r, a, target, file := reusePreviewFixture(t)
	c := reuseCommand(t, r, a, target, file, "copy")
	receipt, e := r.ConfirmLibraryReuse(t.Context(), a, c, "rollback")
	if e != nil {
		t.Fatal(e)
	}
	_, e = f.pool.Exec(t.Context(), `CREATE FUNCTION reject_reuse_finish() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.state='completed' THEN RAISE EXCEPTION 'injected commit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_reuse_finish BEFORE UPDATE ON library_reuse_plan FOR EACH ROW EXECUTE FUNCTION reject_reuse_finish()`)
	if e != nil {
		t.Fatal(e)
	}
	lease := claimReuse(t, f)
	if e = r.ExecuteLibraryReuse(t.Context(), lease); e == nil {
		t.Fatal("expected transaction failure")
	}
	p, _ := f.q.GetLibraryReusePlan(t.Context(), receipt.PlanID)
	if p.State == "completed" {
		t.Fatal("false completion")
	}
	root, _ := f.q.GetLibraryRootByID(t.Context(), target.RootID)
	info, e := os.Stat(filepath.Join(root.Path, c.TargetRel))
	if e != nil {
		t.Fatal("physical publication missing", e)
	}
	_, e = f.pool.Exec(t.Context(), "DROP TRIGGER reject_reuse_finish ON library_reuse_plan")
	if e != nil {
		t.Fatal(e)
	}
	if e = r.ExecuteLibraryReuse(t.Context(), lease); e != nil {
		t.Fatal(e)
	}
	next, e := os.Stat(filepath.Join(root.Path, c.TargetRel))
	if e != nil || !os.SameFile(info, next) {
		t.Fatal("recovery replaced original file", e)
	}
}
func TestLibraryReuseFullMappingPG(t *testing.T) {
	f, r, a, target, file := reusePreviewFixture(t)
	var mediaID uuid.UUID
	if e := f.pool.QueryRow(t.Context(), "SELECT media_id FROM media_unit WHERE id=$1", f.unit).Scan(&mediaID); e != nil {
		t.Fatal(e)
	}
	extra := uuid.New()
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{"UPDATE media SET media_type='tv' WHERE id=$1", []any{mediaID}},
		{"UPDATE library_root SET media_type='tv' WHERE id=ANY($1::uuid[])", []any{[]uuid.UUID{f.root, target.RootID}}},
		{"UPDATE media_unit SET unit_key='S01E01' WHERE id=$1", []any{f.unit}},
		{"INSERT INTO media_unit(id,media_id,unit_key) VALUES($1,$2,'S01E02')", []any{extra, mediaID}},
		{"INSERT INTO library_file_unit(file_id,unit_id) VALUES($1,$2)", []any{file, extra}},
	} {
		if _, e := f.pool.Exec(t.Context(), stmt.sql, stmt.args...); e != nil {
			t.Fatal(e)
		}
	}
	p := reusePreview(t, r, a, target)
	if p.CanConfirm || p.Candidates[0].Qualified {
		t.Fatal("missing full-set imported fact accepted", p)
	}
	_, e := f.pool.Exec(t.Context(), "INSERT INTO unit_fact(id,unit_id,stage,source_file_id,root_id,quality_profile) SELECT $1,$2,'imported',source_file_id,root_id,quality_profile FROM unit_fact WHERE source_file_id=$3 LIMIT 1", uuid.New(), extra, file)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.pool.Exec(t.Context(), "UPDATE library_root SET health='online',baseline_completed=true WHERE id=ANY($1::uuid[])", []uuid.UUID{f.root, target.RootID}); e != nil {
		t.Fatal(e)
	}
	for _, id := range []uuid.UUID{f.unit, extra} {
		probe := target
		probe.UnitID = id
		v := reusePreview(t, r, a, probe)
		if !v.CanConfirm {
			t.Fatalf("full mapping preview: %+v", v)
		}
	}
	c := reuseCommand(t, r, a, target, file, "hardlink")
	c.TargetRel = "Series/S01E01-E02.mkv"
	partial := c
	partial.ConfirmedUnitIDs = []uuid.UUID{f.unit}
	if _, e = r.ConfirmLibraryReuse(t.Context(), a, partial, "partial"); !errors.Is(e, ports.ErrLibraryReuseChanged) {
		t.Fatal("partial coverage", e)
	}
	receipt, e := r.ConfirmLibraryReuse(t.Context(), a, c, "full")
	if e != nil {
		t.Fatal(e)
	}
	if e = r.ExecuteLibraryReuse(t.Context(), claimReuse(t, f)); e != nil {
		t.Fatal(e)
	}
	plan, _ := f.q.GetLibraryReusePlan(t.Context(), receipt.PlanID)
	units, e := f.q.ListUnitsForFile(t.Context(), uuid.UUID(plan.TargetFileID.Bytes))
	if e != nil || len(units) != 2 {
		t.Fatal("mapping lost", units, e)
	}
}
