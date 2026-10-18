package postgres_test

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

func TestMovieUpgradeOrganizationExecutionPermissionWaitPG(t *testing.T) {
	for _, mode := range []string{"admission", "receipt"} {
		t.Run(mode, func(t *testing.T) {
			f, lease, files, r := executionFixture77(t)
			want := 0
			if mode == "receipt" {
				if out, e := r.ExecuteMovieUpgradeOrganization(context.Background(), f.actor, lease); e != nil || out == nil {
					t.Fatal(out, e)
				}
				want = 1
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			holder, e := f.p.Acquire(ctx)
			if e != nil {
				t.Fatal(e)
			}
			defer holder.Release()
			tx, e := holder.Begin(ctx)
			if e != nil {
				t.Fatal(e)
			}
			defer func() { _ = tx.Rollback(context.Background()) }()
			if _, e = tx.Exec(ctx, `UPDATE app_user SET is_active=false WHERE id=$1::uuid`, f.actor.ID); e != nil {
				t.Fatal(e)
			}
			done := make(chan error, 1)
			go func() {
				if mode == "receipt" {
					_, e := r.ReadMovieUpgradeOrganizationExecutionReceipt(ctx, f.actor, lease.ID)
					done <- e
				} else {
					_, e := r.ExecuteMovieUpgradeOrganization(ctx, f.actor, lease)
					done <- e
				}
			}()
			drained := false
			defer func() {
				_ = tx.Rollback(context.Background())
				cancel()
				if !drained {
					<-done
				}
			}()
			waitPreparationBlock(t, f.p, holder.Conn().PgConn().PID())
			if e = tx.Commit(ctx); e != nil {
				t.Fatal(e)
			}
			e = <-done
			drained = true
			if e == nil {
				t.Fatal("revoked account admitted")
			}
			permits, results := executionCounts(t, f)
			if permits != want || results != want || files.calls.Load() != int32(want) {
				t.Fatal(permits, results, files.calls.Load())
			}
		})
	}
}
func TestMovieUpgradeOrganizationExecutionSessionPG(t *testing.T) {
	for _, mode := range []string{"transaction_lock", "shared_session", "nonce_changed"} {
		t.Run(mode, func(t *testing.T) {
			f, lease, files, r := executionFixture77(t)
			mutation := `PERFORM pg_advisory_unlock(NEW.advisory_key);PERFORM pg_advisory_xact_lock(NEW.advisory_key);`
			if mode == "shared_session" {
				mutation = `PERFORM pg_advisory_unlock(NEW.advisory_key);PERFORM pg_advisory_lock_shared(NEW.advisory_key);`
			}
			if mode == "nonce_changed" {
				mutation = `PERFORM set_config('pulsemedia.upgrade_send_nonce','changed',false);`
			}
			f.exec(t, `CREATE FUNCTION native_session_fault()RETURNS TRIGGER LANGUAGE plpgsql AS $$BEGIN `+mutation+`RETURN NEW;END $$;CREATE TRIGGER zz_native_session_fault BEFORE INSERT ON movie_upgrade_organization_execution_permit FOR EACH ROW EXECUTE FUNCTION native_session_fault()`)
			if out, e := r.ExecuteMovieUpgradeOrganization(context.Background(), f.actor, lease); e == nil || out != nil {
				t.Fatal(out, e)
			}
			permits, results := executionCounts(t, f)
			if permits != 0 || results != 0 || files.calls.Load() != 0 {
				t.Fatal(permits, results, files.calls.Load())
			}
		})
	}
}

type delayedExecutionSourceFS struct {
	ports.TransferFilesystem
	sourceRoot, sourceRel string
	entered, resume       chan struct{}
	once                  sync.Once
	calls                 atomic.Int32
}
type delayedExecutionSourceRoot struct {
	ports.TransferRoot
	owner *delayedExecutionSourceFS
}

func (r delayedExecutionSourceRoot) Inspect(ctx context.Context, rel string) (ports.FileIdentity, error) {
	id, e := r.TransferRoot.Inspect(ctx, rel)
	if rel == r.owner.sourceRel {
		r.owner.once.Do(func() { close(r.owner.entered); <-r.owner.resume })
	}
	return id, e
}
func (f *delayedExecutionSourceFS) LockRoots(ctx context.Context, paths ...string) (map[string]ports.TransferRoot, func(), error) {
	roots, release, e := f.TransferFilesystem.LockRoots(ctx, paths...)
	if e != nil {
		return nil, nil, e
	}
	roots[f.sourceRoot] = delayedExecutionSourceRoot{TransferRoot: roots[f.sourceRoot], owner: f}
	return roots, release, nil
}
func (f *delayedExecutionSourceFS) Hardlink(context.Context, ports.TransferRoot, string, ports.TransferRoot, string, ports.FileIdentity) error {
	f.calls.Add(1)
	return ports.ErrFileConflict
}
func TestMovieUpgradeOrganizationExecutionObservationExpiryPG(t *testing.T) {
	f, lease, _, _ := executionFixture77(t)
	relative, e := filepath.Rel(f.sourceRoot, f.sourcePath)
	if e != nil {
		t.Fatal(e)
	}
	files := &delayedExecutionSourceFS{TransferFilesystem: rootfs.NewFilesystem(), sourceRoot: f.sourceRoot, sourceRel: relative, entered: make(chan struct{}), resume: make(chan struct{})}
	r := executionRepository(t, f.p, files)
	ctx, cancel := context.WithTimeout(context.Background(), 55*time.Second)
	defer cancel()
	holder, e := f.p.Acquire(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer holder.Release()
	tx, e := holder.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, e = tx.Exec(ctx, `LOCK TABLE audit_log IN SHARE MODE`); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	drained := false
	resumed := false
	go func() { _, e := r.ExecuteMovieUpgradeOrganization(ctx, f.actor, lease); done <- e }()
	defer func() {
		if !resumed {
			close(files.resume)
		}
		_ = tx.Rollback(context.Background())
		cancel()
		if !drained {
			<-done
		}
	}()
	select {
	case <-files.entered:
	case e = <-done:
		drained = true
		t.Fatal("rejected before source observation", e)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var observed time.Time
	if e = f.p.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&observed); e != nil {
		t.Fatal(e)
	}
	if _, e = f.p.Exec(ctx, `SELECT pg_sleep(26)`); e != nil {
		t.Fatal(e)
	}
	close(files.resume)
	resumed = true
	waitPreparationBlock(t, f.p, holder.Conn().PgConn().PID())
	if _, e = f.p.Exec(ctx, `SELECT pg_sleep(GREATEST(0,30.5-EXTRACT(EPOCH FROM(clock_timestamp()-$1::timestamptz))))`, observed); e != nil {
		t.Fatal(e)
	}
	var expired bool
	if e = f.p.QueryRow(ctx, `SELECT clock_timestamp()>$1::timestamptz+interval '30 seconds'`, observed).Scan(&expired); e != nil || !expired {
		t.Fatal(expired, e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	e = <-done
	drained = true
	if e == nil {
		t.Fatal("expired physical evidence admitted")
	}
	permits, results := executionCounts(t, f)
	if permits != 0 || results != 0 || files.calls.Load() != 0 {
		t.Fatal(permits, results, files.calls.Load())
	}
	if strings.Contains(e.Error(), "deadline") {
		t.Fatal("request timeout masked database freshness", e)
	}
}
