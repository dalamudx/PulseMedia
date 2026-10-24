package transfer

import (
	"errors"
	"os"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/outbox"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
)

func TestMediaMinimumRechecksPendingPlanBeforeNativeWritePG(t *testing.T) {
	f := safetySetup(t)
	plan, step := f.plan(t)
	ids := idgen.New()
	// A new process can load a plan prepared under a lower deployment minimum.
	repository := NewRepositoryWithMinimumMediaBytes(f.pool, ids, outbox.New(f.pool, ids, clock.New()), rootfs.NewFilesystem(), step.SourceSize+1)
	if err := repository.ExecuteTransfer(t.Context(), plan.ID); !errors.Is(err, ErrReviewRequired) {
		t.Fatal("pending plan bypassed new minimum", err)
	}
	if _, err := os.Lstat(step.TargetPath); !os.IsNotExist(err) {
		t.Fatal("excluded pending plan published target", err)
	}
	f.noImport(t)
	current, err := f.svc.queries.GetTransferPlanByID(t.Context(), plan.ID)
	if err != nil || current.State != "review_required" {
		t.Fatal("pending plan did not retain review state", err)
	}
}
