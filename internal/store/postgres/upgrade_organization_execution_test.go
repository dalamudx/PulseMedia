package postgres_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/torrent"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/jackc/pgx/v5/pgxpool"
)

type nativeLinkFS struct {
	ports.TransferFilesystem
	calls atomic.Int32
}

func (f *nativeLinkFS) Hardlink(ctx context.Context, src ports.TransferRoot, sr string, dst ports.TransferRoot, tr string, id ports.FileIdentity) error {
	f.calls.Add(1)
	return f.TransferFilesystem.Hardlink(ctx, src, sr, dst, tr, id)
}
func executionRepository(t *testing.T, p *pgxpool.Pool, files ports.TransferFilesystem) ports.MovieUpgradeOrganizationExecutionRepository {
	t.Helper()
	r, e := postgres.NewMovieUpgradeOrganizationExecutionRepository(nativePreparationStore(t, p), files)
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func nativePreparationStore(t *testing.T, p *pgxpool.Pool) *postgres.UpgradePreparationStore {
	t.Helper()
	store, e := postgres.NewUpgradePreparationStore(p, idgen.New(), torrent.Inspector{})
	if e != nil {
		t.Fatal(e)
	}
	return store
}
func executionFixture77(t *testing.T, ttl ...time.Duration) (*organizationJobFixture, ports.JobLease, *nativeLinkFS, ports.MovieUpgradeOrganizationExecutionRepository) {
	t.Helper()
	f := organizationJobFixture76(t)
	if v, e := postgres.Migrate(context.Background(), f.dsn, organization77MigrationDir(t)); e != nil || v != 77 {
		t.Fatal(v, e)
	}
	f.queued(t)
	duration := 2 * time.Minute
	if len(ttl) > 0 {
		duration = ttl[0]
	}
	lease := f.lease(t, duration)
	if r, e := f.repo.CaptureMovieUpgradeOrganizationJob(context.Background(), f.actor, lease); e != nil || r == nil {
		t.Fatal(r, e)
	}
	files := &nativeLinkFS{TransferFilesystem: rootfs.NewFilesystem()}
	r, e := postgres.NewMovieUpgradeOrganizationExecutionRepository(f.trackingFixture.preparation, files)
	if e != nil {
		t.Fatal(e)
	}
	return f, lease, files, r
}
func executionTarget(t *testing.T, f *organizationJobFixture) string {
	t.Helper()
	var rel string
	if e := f.p.QueryRow(context.Background(), `SELECT target_rel FROM transfer_step WHERE plan_id=(SELECT plan_id FROM movie_upgrade_organization_preparation WHERE id=$1)`, f.command.PreparationID).Scan(&rel); e != nil {
		t.Fatal(e)
	}
	return filepath.Join(f.rootPath, rel)
}
func TestMovieUpgradeOrganizationExecutionNativePG(t *testing.T) {
	f, lease, files, r := executionFixture77(t)
	ctx := context.Background()
	before := nativeProtectedGraph(t, f.preparedUpgradeFixture)
	var first *ports.MovieUpgradeOrganizationExecutionResult
	var mu sync.Mutex
	var wg sync.WaitGroup
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, e := r.ExecuteMovieUpgradeOrganization(ctx, f.actor, lease)
			mu.Lock()
			defer mu.Unlock()
			if e != nil {
				return
			}
			if first == nil {
				first = out
			} else if out == nil || *first != *out {
				t.Error("different native return", out)
			}
		}()
	}
	wg.Wait()
	if first == nil || first.Status != "linked_pending_ingestion" || first.ExecutionAllowed || files.calls.Load() != 1 {
		t.Fatal(first, files.calls.Load())
	}
	src, e := os.Stat(f.sourcePath)
	if e != nil {
		t.Fatal(e)
	}
	dst, e := os.Stat(executionTarget(t, f))
	if e != nil || !os.SameFile(src, dst) {
		t.Fatal("not exact hardlink", e)
	}
	var valid bool
	if e = f.p.QueryRow(ctx, `SELECT j.state='succeeded' AND to_jsonb(j)=r.finished_job AND p.operation_id=$2 AND r.status='linked_pending_ingestion' FROM job j JOIN movie_upgrade_organization_execution_result r ON r.job_id=j.id JOIN movie_upgrade_organization_execution_permit p USING(permit_id) WHERE j.id=$1`, lease.ID, f.operation).Scan(&valid); e != nil || !valid {
		t.Fatal(valid, e)
	}
	after := nativeProtectedGraph(t, f.preparedUpgradeFixture)
	if before != after {
		t.Fatal("old task/file/facts/protections changed")
	}
	for _, query := range []string{`DELETE FROM movie_upgrade_organization_execution_permit`, `UPDATE movie_upgrade_organization_execution_result SET status='manual_review'`, `TRUNCATE movie_upgrade_organization_execution_permit CASCADE`, `UPDATE transfer_plan SET state='approved',requires_approval=false`} {
		if _, e = f.p.Exec(ctx, query); e == nil {
			t.Fatal("history mutation", query)
		}
	}
	if e = os.RemoveAll(f.sourceRoot); e != nil {
		t.Fatal(e)
	}
	f.exec(t, `UPDATE subscription SET upgrade_mode='disabled',intent_version=intent_version+1 WHERE id=$1`, f.sub)
	got, e := r.ReadMovieUpgradeOrganizationExecutionReceipt(ctx, f.actor, lease.ID)
	if e != nil || got == nil || *got != *first {
		t.Fatal(got, e)
	}
	got, e = r.ExecuteMovieUpgradeOrganization(ctx, f.actor, lease)
	if e != nil || got == nil || *got != *first || files.calls.Load() != 1 {
		t.Fatal("resend after recovery", got, e)
	}
	if _, e = postgres.Rollback(ctx, f.dsn, organization77MigrationDir(t)); e == nil || !strings.Contains(e.Error(), "upgrade organization physical history prevents downgrade") {
		t.Fatal(e)
	}
}
func TestMovieUpgradeOrganizationExecutionMigrationPG(t *testing.T) {
	ctx := context.Background()
	dsn := pgtest.GetCluster(t).NewTestDB(t)
	if v, e := postgres.Migrate(ctx, dsn, organization76MigrationDir(t)); e != nil || v != 76 {
		t.Fatal(v, e)
	}
	p, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	read := func() string {
		var text string
		if e := p.QueryRow(ctx, `SELECT string_agg(pg_get_functiondef(oid),'\n' ORDER BY proname) FROM pg_proc WHERE proname IN ('pulse_assert_upgrade_organization_job','pulse_upgrade_organization_job_update')`).Scan(&text); e != nil {
			t.Fatal(e)
		}
		return text
	}
	before := read()
	if v, e := postgres.Migrate(ctx, dsn, organization77MigrationDir(t)); e != nil || v != 77 {
		t.Fatal(v, e)
	}
	if v, e := postgres.Rollback(ctx, dsn, organization77MigrationDir(t)); e != nil || v != 76 {
		t.Fatal(v, e)
	}
	if before != read() {
		t.Fatal("Down did not restore original76 functions")
	}
	if v, e := postgres.Migrate(ctx, dsn, organization77MigrationDir(t)); e != nil || v != 77 {
		t.Fatal(v, e)
	}
}
