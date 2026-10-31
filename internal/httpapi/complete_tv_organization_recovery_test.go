package httpapi_test

import (
	"context"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/app"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/outbox"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	transferstore "github.com/dalamudx/PulseMedia/internal/store/transfer"
	"github.com/dalamudx/PulseMedia/internal/usecase/transfer"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCompleteTVOrganizationPGInterruptedAppRestartAndScanReuse(t *testing.T) {
	f := newCompleteOrganizationFixture(t)
	ctx := context.Background()
	r, e := f.confirm("restart")
	if e != nil {
		t.Fatal(e)
	}
	steps, e := f.n.f.a.Queries().ListStepsForPlan(ctx, r.PlanID)
	if e != nil {
		t.Fatal(e)
	}
	// Fail the second database publication after the hardlink already exists.
	f.n.exec(t, fmt.Sprintf(`CREATE FUNCTION reject_second_complete_import() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.target_id='%s' THEN RAISE EXCEPTION 'second publication interrupted'; END IF; RETURN NEW; END$$`, steps[1].ID))
	f.n.exec(t, `CREATE TRIGGER reject_second_complete_import BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION reject_second_complete_import()`)
	if e = f.run(t, r); e == nil {
		t.Fatal("expected interrupted publication")
	}
	current, e := f.n.f.a.Queries().ListStepsForPlan(ctx, r.PlanID)
	if e != nil || current[0].State != "completed" || current[1].State == "completed" {
		t.Fatal("partial progress", current, e)
	}
	for _, s := range current {
		src, e := os.Stat(s.SourcePath)
		if e != nil {
			t.Fatal(e)
		}
		target, e := os.Stat(s.TargetPath)
		if e != nil || !os.SameFile(src, target) {
			t.Fatal("link lost after rollback", e)
		}
	}
	var facts, pending, reserved, lanes int
	e = f.n.f.a.DBPool().QueryRow(ctx, `SELECT (SELECT count(*) FROM unit_fact WHERE stage='imported'),(SELECT count(*) FROM download_requirement WHERE state='pending'),(SELECT count(*) FROM unit_reservation WHERE state='dispatched'),(SELECT count(*) FROM acquisition_reservation WHERE released_at IS NULL)`).Scan(&facts, &pending, &reserved, &lanes)
	if e != nil || facts != 1 || pending != 1 || reserved != 1 || lanes != 2 {
		t.Fatal("protection prematurely released", facts, pending, reserved, lanes, e)
	}
	f.n.exec(t, `DROP TRIGGER reject_second_complete_import ON audit_log`)
	f.n.exec(t, `DROP FUNCTION reject_second_complete_import()`)
	// A scan has registered the exact target inode but has not associated a unit.
	second := current[1]
	plan, e := f.n.f.a.Queries().GetTransferPlanByID(ctx, r.PlanID)
	if e != nil {
		t.Fatal(e)
	}
	scanned := uuid.New()
	_, e = f.n.f.a.Queries().CreateLibraryFile(ctx, db.CreateLibraryFileParams{ID: scanned, RootID: plan.RootID, RelPath: second.TargetRel.String, SizeBytes: second.SourceSize, MtimeNs: second.SourceMtime, FileFingerprint: pgtype.Text{String: fmt.Sprintf("stat.v1:%d:%d:%d", second.SourceInode.Int64, second.SourceSize, second.SourceMtime), Valid: true}, LastMutationSeq: 2})
	if e != nil {
		t.Fatal(e)
	}
	f.n.exec(t, `UPDATE transfer_plan SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, r.PlanID)
	f.n.exec(t, `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, r.JobID)
	f.n.exec(t, `UPDATE job SET state='succeeded',lease_owner=NULL,lease_until=NULL WHERE id<>$1 AND state='running'`, r.JobID)
	cfg := *f.n.f.a.Config()
	cfg.Server.HTTPAddr = "127.0.0.1:0"
	if e = f.n.f.a.Close(); e != nil {
		t.Fatal(e)
	}
	restarted, e := app.New(&cfg, app.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if e != nil {
		t.Fatal(e)
	}
	f.n.f.a = restarted
	queries := restarted.Queries()
	runCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
	done := make(chan error, 1)
	go func() { done <- restarted.Run(runCtx) }()
	defer func() {
		cancel()
		e := <-done
		if e != nil {
			t.Error("App shutdown", e)
		}
		_ = restarted.Close()
	}()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		task, e := queries.GetDownloadTaskByOpID(runCtx, f.command.OperationID)
		if e != nil {
			t.Fatal(e)
		}
		if task.OrganizationState == "completed" {
			break
		}
		select {
		case <-runCtx.Done():
			t.Fatal("App did not recover complete package", task)
		case <-ticker.C:
		}
	}
	f.assertImported(t, r)
	completed, e := restarted.Queries().ListStepsForPlan(ctx, r.PlanID)
	if e != nil || uuid.UUID(completed[1].TargetFileID.Bytes) != scanned {
		t.Fatal("scan row was not reused", e)
	}
}

func TestCompleteTVOrganizationPGFrozenChangesRequireReview(t *testing.T) {
	for _, kind := range []string{"target-conflict", "source-replace", "source-symlink", "root-replace", "intent", "provider", "external-fact"} {
		t.Run(kind, func(t *testing.T) {
			f := newCompleteOrganizationFixture(t)
			r, e := f.confirm("frozen")
			if e != nil {
				t.Fatal(e)
			}
			steps, e := f.n.f.a.Queries().ListStepsForPlan(context.Background(), r.PlanID)
			if e != nil {
				t.Fatal(e)
			}
			switch kind {
			case "target-conflict":
				if e = os.MkdirAll(filepath.Dir(steps[0].TargetPath), 0700); e == nil {
					e = os.WriteFile(steps[0].TargetPath, []byte("conflict"), 0600)
				}
			case "source-replace":
				e = os.Rename(steps[0].SourcePath, steps[0].SourcePath+".old")
				if e == nil {
					e = os.WriteFile(steps[0].SourcePath, make([]byte, 512), 0600)
				}
			case "source-symlink":
				e = os.Rename(steps[0].SourcePath, steps[0].SourcePath+".old")
				if e == nil {
					e = os.Symlink(steps[0].SourcePath+".old", steps[0].SourcePath)
				}
			case "root-replace":
				e = os.Rename(f.target, f.target+".old")
				if e == nil {
					e = os.Mkdir(f.target, 0700)
				}
				if e == nil {
					e = os.WriteFile(filepath.Join(f.target, ".pulsemedia_root"), nil, 0600)
				}
			case "intent":
				f.n.exec(t, `UPDATE subscription SET intent_version=intent_version+1 WHERE id=$1`, f.n.f.sub.Id)
			case "provider":
				if _, e = f.n.f.a.DBPool().Exec(context.Background(), `UPDATE media_unit_identity SET external_id=external_id+10000 WHERE unit_id=$1`, steps[0].UnitID); e == nil {
					t.Fatal("provider history mutation accepted")
				}
				f.n.exec(t, `UPDATE media_unit SET metadata_version=metadata_version+1 WHERE id=$1`, steps[0].UnitID)
				e = nil
			case "external-fact":
				f.n.exec(t, `UPDATE media_unit SET facts_version=facts_version+1 WHERE id=$1`, steps[0].UnitID)
			}
			if e != nil {
				t.Fatal(e)
			}
			_ = f.run(t, r)
			task, e := f.n.f.a.Queries().GetDownloadTaskByOpID(context.Background(), f.command.OperationID)
			if e != nil || task.OrganizationState != "review_required" {
				t.Fatal("not held for review", task, e)
			}
			var n, lanes int
			e = f.n.f.a.DBPool().QueryRow(context.Background(), `SELECT (SELECT count(*) FROM unit_fact WHERE stage='imported'),(SELECT count(*) FROM acquisition_reservation WHERE released_at IS NULL)`).Scan(&n, &lanes)
			if e != nil || n != 0 || lanes != 2 || f.n.adds.Load() != 0 {
				t.Fatal("invalid import or released protection", n, lanes, e)
			}
		})
	}
}

func TestCompleteTVOrganizationPGRealDMLRoleAndHistory(t *testing.T) {
	f := newCompleteOrganizationFixture(t)
	ctx := context.Background()
	uri, e := url.Parse(f.n.f.a.Config().Database.URL)
	if e != nil {
		t.Fatal(e)
	}
	_, dml := pgtest.GetCluster(t).CreateRoles(t, strings.TrimPrefix(uri.Path, "/"))
	pool, e := postgres.NewPool(ctx, dml, postgres.DefaultPoolConfig())
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	var super bool
	if e = pool.QueryRow(ctx, `SELECT rolsuper FROM pg_roles WHERE rolname=current_user`).Scan(&super); e != nil || super {
		t.Fatal("not a DML role", e)
	}
	gen := idgen.New()
	repo := transferstore.NewRepository(pool, gen, outbox.New(pool, gen, clock.New()), rootfs.NewFilesystem(), transfer.ExecutePreparedPlan)
	f.service = transfer.NewCompleteTVOrganizationService(repo)
	f.worker = transfer.NewService(repo)
	r, e := f.confirm("dml")
	if e != nil {
		t.Fatal("DML confirm", e)
	}
	if e = f.run(t, r); e != nil {
		t.Fatal("DML worker", e)
	}
	f.assertImported(t, r)
	for _, sql := range []string{`UPDATE transfer_step SET source_inode=source_inode+1 WHERE plan_id=$1`, `DELETE FROM transfer_step WHERE plan_id=$1`, `UPDATE transfer_plan SET root_generation=root_generation+1 WHERE id=$1`, `DELETE FROM complete_tv_transfer_evidence WHERE plan_id=$1`} {
		if _, e = pool.Exec(ctx, sql, r.PlanID); e == nil {
			t.Fatal("mutable history", sql)
		}
	}
	if e = applyCompleteTVOrganizationDDL(t, ctx, f.n.f.a.DBPool(), false); e == nil {
		t.Fatal("downgrade discarded full package history")
	}
	var n int
	if e = pool.QueryRow(ctx, `SELECT count(*) FROM complete_tv_transfer_evidence WHERE plan_id=$1`, r.PlanID).Scan(&n); e != nil || n != 1 {
		t.Fatal("history was lost", n, e)
	}
}
