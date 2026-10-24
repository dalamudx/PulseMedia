package library

import (
	"github.com/dalamudx/PulseMedia/internal/ports"
	"testing"
)

func TestUntrackedReviewDeadIsReadOnly(t *testing.T) {
	f := untrackedReviewFixture()
	r := f.repo.review
	r.State = "dead"
	r.Evidence = nil
	r.MaterialHash = ""
	r.RecordedAt = nil
	out, err := f.service.Read(t.Context(), f.actor, f.snapshot.Scope.ID, f.snapshot.File.ID, r.Request.ID)
	if err != nil || out == nil || out.State != "dead" || out.ExecutionAllowed || f.files.opens.Load() != 0 {
		t.Fatal("terminal request should remain readable without IO", err)
	}
	r.Evidence = &ports.UntrackedReviewEvidence{}
	if _, err = f.service.Read(t.Context(), f.actor, f.snapshot.Scope.ID, f.snapshot.File.ID, r.Request.ID); err == nil {
		t.Fatal("dead request accepted trusted evidence")
	}
}
