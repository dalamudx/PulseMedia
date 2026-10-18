package postgres_test

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/jobs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type organizationJobFixture struct {
	*physicalOrganizationFixture
	repo    ports.MovieUpgradeOrganizationJobRepository
	command ports.QueueMovieUpgradeOrganization
	queue   *jobs.Queue
}

func organizationJobFixture76(t *testing.T) *organizationJobFixture {
	t.Helper()
	f := &organizationJobFixture{physicalOrganizationFixture: physicalOrganizationFixture75(t)}
	p, e := f.preparation.PrepareMovieUpgradeOrganization(context.Background(), f.actor, f.physicalOrganizationFixture.command, "org-job-prepare")
	if e != nil {
		t.Fatal(e)
	}
	if v, e := postgres.Migrate(context.Background(), f.dsn, organization76MigrationDir(t)); e != nil || v != 76 {
		t.Fatal(v, e)
	}
	f.repo, e = postgres.NewMovieUpgradeOrganizationJobRepository(f.trackingFixture.preparation)
	if e != nil {
		t.Fatal(e)
	}
	f.command = ports.QueueMovieUpgradeOrganization{OperationID: f.operation, PreparationID: p.ID, Confirmed: true}
	f.queue = jobs.NewQueue(f.p, idgen.New(), nativeUpgradeClock{})
	return f
}
func (f *organizationJobFixture) queued(t *testing.T) *ports.MovieUpgradeOrganizationJobReceipt {
	t.Helper()
	r, e := f.repo.QueueMovieUpgradeOrganization(context.Background(), f.actor, f.command, "execute-command")
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func (f *organizationJobFixture) lease(t *testing.T, ttl time.Duration) ports.JobLease {
	t.Helper()
	j, e := f.queue.Claim(context.Background(), []string{ports.MovieUpgradeOrganizationJobKind}, "org-executor-test", ttl)
	if e != nil || j == nil {
		t.Fatal(j, e)
	}
	return ports.JobLease{ID: j.ID, Owner: j.LeaseOwner.String, Token: j.LeaseToken}
}
func TestMovieUpgradeOrganizationJobCommandPG(t *testing.T) {
	f := organizationJobFixture76(t)
	ctx := context.Background()
	before := nativeProtectedGraph(t, f.preparedUpgradeFixture)
	var mu sync.Mutex
	var first *ports.MovieUpgradeOrganizationJobReceipt
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := f.repo.QueueMovieUpgradeOrganization(ctx, f.actor, f.command, "execute-command")
			mu.Lock()
			defer mu.Unlock()
			if e != nil || r == nil || r.ExecutionAllowed || r.Status != "queued" {
				t.Error(r, e)
				return
			}
			if first == nil {
				first = r
			} else if *first != *r {
				t.Error("different receipt")
			}
		}()
	}
	wg.Wait()
	if t.Failed() {
		return
	}
	var n int
	if e := f.p.QueryRow(ctx, `SELECT count(*) FROM movie_upgrade_organization_job`).Scan(&n); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	lease := f.lease(t, time.Minute)
	captured, e := f.repo.CaptureMovieUpgradeOrganizationJob(ctx, f.actor, lease)
	if e != nil || captured == nil || captured.ExecutionAllowed || captured.Status != "claim_captured_not_executable" {
		t.Fatal(captured, e)
	}
	if _, e = f.repo.CaptureMovieUpgradeOrganizationJob(ctx, f.actor, lease); e == nil {
		t.Fatal("duplicate capture")
	}
	for _, query := range []string{`UPDATE job SET lease_until=lease_until+interval '1 second' WHERE id=$1`, `UPDATE job SET lease_owner='other' WHERE id=$1`, `UPDATE job SET state='queued',lease_owner=NULL,lease_until=NULL WHERE id=$1`, `UPDATE job SET state='succeeded' WHERE id=$1`, `DELETE FROM movie_upgrade_organization_job_claim WHERE job_id=$1`, `UPDATE transfer_plan SET state='approved',requires_approval=false WHERE id=(SELECT plan_id FROM movie_upgrade_organization_preparation WHERE id=(SELECT preparation_id FROM movie_upgrade_organization_job WHERE job_id=$1))`} {
		if _, e = f.p.Exec(ctx, query, lease.ID); e == nil {
			t.Fatal("mutation allowed", query)
		}
	}
	if e = f.queue.Fail(ctx, lease.ID, lease.Owner, lease.Token, "manual review", time.Second); e != nil {
		t.Fatal("safe terminal through actual queue", e)
	}
	if _, e = f.repo.CaptureMovieUpgradeOrganizationJob(ctx, f.actor, lease); e == nil {
		t.Fatal("terminal takeover")
	}
	f.exec(t, `UPDATE subscription SET upgrade_mode='disabled',intent_version=intent_version+1 WHERE id=$1`, f.sub)
	if e = os.RemoveAll(f.sourceRoot); e != nil {
		t.Fatal(e)
	}
	recovered, e := f.repo.ReadMovieUpgradeOrganizationJobReceipt(ctx, f.actor, f.operation, "execute-command")
	if e != nil || recovered == nil || *recovered != *first {
		t.Fatal(recovered, e)
	}
	if _, e = f.repo.QueueMovieUpgradeOrganization(ctx, f.actor, f.command, "different-command"); e == nil {
		t.Fatal("second command")
	}
	after := nativeProtectedGraph(t, f.preparedUpgradeFixture)
	if after != before {
		t.Fatal("protected graph changed")
	}
	if f.adds.Load() != 1 {
		t.Fatal("extra Add")
	}
	var target string
	if e = f.p.QueryRow(ctx, `SELECT target_rel FROM transfer_step WHERE plan_id=(SELECT plan_id FROM movie_upgrade_organization_preparation WHERE id=$1)`, f.command.PreparationID).Scan(&target); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Lstat(filepath.Join(f.rootPath, target)); !os.IsNotExist(e) {
		t.Fatal("filesystem execution", e)
	}
	var planState string
	if e = f.p.QueryRow(ctx, `SELECT state FROM transfer_plan WHERE id=(SELECT plan_id FROM movie_upgrade_organization_preparation WHERE id=$1)`, f.command.PreparationID).Scan(&planState); e != nil || planState != "review_required" {
		t.Fatal(planState, e)
	}
}
func TestMovieUpgradeOrganizationJobMigrationPG(t *testing.T) {
	ctx := context.Background()
	dsn := pgtest.GetCluster(t).NewTestDB(t)
	if v, e := postgres.Migrate(ctx, dsn, organization75MigrationDir(t)); e != nil || v != 75 {
		t.Fatal(v, e)
	}
	if v, e := postgres.Migrate(ctx, dsn, organization76MigrationDir(t)); e != nil || v != 76 {
		t.Fatal(v, e)
	}
	if v, e := postgres.Rollback(ctx, dsn, organization76MigrationDir(t)); e != nil || v != 75 {
		t.Fatal(v, e)
	}
	if v, e := postgres.Migrate(ctx, dsn, organization76MigrationDir(t)); e != nil || v != 76 {
		t.Fatal(v, e)
	}
}
