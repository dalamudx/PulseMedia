package app

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/library"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"os"
	"path/filepath"
	"testing"
)

func TestSharedMovieOrganizationImportsEachRootAndReleasesGlobalLane(t *testing.T) {
	f := organizationSetup(t)
	ctx := context.Background()
	rootB := uuid.New()
	targetB := filepath.Join(t.TempDir(), "movies-B")
	if e := os.MkdirAll(targetB, 0750); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(targetB, ".pulsemedia_root"), []byte("root identity marker"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := f.q.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: rootB, Path: targetB, MediaType: "movie", Name: "Root B"}); e != nil {
		t.Fatal(e)
	}
	if _, e := library.NewScanner(f.gen, rootfs.NewFilesystem(), postgres.NewScanObservationStore(f.pool)).ScanRoot(ctx, rootB); e != nil {
		t.Fatal(e)
	}
	task, e := f.q.GetDownloadTaskByOpID(ctx, f.op)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.q.CreateDownloadRequirement(ctx, db.CreateDownloadRequirementParams{ID: uuid.New(), OperationID: f.op, UnitID: f.unit, RootID: rootB, ActorType: "user", ActorID: "owner-B"}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.q.CreateUnitReservation(ctx, db.CreateUnitReservationParams{ID: uuid.New(), OperationID: f.op, UnitID: f.unit, RootID: rootB, CandidateID: task.CandidateID, Purpose: "manual_acquisition"}); e != nil {
		t.Fatal(e)
	}
	root, e := f.q.GetLibraryRootByID(ctx, f.root)
	if e != nil {
		t.Fatal(e)
	}
	candidate, e := f.q.GetCandidateByID(ctx, task.CandidateID)
	if e != nil {
		t.Fatal(e)
	}
	lane, e := f.q.CreateAcquisitionLane(ctx, db.CreateAcquisitionLaneParams{ID: uuid.New(), UnitID: f.unit, OperationID: f.op, CandidateID: task.CandidateID, SourceRootID: f.root, SourceGeneration: root.Generation, SourceStamp: quality.AcquisitionSourceStamp(candidate.Title, candidate.InfoHash.String, candidate.LinkEncrypted, candidate.QualityProfile)})
	if e != nil {
		t.Fatal(e)
	}
	if e = f.q.BindPhysicalAcquisitionTask(ctx, db.BindPhysicalAcquisitionTaskParams{OperationID: f.op, TaskID: pgtype.UUID{Bytes: task.ID, Valid: true}}); e != nil {
		t.Fatal(e)
	}
	f.observe(t)
	first := f.claim(t, "root-one")
	if e = f.newService().OrganizeDownload(ctx, first); e != nil {
		t.Fatal("first root", e)
	}
	after, e := f.q.GetDownloadTaskByOpID(ctx, f.op)
	if e != nil || after.OrganizationState != "queued" || after.TransferPlanID.Valid {
		t.Fatal("first import incorrectly completed all roots", after, e)
	}
	var active bool
	if e = f.pool.QueryRow(ctx, "SELECT released_at IS NULL FROM acquisition_reservation WHERE id=$1", lane.ID).Scan(&active); e != nil || !active {
		t.Fatal("first root released global protection", active, e)
	}
	second := f.claim(t, "root-two-after-restart")
	if second.ID != first.ID || second.Token <= first.Token {
		t.Fatal("remaining root was not durable job continuation", first, second)
	}
	if e = f.newService().OrganizeDownload(ctx, second); e != nil {
		t.Fatal("second root after restart", e)
	}
	after, e = f.q.GetDownloadTaskByOpID(ctx, f.op)
	if e != nil || after.OrganizationState != "completed" {
		t.Fatal("all roots not completed", after, e)
	}
	reqs, e := f.q.ListRequirementsForOperation(ctx, f.op)
	if e != nil || len(reqs) != 2 || reqs[0].State != "imported" || reqs[1].State != "imported" {
		t.Fatal("shared demands not independently imported", reqs, e)
	}
	var plans, files, facts, held int
	if e = f.pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM transfer_plan WHERE operation_id=$1),(SELECT count(*) FROM library_file_unit WHERE unit_id=$2),(SELECT count(*) FROM unit_fact WHERE unit_id=$2 AND stage='imported'),(SELECT count(*) FROM acquisition_reservation WHERE id=$3 AND released_at IS NULL)", f.op, f.unit, lane.ID).Scan(&plans, &files, &facts, &held); e != nil || plans != 2 || files != 2 || facts != 2 || held != 0 {
		t.Fatal("false full completion or duplicate artifact", plans, files, facts, held, e)
	}
	paths, e := f.pool.Query(ctx, "SELECT f.root_id,f.rel_path FROM library_file f JOIN library_file_unit fu ON fu.file_id=f.id WHERE fu.unit_id=$1", f.unit)
	if e != nil {
		t.Fatal(e)
	}
	defer paths.Close()
	source, e := os.Stat(f.path)
	if e != nil {
		t.Fatal(e)
	}
	for paths.Next() {
		var id uuid.UUID
		var rel string
		if e = paths.Scan(&id, &rel); e != nil {
			t.Fatal(e)
		}
		base := f.target
		if id == rootB {
			base = targetB
		}
		file, e := os.Stat(filepath.Join(base, rel))
		if e != nil || !os.SameFile(source, file) {
			t.Fatal("actual hardlink identity missing", id, e)
		}
	}
	if e = paths.Err(); e != nil {
		t.Fatal(e)
	}
}
