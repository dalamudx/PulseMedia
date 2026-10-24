package download

import (
	"errors"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

func TestDownloadWatchLegacyNullFenceNeedsFrozenAcquisitionPG(t *testing.T) {
	f := newWatchFixture(t)
	ratePublisherExec(t, f.pool, `UPDATE download_requirement SET submission_fence=NULL WHERE operation_id=$1`, f.task.OperationID)
	hint := ports.RootChangeHint{RelativePath: ".", Kind: ports.RootChangeRescan}
	page, e := f.sources.ResolveDownloadWatchTargets(t.Context(), f.target.Binding, hint, ports.DownloadWatchTargetQuery{})
	if e != nil || len(page.Targets) != 0 {
		t.Fatal("legacy task without frozen acquisition granted observation", e)
	}
	ratePublisherExec(t, f.pool, `INSERT INTO acquisition_reservation(id,unit_id,operation_id,candidate_id,task_id,source_root_id,source_generation,source_stamp)
 SELECT $1,$2,$3,$4,$5,r.id,r.generation,'frozen-source-compatibility' FROM library_root r WHERE r.id=$6`, uuid.New(), f.unit, f.task.OperationID, f.task.CandidateID, f.task.ID, f.root)
	f.target = f.readTarget(t)
	task, e := f.repo.ObserveDownloadWatchTarget(t.Context(), f.target)
	if e != nil || task == nil || task.State != "tracking" || task.Progress != f.info.Progress {
		t.Fatal("frozen legacy observation rejected", e)
	}
	before := f.client.calls.Load()
	ratePublisherExec(t, f.pool, `UPDATE acquisition_reservation SET released_at=clock_timestamp() WHERE operation_id=$1`, f.task.OperationID)
	if _, e = f.repo.ObserveDownloadWatchTarget(t.Context(), f.target); !errors.Is(e, ports.ErrDownloadWatchStale) {
		t.Fatal("released acquisition accepted old target", e)
	}
	if f.client.calls.Load() != before {
		t.Fatal("released acquisition dispatched native I/O")
	}
}
