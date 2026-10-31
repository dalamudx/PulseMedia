package httpapi_test

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/outbox"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	transferstore "github.com/dalamudx/PulseMedia/internal/store/transfer"
	"github.com/dalamudx/PulseMedia/internal/usecase/transfer"
	"github.com/jackc/pgx/v5"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type completeBlockingFS struct {
	ports.TransferFilesystem
	entered, release chan struct{}
	calls            atomic.Int32
}

func (g *completeBlockingFS) Hardlink(ctx context.Context, source ports.TransferRoot, rel string, target ports.TransferRoot, dest string, identity ports.FileIdentity) error {
	e := g.TransferFilesystem.Hardlink(ctx, source, rel, target, dest, identity)
	if g.calls.Add(1) == 1 {
		close(g.entered)
		<-g.release
	} // deliberately ignore cancellation
	return e
}
func TestCompleteTVOrganizationPGExpiredLeaseRetainsPhysicalSlot(t *testing.T) {
	f := newCompleteOrganizationFixture(t)
	r, e := f.confirm("physical")
	if e != nil {
		t.Fatal(e)
	}
	pool := f.n.f.a.DBPool()
	gen := idgen.New()
	gate := &completeBlockingFS{TransferFilesystem: rootfs.NewFilesystem(), entered: make(chan struct{}), release: make(chan struct{})}
	old := transfer.NewService(transferstore.NewRepository(pool, gen, outbox.New(pool, gen, clock.New()), gate, transfer.ExecutePreparedPlan))
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	oldCtx, cancelOld := context.WithCancel(ctx)
	defer cancelOld()
	f.n.exec(t, `UPDATE job SET state='running',lease_owner='old-full',lease_token=77,lease_until=clock_timestamp()+interval '1 minute' WHERE id=$1`, r.JobID)
	doneOld := make(chan error, 1)
	go func() {
		doneOld <- old.OrganizeDownload(oldCtx, ports.JobLease{ID: r.JobID, Owner: "old-full", Token: 77})
	}()
	var once sync.Once
	release := func() { once.Do(func() { close(gate.release) }) }
	defer release()
	select {
	case <-gate.entered:
	case <-ctx.Done():
		t.Fatal("old full plan did not enter hardlink")
	}
	f.n.exec(t, `UPDATE transfer_plan SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, r.PlanID)
	f.n.exec(t, `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, r.JobID)
	if _, e = postgres.Queries(pool).GuardDownloadOrganizationJob(ctx, db.GuardDownloadOrganizationJobParams{ID: r.JobID, Owner: "old-full", Token: 77}); !errors.Is(e, pgx.ErrNoRows) {
		t.Fatal("expired token accepted", e)
	}
	cancelOld()
	f.n.exec(t, `UPDATE job SET lease_owner='new-full',lease_token=78,lease_until=clock_timestamp()+interval '1 minute' WHERE id=$1`, r.JobID)
	next := transfer.NewService(transferstore.NewRepository(pool, gen, outbox.New(pool, gen, clock.New()), gate, transfer.ExecutePreparedPlan))
	doneNext := make(chan error, 1)
	go func() {
		doneNext <- next.OrganizeDownload(ctx, ports.JobLease{ID: r.JobID, Owner: "new-full", Token: 78})
	}()
	select {
	case e := <-doneNext:
		t.Fatal("replacement bypassed occupied root slot", e)
	case <-time.After(75 * time.Millisecond):
	}
	if gate.calls.Load() != 1 {
		t.Fatal("overlapping hardlink", gate.calls.Load())
	}
	release()
	select {
	case <-doneOld:
	case <-ctx.Done():
		t.Fatal("old did not drain")
	}
	select {
	case e := <-doneNext:
		if e != nil {
			t.Fatal("replacement did not recover", e)
		}
	case <-ctx.Done():
		t.Fatal("replacement timeout")
	}
	f.assertImported(t, r)
}

func TestCompleteTVOrganizationPGEmptyMigrationRoundTrip(t *testing.T) {
	f := newCompleteOrganizationFixture(t)
	ctx := context.Background()
	if e := applyCompleteTVOrganizationDDL(t, ctx, f.n.f.a.DBPool(), false); e != nil {
		t.Fatal("empty schema63 down", e)
	}
	var absent bool
	if e := f.n.f.a.DBPool().QueryRow(ctx, `SELECT to_regclass('complete_tv_transfer_evidence') IS NULL`).Scan(&absent); e != nil || !absent {
		t.Fatal("schema63 remained", absent, e)
	}
	if e := applyCompleteTVOrganizationDDL(t, ctx, f.n.f.a.DBPool(), true); e != nil {
		t.Fatal("schema63 re-up", e)
	}
	r, e := f.confirm("after-roundtrip")
	if e != nil {
		t.Fatal("confirm after roundtrip", e)
	}
	if e = f.run(t, r); e != nil {
		t.Fatal("worker after roundtrip", e)
	}
	f.assertImported(t, r)
}

func TestCompleteTVOrganizationPGDowngradeWaitsForPublication(t *testing.T) {
	f := newCompleteOrganizationFixture(t)
	pool := f.n.f.a.DBPool()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	blocker, e := pool.Acquire(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer blocker.Release()
	if _, e = blocker.Exec(ctx, `SELECT pg_advisory_lock(90063)`); e != nil {
		t.Fatal(e)
	}
	var once sync.Once
	release := func() {
		once.Do(func() { _, _ = blocker.Exec(context.Background(), `SELECT pg_advisory_unlock(90063)`) })
	}
	defer release()
	f.n.exec(t, `CREATE FUNCTION hold_complete_publication() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.action='complete_tv.organization.confirm' THEN PERFORM pg_advisory_xact_lock(90063); END IF;RETURN NEW;END$$`)
	f.n.exec(t, `CREATE TRIGGER hold_complete_publication BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION hold_complete_publication()`)
	done := make(chan error, 1)
	go func() { _, e := f.service.Confirm(ctx, f.n.actor, f.command, "downgrade"); done <- e }()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		if e = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND objid=90063 AND NOT granted)`).Scan(&waiting); e != nil {
			t.Fatal(e)
		}
		if waiting {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("confirmation did not reach publication")
		case <-ticker.C:
		}
	}
	down := make(chan error, 1)
	go func() { down <- applyCompleteTVOrganizationDDL(t, ctx, pool, false) }()
	select {
	case e := <-down:
		t.Fatal("downgrade did not wait for in-flight evidence", e)
	case <-time.After(75 * time.Millisecond):
	}
	release()
	select {
	case e := <-done:
		if e != nil {
			t.Fatal("confirmation", e)
		}
	case <-ctx.Done():
		t.Fatal("confirmation timeout")
	}
	select {
	case e := <-down:
		if e == nil {
			t.Fatal("downgrade discarded committed evidence")
		}
	case <-ctx.Done():
		t.Fatal("downgrade timeout")
	}
	f.n.exec(t, `DROP TRIGGER hold_complete_publication ON audit_log`)
	f.n.exec(t, `DROP FUNCTION hold_complete_publication()`)
	var count int
	if e = pool.QueryRow(ctx, `SELECT count(*) FROM complete_tv_transfer_evidence`).Scan(&count); e != nil || count != 1 {
		t.Fatal("evidence missing", count, e)
	}
}
