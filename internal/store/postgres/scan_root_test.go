package postgres_test

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"testing"
	"time"
)

func interruptSeal(f observationFixture) ports.ScanSeal {
	in := f.input
	return ports.ScanSeal{RunID: in.RunID, RootID: in.RootID, Path: in.Path, MediaType: in.MediaType, MarkerFilename: in.MarkerFilename, Generation: in.Generation, HealthRevision: in.HealthRevision, ScanToken: in.ScanToken, StartMutationSeq: in.StartMutationSeq}
}
func interruptState(t *testing.T, f observationFixture) string {
	t.Helper()
	var row string
	if err := f.pool.QueryRow(context.Background(), `SELECT jsonb_build_object('root',(SELECT to_jsonb(r) FROM library_root r WHERE id=$1),'run',(SELECT to_jsonb(s) FROM scan_run s WHERE id=$2))::text`, f.root, f.input.RunID).Scan(&row); err != nil {
		t.Fatal(err)
	}
	return row
}
func TestScanRootInterruptFaultIsAtomicAndRecoverable(t *testing.T) {
	f := newObservationFixture(t)
	ctx := context.Background()
	in := interruptSeal(f)
	_, err := f.pool.Exec(ctx, `CREATE FUNCTION reject_interrupt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic interruption fault'; END $$; CREATE TRIGGER reject_interrupt BEFORE UPDATE ON scan_run FOR EACH ROW EXECUTE FUNCTION reject_interrupt()`)
	if err != nil {
		t.Fatal(err)
	}
	before := interruptState(t, f)
	facts := f.snapshot(t)
	if err = f.store.Interrupt(ctx, in); err == nil {
		t.Fatal("faulted interruption succeeded")
	}
	if interruptState(t, f) != before || f.snapshot(t) != facts {
		t.Fatal("fault left partial state")
	}
	if _, err = f.pool.Exec(ctx, "DROP TRIGGER reject_interrupt ON scan_run"); err != nil {
		t.Fatal(err)
	}
	if err = f.store.Interrupt(ctx, in); err != nil {
		t.Fatal(err)
	}
	var state string
	if err = f.pool.QueryRow(ctx, "SELECT state FROM scan_run WHERE id=$1", in.RunID).Scan(&state); err != nil || state != "partial" {
		t.Fatal(state, err)
	}
	before = interruptState(t, f)
	if err = f.store.Interrupt(ctx, in); !errors.Is(err, ports.ErrScanObservationStale) {
		t.Fatal("terminal replay accepted", err)
	}
	if interruptState(t, f) != before || f.snapshot(t) != facts {
		t.Fatal("terminal replay changed history")
	}
}
func TestScanRootInterruptRechecksAuthorityAfterRootWait(t *testing.T) {
	for _, change := range []struct{ name, sql string }{
		{"path", "UPDATE library_root SET path=path||'-replacement' WHERE id=$1"},
		{"health", "UPDATE library_root SET health='offline' WHERE id=$1"},
		{"generation", "UPDATE library_root SET generation=generation+1 WHERE id=$1"},
		{"revision", "UPDATE library_root SET health_revision=health_revision+1 WHERE id=$1"},
		{"token", "UPDATE library_root SET scan_token=scan_token+1 WHERE id=$1"},
	} {
		t.Run(change.name, func(t *testing.T) {
			f := newObservationFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			holder, err := f.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = holder.Exec(ctx, "SELECT id FROM library_root WHERE id=$1 FOR UPDATE", f.root); err != nil {
				_ = holder.Rollback(context.Background())
				t.Fatal(err)
			}
			out := make(chan error, 1)
			finished := make(chan struct{})
			go func() { defer close(finished); out <- f.store.Interrupt(ctx, interruptSeal(f)) }()
			defer func() {
				cancel()
				_ = holder.Rollback(context.Background())
				select {
				case <-finished:
				case <-time.After(time.Second):
					t.Error("interruption did not drain")
				}
			}()
			deadline := time.Now().Add(time.Second)
			for {
				var waiting int
				err = f.pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%library_root%'`).Scan(&waiting)
				if err != nil {
					t.Fatal(err)
				}
				if waiting > 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("interruption did not wait on root")
				}
				time.Sleep(5 * time.Millisecond)
			}
			if _, err = holder.Exec(ctx, change.sql, f.root); err != nil {
				t.Fatal(err)
			}
			if err = holder.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			before := interruptState(t, f)
			facts := f.snapshot(t)
			if err = <-out; !errors.Is(err, ports.ErrScanObservationStale) {
				t.Fatal("stale interruption accepted", err)
			}
			if interruptState(t, f) != before || f.snapshot(t) != facts {
				t.Fatal("stale interruption changed current root or history")
			}
		})
	}
}
