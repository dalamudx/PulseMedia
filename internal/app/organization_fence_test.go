package app

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/outbox"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	transferstore "github.com/dalamudx/PulseMedia/internal/store/transfer"
	"github.com/dalamudx/PulseMedia/internal/usecase/transfer"
	"github.com/google/uuid"
	"testing"
	"time"
)

type organizationLinkGate struct {
	ports.TransferFilesystem
	reached, release chan struct{}
}

func (g *organizationLinkGate) Hardlink(ctx context.Context, source ports.TransferRoot, sourceRel string, target ports.TransferRoot, targetRel string, identity ports.FileIdentity) error {
	if err := g.TransferFilesystem.Hardlink(ctx, source, sourceRel, target, targetRel, identity); err != nil {
		return err
	}
	close(g.reached)
	select {
	case <-g.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func TestOrganizationExpiredJobCannotCommitOrReleasePhysicalSlot(t *testing.T) {
	f := organizationSetup(t)
	f.observe(t)
	lease := f.claim(t, "old")
	gate := &organizationLinkGate{TransferFilesystem: rootfs.NewFilesystem(), reached: make(chan struct{}), release: make(chan struct{})}
	old := transfer.NewService(transferstore.NewRepository(f.pool, f.gen, outbox.New(f.pool, f.gen, clock.New()), gate, transfer.ExecutePreparedPlan))
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- old.OrganizeDownload(ctx, lease); close(done) }()
	released := false
	defer func() {
		if !released {
			close(gate.release)
		}
		<-done
	}()
	select {
	case <-gate.reached:
	case <-ctx.Done():
		t.Fatal("old worker never reached physical postcondition")
	}
	task, err := f.q.GetDownloadTaskByOpID(ctx, f.op)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, "UPDATE job SET lease_until=statement_timestamp()-interval '1 second' WHERE id=$1", lease.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, "UPDATE transfer_plan SET lease_until=statement_timestamp()-interval '1 second' WHERE id=$1", uuid.UUID(task.TransferPlanID.Bytes)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.q.RecoverExpiredOrganizationJobs(ctx); err != nil {
		t.Fatal(err)
	}
	next := f.claim(t, "new")
	waitCtx, stop := context.WithTimeout(ctx, 70*time.Millisecond)
	err = f.newService().OrganizeDownload(waitCtx, next)
	stop()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("logical expiry stole physical slot: %v", err)
	}
	if f.count(t, "unit_fact") != 0 {
		t.Fatal("new worker committed while old I/O retained slot")
	}
	close(gate.release)
	released = true
	if err := <-done; !errors.Is(err, transfer.ErrExecutionRejected) {
		t.Fatalf("old worker committed under stolen job: %v", err)
	}
	if f.count(t, "unit_fact") != 0 {
		t.Fatal("stale physical result committed")
	}
	if err := f.newService().OrganizeDownload(ctx, next); err != nil {
		t.Fatal(err)
	}
	f.assertImported(t)
}
