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

func TestSharedOrganizationOfflineRootKeepsLaneAndImportedRoot(t *testing.T) {
	f := organizationSetup(t)
	ctx := context.Background()
	other := uuid.New()
	path := filepath.Join(t.TempDir(), "offline-B")
	if e := os.MkdirAll(path, 0750); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(path, ".pulsemedia_root"), []byte("root identity marker"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := f.q.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: other, Path: path, MediaType: "movie", Name: "offline B"}); e != nil {
		t.Fatal(e)
	}
	if _, e := library.NewScanner(f.gen, rootfs.NewFilesystem(), postgres.NewScanObservationStore(f.pool)).ScanRoot(ctx, other); e != nil {
		t.Fatal(e)
	}
	task, e := f.q.GetDownloadTaskByOpID(ctx, f.op)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.q.CreateDownloadRequirement(ctx, db.CreateDownloadRequirementParams{ID: uuid.New(), OperationID: f.op, UnitID: f.unit, RootID: other, ActorType: "user", ActorID: "B"}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.q.CreateUnitReservation(ctx, db.CreateUnitReservationParams{ID: uuid.New(), UnitID: f.unit, RootID: other, CandidateID: task.CandidateID, OperationID: f.op, Purpose: "manual_acquisition"}); e != nil {
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
	if _, e = f.pool.Exec(ctx, "UPDATE library_root SET health='offline' WHERE id=$1", other); e != nil {
		t.Fatal(e)
	}
	f.observe(t)
	if e = f.newService().OrganizeDownload(ctx, f.claim(t, "healthy-root-first")); e != nil {
		t.Fatal(e)
	}
	if f.count(t, "library_file_unit") != 1 {
		t.Fatal("healthy first root not actually imported")
	}
	if e = f.newService().OrganizeDownload(ctx, f.claim(t, "offline-root-second")); e != nil {
		t.Fatal(e)
	}
	after, e := f.q.GetDownloadTaskByOpID(ctx, f.op)
	if e != nil || after.OrganizationState != "review_required" {
		t.Fatal("offline target reported overall success", after, e)
	}
	requirements, e := f.q.ListRequirementsForOperation(ctx, f.op)
	if e != nil {
		t.Fatal(e)
	}
	for _, r := range requirements {
		if r.RootID == other && r.State != "pending" {
			t.Fatal("offline root manufactured imported", r.State)
		}
		if r.RootID == f.root && r.State != "imported" {
			t.Fatal("successful root rolled back by unrelated offline root", r.State)
		}
	}
	var active bool
	if e = f.pool.QueryRow(ctx, "SELECT released_at IS NULL FROM acquisition_reservation WHERE id=$1", lane.ID).Scan(&active); e != nil || !active {
		t.Fatal("offline requirement released physical lane", active, e)
	}
	var rootsProtected int
	if e = f.pool.QueryRow(ctx, "SELECT count(*) FROM unit_reservation WHERE root_id=$1 AND state IN ('reserved','dispatched')", other).Scan(&rootsProtected); e != nil || rootsProtected != 1 {
		t.Fatal("unknown offline protection missing", rootsProtected, e)
	}
}
