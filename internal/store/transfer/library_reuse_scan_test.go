package transfer

import (
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
)

func TestLibraryReusePreviewCapPG(t *testing.T) {
	f, r, a, target, file := reusePreviewFixture(t)
	_, e := f.pool.Exec(t.Context(), `WITH copies AS (
 INSERT INTO library_file(id,root_id,rel_path,size_bytes,mtime_ns,file_fingerprint)
 SELECT gen_random_uuid(),root_id,'candidate-'||n||'.mkv',size_bytes,mtime_ns,file_fingerprint
 FROM library_file CROSS JOIN generate_series(1,128)n WHERE id=$1 RETURNING id)
 INSERT INTO library_file_unit(file_id,unit_id) SELECT id,$2 FROM copies`, file, target.UnitID)
	if e != nil {
		t.Fatal(e)
	}
	p := reusePreview(t, r, a, target)
	if p.Complete || p.CanConfirm || len(p.Candidates) != 128 {
		t.Fatal("truncated preview admitted confirmation", p)
	}
}

func TestLibraryReuseOldScanWatermarkPG(t *testing.T) {
	f, r, a, target, file := reusePreviewFixture(t)
	c := reuseCommand(t, r, a, target, file, "hardlink")
	run, e := f.q.StartScanRun(t.Context(), db.StartScanRunParams{ID: target.RootID, ID_2: uuid.New()})
	if e != nil {
		t.Fatal(e)
	}
	root, e := f.q.GetLibraryRootByID(t.Context(), target.RootID)
	if e != nil {
		t.Fatal(e)
	}
	receipt, e := r.ConfirmLibraryReuse(t.Context(), a, c, "old-scan")
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
	imported, e := f.q.GetActiveFileByPath(t.Context(), db.GetActiveFileByPathParams{RootID: root.ID, RelPath: c.TargetRel})
	if e != nil {
		t.Fatal(e)
	}
	if imported.LastMutationSeq <= run.StartMutationSeq {
		t.Fatal("no post-scan watermark")
	}
	store := postgres.NewScanObservationStore(f.pool)
	input := ports.ScanObservation{RunID: run.ID, RootID: root.ID, ObservationID: uuid.New(), NewFileID: uuid.New(), Path: root.Path, MediaType: root.MediaType, MarkerFilename: root.MarkerFilename, Generation: root.Generation, HealthRevision: root.HealthRevision, ScanToken: run.ScanToken, StartMutationSeq: run.StartMutationSeq, RelPath: c.TargetRel, Identity: ports.FileIdentity{Size: 1, Mtime: 1, Inode: 1234}}
	if _, e = store.Record(t.Context(), input); e != nil {
		t.Fatal(e)
	}
	after, e := f.q.GetActiveFileByPath(t.Context(), db.GetActiveFileByPathParams{RootID: root.ID, RelPath: c.TargetRel})
	if e != nil {
		t.Fatal(e)
	}
	if after != imported || uuid.UUID(p.TargetFileID.Bytes) != after.ID {
		t.Fatal("old scan replaced imported incarnation")
	}
	if v := reusePreview(t, r, a, target); !v.AlreadyOwned {
		t.Fatal("old scan revoked current ownership", v)
	}
}
