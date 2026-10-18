package postgres_test

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/adapter/torrent"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
	"sync"
	"testing"
	"time"
)

func physicalPlanRepository(t *testing.T, p *pgxpool.Pool, fs ports.RootFilesystem) ports.MovieUpgradeOrganizationPreparationRepository {
	t.Helper()
	store, e := postgres.NewUpgradePreparationStore(p, idgen.New(), torrent.Inspector{})
	if e != nil {
		t.Fatal(e)
	}
	r, e := postgres.NewMovieUpgradeOrganizationPreparationRepository(store, fs)
	if e != nil {
		t.Fatal(e)
	}
	return r
}

type physicalBarrierFS struct {
	ports.RootFilesystem
	path                       string
	entered, proceed, released chan struct{}
	once, releaseOnce          sync.Once
}
type physicalBarrierRoot struct {
	ports.TransferRoot
	fs *physicalBarrierFS
}

func (r *physicalBarrierRoot) Inspect(ctx context.Context, rel string) (ports.FileIdentity, error) {
	r.fs.once.Do(func() { close(r.fs.entered); <-r.fs.proceed })
	return r.TransferRoot.Inspect(context.WithoutCancel(ctx), rel)
}
func (f *physicalBarrierFS) LockRoots(ctx context.Context, paths ...string) (map[string]ports.TransferRoot, func(), error) {
	roots, release, e := f.RootFilesystem.LockRoots(ctx, paths...)
	if e != nil {
		return nil, nil, e
	}
	if r := roots[f.path]; r != nil {
		roots[f.path] = &physicalBarrierRoot{r, f}
	}
	return roots, func() { release(); f.releaseOnce.Do(func() { close(f.released) }) }, nil
}
func physicalBarrier(f *physicalOrganizationFixture) *physicalBarrierFS {
	return &physicalBarrierFS{RootFilesystem: rootfs.NewFilesystem(), path: f.sourceRoot, entered: make(chan struct{}), proceed: make(chan struct{}), released: make(chan struct{})}
}
func TestMovieUpgradeOrganizationPhysicalCancellationPG(t *testing.T) {
	f := physicalOrganizationFixture75(t)
	fs := physicalBarrier(f)
	r := physicalPlanRepository(t, f.p, fs)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	var once sync.Once
	release := func() { once.Do(func() { close(fs.proceed) }) }
	defer release()
	go func() {
		_, e := r.PrepareMovieUpgradeOrganization(ctx, f.actor, f.command, "cancel-physical")
		done <- e
	}()
	select {
	case <-fs.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("physical observation not entered")
	}
	cancel()
	select {
	case e := <-done:
		t.Fatal("returned before physical observation", e)
	case <-fs.released:
		t.Fatal("root slot released while observation runs")
	case <-time.After(75 * time.Millisecond):
	}
	release()
	select {
	case e := <-done:
		if e == nil {
			t.Fatal("cancelled preparation committed")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("physical observation did not drain")
	}
	select {
	case <-fs.released:
	default:
		t.Fatal("drained roots retained")
	}
	f.planCount(t, 0)
}
func TestMovieUpgradeOrganizationPhysicalFreshnessPG(t *testing.T) {
	f := physicalOrganizationFixture75(t)
	fs := physicalBarrier(f)
	r := physicalPlanRepository(t, f.p, fs)
	ctx, cancel := context.WithTimeout(context.Background(), 65*time.Second)
	defer cancel()
	done := make(chan error, 1)
	var once sync.Once
	release := func() { once.Do(func() { close(fs.proceed) }) }
	defer release()
	go func() {
		_, e := r.PrepareMovieUpgradeOrganization(ctx, f.actor, f.command, "expired-observation")
		done <- e
	}()
	select {
	case <-fs.entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var start, now time.Time
	if e := f.p.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&start); e != nil {
		t.Fatal(e)
	}
	timer := time.NewTimer(31 * time.Second)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if e := f.p.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); e != nil || now.Sub(start) < 30*time.Second {
		t.Fatal("freshness boundary not crossed", e)
	}
	release()
	select {
	case e := <-done:
		if e == nil {
			t.Fatal("stale physical observation committed")
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	f.planCount(t, 0)
}
func physicalWaitSQL(t *testing.T, ctx context.Context, p *pgxpool.Pool, query string) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		if e := p.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND wait_event_type='Lock' AND query ILIKE '%'||$1||'%')`, query).Scan(&waiting); e != nil {
			t.Fatal(e)
		}
		if waiting {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
		}
	}
}
func TestMovieUpgradeOrganizationPhysicalAccountWaitPG(t *testing.T) {
	for _, receipt := range []bool{false, true} {
		name := "preparation"
		if receipt {
			name = "receipt"
		}
		t.Run(name, func(t *testing.T) {
			f := physicalOrganizationFixture75(t)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if receipt {
				if _, e := f.preparation.PrepareMovieUpgradeOrganization(ctx, f.actor, f.command, "permission-wait"); e != nil {
					t.Fatal(e)
				}
			}
			tx, e := f.p.Begin(ctx)
			if e != nil {
				t.Fatal(e)
			}
			defer func() { _ = tx.Rollback(context.Background()) }()
			if _, e = tx.Exec(ctx, `SELECT id FROM app_user WHERE id=$1 FOR UPDATE`, f.actor.ID); e != nil {
				t.Fatal(e)
			}
			done := make(chan error, 1)
			go func() {
				var e error
				if receipt {
					_, e = f.preparation.ReadMovieUpgradeOrganizationPreparationReceipt(ctx, f.actor, f.operation, "permission-wait")
				} else {
					_, e = f.preparation.PrepareMovieUpgradeOrganization(ctx, f.actor, f.command, "permission-wait")
				}
				done <- e
			}()
			physicalWaitSQL(t, ctx, f.p, "app_user")
			if _, e = tx.Exec(ctx, `UPDATE app_user SET is_active=false WHERE id=$1`, f.actor.ID); e != nil {
				t.Fatal(e)
			}
			if e = tx.Commit(ctx); e != nil {
				t.Fatal(e)
			}
			if e = <-done; e == nil {
				t.Fatal("revoked account passed after lock wait")
			}
			want := 0
			if receipt {
				want = 1
			}
			f.planCount(t, want)
		})
	}
}
