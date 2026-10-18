package httpapi_test

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/outbox"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	transferstore "github.com/dalamudx/PulseMedia/internal/store/transfer"
	"github.com/dalamudx/PulseMedia/internal/usecase/transfer"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type completeOrganizationFixture struct {
	n              *trackingFixture
	service        *transfer.CompleteTVOrganizationService
	worker         *transfer.Service
	command        ports.CompleteTVOrganizationCommand
	source, target string
}

func newCompleteOrganizationFixture(t *testing.T) *completeOrganizationFixture {
	t.Helper()
	source, target := filepath.Join(t.TempDir(), "downloads"), filepath.Join(t.TempDir(), "library")
	for _, dir := range []string{filepath.Join(source, "Show"), target} {
		if e := os.MkdirAll(dir, 0700); e != nil {
			t.Fatal(e)
		}
	}
	for _, name := range []string{"Show.S01E01.mkv", "Show.S01E02.mkv"} {
		if e := os.WriteFile(filepath.Join(source, "Show", name), make([]byte, 512), 0600); e != nil {
			t.Fatal(e)
		}
	}
	if e := os.WriteFile(filepath.Join(source, "Show", "note.txt"), nil, 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(target, ".pulsemedia_root"), []byte("fixture"), 0600); e != nil {
		t.Fatal(e)
	}
	n := newTrackingFixtureWithRoots(t, func(f *completeTVFixture) {
		f.exec(t, `UPDATE library_root SET path=$1 WHERE id=(SELECT root_id FROM subscription WHERE id=$2)`, target, f.f.sub.Id)
		f.exec(t, `UPDATE library_root SET health='online',baseline_completed=true WHERE id=(SELECT root_id FROM subscription WHERE id=$1)`, f.f.sub.Id)
		f.exec(t, `UPDATE download_client SET remote_path_map=jsonb_build_object($1::text,$2::text)`, target, source)
	}, true)
	n.files = func() string {
		return `[{"index":0,"name":"Show/Show.S01E01.mkv","size":512,"progress":1},{"index":1,"name":"Show/Show.S01E02.mkv","size":512,"progress":1},{"index":2,"name":"Show/note.txt","size":0,"progress":1}]`
	}
	receipt, e := n.observe(context.Background())
	if e != nil || receipt.State != "completed" {
		t.Fatalf("tracking: %+v %v", receipt, e)
	}
	pool := n.f.a.DBPool()
	gen := idgen.New()
	repo := transferstore.NewRepository(pool, gen, outbox.New(pool, gen, clock.New()), rootfs.NewFilesystem())
	return &completeOrganizationFixture{n: n, service: transfer.NewCompleteTVOrganizationService(repo), worker: transfer.NewService(repo), command: ports.CompleteTVOrganizationCommand{TaskID: n.request.TaskID, OperationID: n.request.OperationID, TrackingJobID: n.request.Lease.ID, ExpectedEvidenceStamp: receipt.EvidenceStamp, Confirmed: true}, source: source, target: target}
}
func (f *completeOrganizationFixture) confirm(key string) (*ports.CompleteTVOrganizationReceipt, error) {
	return f.service.Confirm(context.Background(), f.n.actor, f.command, key)
}
func (f *completeOrganizationFixture) run(t *testing.T, r *ports.CompleteTVOrganizationReceipt) error {
	t.Helper()
	f.n.exec(t, `UPDATE job SET state='running',lease_owner='complete-organizer',lease_token=77,lease_until=clock_timestamp()+interval '2 minutes' WHERE id=$1`, r.JobID)
	return f.worker.OrganizeDownload(context.Background(), ports.JobLease{ID: r.JobID, Owner: "complete-organizer", Token: 77})
}
func (f *completeOrganizationFixture) assertImported(t *testing.T, r *ports.CompleteTVOrganizationReceipt) {
	t.Helper()
	q := f.n.f.a.Queries()
	ctx := context.Background()
	steps, e := q.ListStepsForPlan(ctx, r.PlanID)
	if e != nil || len(steps) != 2 {
		t.Fatal(steps, e)
	}
	expected := map[string]string{"S01E01": "Show.S01E01.mkv", "S01E02": "Show.S01E02.mkv"}
	for _, s := range steps {
		u, e := q.GetSearchUnit(ctx, s.UnitID)
		if e != nil {
			t.Fatal(e)
		}
		if s.SourcePath != filepath.Join(f.source, "Show", expected[u.UnitKey]) || s.TargetPath != filepath.Join(f.target, s.TargetRel.String) || s.SourceSize != 512 {
			t.Fatal("frozen paths/size", s)
		}
		source, e := os.Stat(filepath.Join(f.source, "Show", expected[u.UnitKey]))
		if e != nil {
			t.Fatal(e)
		}
		target, e := os.Stat(s.TargetPath)
		if e != nil || !os.SameFile(source, target) || s.State != "completed" {
			t.Fatal("hardlink", s, e)
		}
		var valid bool
		e = f.n.f.a.DBPool().QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM complete_tv_submission_unit frozen JOIN library_file_unit fu ON fu.unit_id=frozen.unit_id AND fu.file_id=$2 JOIN unit_fact fact ON fact.unit_id=frozen.unit_id AND fact.source_file_id=fu.file_id AND fact.stage='imported' AND fact.revoked_at IS NULL WHERE frozen.task_id=$1 AND frozen.unit_id=$3 AND frozen.unit_version=$4 AND frozen.unit_key=$5 AND frozen.provider_episode_id=$6)`, r.TaskID, s.TargetFileID, s.UnitID, u.MetadataVersion, u.UnitKey, map[string]int64{"S01E01": 1001, "S01E02": 1002}[u.UnitKey]).Scan(&valid)
		if e != nil || !valid {
			t.Fatal("exact frozen imported identity", e)
		}
		t.Logf("IMPORTED_PROOF unit=%s version=%d key=%s provider=%d source=%s target=%s inode=%d", s.UnitID, u.MetadataVersion, u.UnitKey, map[string]int64{"S01E01": 1001, "S01E02": 1002}[u.UnitKey], s.SourcePath, s.TargetPath, s.SourceInode.Int64)
	}
	task, e := q.GetDownloadTaskByOpID(ctx, f.command.OperationID)
	if e != nil || task.OrganizationState != "completed" {
		t.Fatal(task, e)
	}
	var facts, audits, events, pending, lanes, attachments int
	e = f.n.f.a.DBPool().QueryRow(ctx, `SELECT (SELECT count(*) FROM unit_fact WHERE stage='imported'),(SELECT count(*) FROM audit_log WHERE action='transfer.imported'),(SELECT count(*) FROM outbox_event WHERE event_type='media.imported'),(SELECT count(*) FROM download_requirement WHERE state='pending'),(SELECT count(*) FROM acquisition_reservation WHERE released_at IS NULL),(SELECT jsonb_array_length(files) FROM download_file_inventory WHERE task_id=$1)`, r.TaskID).Scan(&facts, &audits, &events, &pending, &lanes, &attachments)
	if e != nil || facts != 2 || audits != 2 || events != 2 || pending != 0 || lanes != 0 || attachments != 3 {
		t.Fatal("publish/consume counts", facts, audits, events, pending, lanes, attachments, e)
	}
	note, e := os.Stat(filepath.Join(f.source, "Show", "note.txt"))
	if e != nil || note.Size() != 0 {
		t.Fatal("attachment changed", e)
	}
	if f.n.adds.Load() != 0 {
		t.Fatal("organization issued Add")
	}
}
func TestCompleteTVOrganizationPGTwoVideosAndEightWayReceipt(t *testing.T) {
	f := newCompleteOrganizationFixture(t)
	var wg sync.WaitGroup
	results := make(chan *ports.CompleteTVOrganizationReceipt, 8)
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); r, e := f.confirm("same"); results <- r; errs <- e }()
	}
	wg.Wait()
	close(results)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal("confirm", e)
		}
	}
	var first *ports.CompleteTVOrganizationReceipt
	for r := range results {
		if first == nil {
			first = r
		} else if *first != *r {
			t.Fatal("split idempotency")
		}
	}
	original := f.command
	f.command.ExpectedEvidenceStamp = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, e := f.confirm("same"); !errors.Is(e, ports.ErrTransferReviewRequired) {
		t.Fatal("different intent", e)
	}
	f.command = original
	if e := f.run(t, first); e != nil {
		t.Fatal("worker", e)
	}
	f.assertImported(t, first)
	if _, e := f.confirm("same"); e != nil {
		t.Fatal("completed receipt", e)
	}
}
func TestCompleteTVOrganizationPGLateRollback(t *testing.T) {
	f := newCompleteOrganizationFixture(t)
	for _, table := range []string{"job", "audit_log", "outbox_event", "idempotency_record"} {
		t.Run(table, func(t *testing.T) {
			f.n.exec(t, `CREATE FUNCTION reject_complete_organization() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RAISE EXCEPTION 'tail rollback';END$$`)
			f.n.exec(t, "CREATE TRIGGER reject_complete_organization BEFORE INSERT ON "+table+" FOR EACH ROW EXECUTE FUNCTION reject_complete_organization()")
			if _, e := f.confirm("tail"); e == nil {
				t.Fatal("injected tail succeeded")
			}
			f.n.exec(t, "DROP TRIGGER reject_complete_organization ON "+table)
			f.n.exec(t, "DROP FUNCTION reject_complete_organization()")
			var n int
			e := f.n.f.a.DBPool().QueryRow(context.Background(), `SELECT (SELECT count(*) FROM transfer_plan)+(SELECT count(*) FROM transfer_step)+(SELECT count(*) FROM complete_tv_transfer_evidence)+(SELECT count(*) FROM download_file_inventory)+(SELECT count(*) FROM job WHERE kind='download_organization')`).Scan(&n)
			if e != nil || n != 0 {
				t.Fatal("partial plan", n, e)
			}
		})
	}
	r, e := f.confirm("tail")
	if e != nil {
		t.Fatal(e)
	}
	if e = f.run(t, r); e != nil {
		t.Fatal(e)
	}
	f.assertImported(t, r)
}

// Every wait must observe revocation before returning even a cached receipt.
func TestCompleteTVOrganizationPGRevokedReceipt(t *testing.T) {
	f := newCompleteOrganizationFixture(t)
	if _, e := f.confirm("cached"); e != nil {
		t.Fatal(e)
	}
	tx, e := f.n.f.a.DBPool().Begin(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, e = tx.Exec(context.Background(), `UPDATE app_user SET is_active=false WHERE id=$1`, f.n.actor.ID); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { _, e := f.confirm("cached"); done <- e }()
	select {
	case e := <-done:
		t.Fatal("bypassed account wait", e)
	case <-time.After(50 * time.Millisecond):
	}
	if e = tx.Commit(context.Background()); e != nil {
		t.Fatal(e)
	}
	select {
	case e := <-done:
		if !errors.Is(e, ports.ErrForbidden) {
			t.Fatal(e)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("revocation wait")
	}
}
