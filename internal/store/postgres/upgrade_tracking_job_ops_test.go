package postgres_test

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/adapter/torrent"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/jobs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/jackc/pgx/v5/pgxpool"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func trackingJobPool(t *testing.T, f *trackingJobFixture, p *pgxpool.Pool) (ports.MovieUpgradeNativeJobTrackingRepository, *postgres.MovieUpgradeTrackingRuntimeStore, *jobs.Queue) {
	t.Helper()
	s, e := postgres.NewUpgradePreparationStore(p, idgen.New(), torrent.Inspector{})
	if e != nil {
		t.Fatal(e)
	}
	repo, e := postgres.NewMovieUpgradeNativeJobTrackingRepository(s, f.driver, f.issuer)
	if e != nil {
		t.Fatal(e)
	}
	runtime, e := postgres.NewMovieUpgradeTrackingRuntimeStore(s)
	if e != nil {
		t.Fatal(e)
	}
	return repo, runtime, jobs.NewQueue(p, idgen.New(), nativeUpgradeClock{})
}
func TestMovieUpgradeTrackingJobMigrationPG(t *testing.T) {
	f := nativeTrackingFixture(t, "Ok.")
	ctx := context.Background()
	dir := tracking73MigrationDir(t)
	definition := func() string {
		var s string
		if e := f.p.QueryRow(ctx, `SELECT pg_get_functiondef('pulse_assert_upgrade_tracking(uuid)'::regprocedure)`).Scan(&s); e != nil {
			t.Fatal(e)
		}
		return s
	}
	before := definition()
	if v, e := postgres.Migrate(ctx, f.dsn, dir); e != nil || v != 73 {
		t.Fatal(v, e)
	}
	if v, e := postgres.Rollback(ctx, f.dsn, dir); e != nil || v != 72 {
		t.Fatal(v, e)
	}
	if definition() != before {
		t.Fatal("Down did not restore schema72 verbatim")
	}
	if v, e := postgres.Migrate(ctx, f.dsn, dir); e != nil || v != 73 {
		t.Fatal(v, e)
	}
	if out, e := f.repo.TrackMovieUpgrade(ctx, f.actor, f.operation, idgen.New().NewUUID(), "unbound-after-roundtrip", "unbound-after-roundtrip-result"); e != nil || out.Outcome != "completed" {
		t.Fatal("unbound schema72 compatibility", out, e)
	}
}
func TestMovieUpgradeTrackingJobBusinessRestorePG(t *testing.T) {
	f := nativeTrackingJobFixture(t)
	ctx := context.Background()
	_, dsn := pgtest.GetCluster(t).CreateRoles(t, mustFrozenDB(t, f.dsn))
	pool, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	repo, runtime, queue := trackingJobPool(t, f, pool)
	if n, e := runtime.ScheduleMovieUpgradeTracking(ctx); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	j, e := queue.Claim(ctx, []string{ports.MovieUpgradeTrackingJobKind}, "business-tracking", 2*time.Minute)
	if e != nil || j == nil {
		t.Fatal(j, e)
	}
	actor, req, e := runtime.MovieUpgradeTrackingClaim(ctx, ports.JobLease{ID: j.ID, Owner: j.LeaseOwner.String, Token: j.LeaseToken})
	if e != nil {
		t.Fatal(e)
	}
	out, e := repo.TrackMovieUpgradeJob(ctx, actor, req)
	if e != nil || out.Outcome != "completed" {
		t.Fatal(out, e)
	}
	for _, sql := range []string{`ALTER TABLE movie_upgrade_tracking_job ADD COLUMN forbidden int`, `TRUNCATE movie_upgrade_tracking_job`, `TRUNCATE movie_upgrade_tracking_job_claim`, `UPDATE movie_upgrade_tracking_job SET actor_id=gen_random_uuid()`, `DELETE FROM movie_upgrade_tracking_job_claim`} {
		if _, e = pool.Exec(ctx, sql); e == nil {
			t.Fatal("business mutated DDL/history", sql)
		}
	}
	dump := filepath.Join(t.TempDir(), "tracking73.dump")
	frozenPGCommand(t, ctx, "pg_dump", f.dsn, "--no-password", "-Fc", "-f", dump)
	restored := pgtest.GetCluster(t).NewTestDB(t)
	frozenPGCommand(t, ctx, "pg_restore", restored, "--no-password", "--exit-on-error", "--no-owner", "--no-privileges", "-d", mustFrozenDB(t, restored), dump)
	p, e := pgxpool.New(ctx, restored)
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	if e = postgres.CheckSchemaVersion(ctx, p, 73); e != nil {
		t.Fatal(e)
	}
	restoredRepo, restoredRuntime, _ := trackingJobPool(t, f, p)
	queries := f.infos.Load()
	again, e := restoredRepo.TrackMovieUpgradeJob(ctx, actor, req)
	if e != nil || *again != *out || f.infos.Load() != queries {
		t.Fatal("restore reissued query", again, e)
	}
	if n, e := restoredRuntime.ScheduleMovieUpgradeTracking(ctx); e != nil || n != 0 {
		t.Fatal("restored completed rescheduled", n, e)
	}
	if _, e = postgres.Rollback(ctx, restored, filepath.Join("..", "..", "..", "migrations")); e == nil || !strings.Contains(e.Error(), "upgrade tracking job history prevents downgrade") {
		t.Fatal("history downgrade", e)
	}
}
func TestMovieUpgradeTrackingJobDownConcurrentPG(t *testing.T) {
	f := nativeTrackingJobFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	holder, e := f.p.Acquire(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer holder.Release()
	const key int64 = 174885012733
	if _, e = holder.Exec(ctx, `SELECT pg_advisory_lock($1)`, key); e != nil {
		t.Fatal(e)
	}
	defer func() {
		c, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_, _ = holder.Exec(c, `SELECT pg_advisory_unlock($1)`, key)
	}()
	if _, e = f.p.Exec(ctx, `CREATE FUNCTION pulse_test_tracking_job_gate() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(174885012733);RETURN NEW;END $$;CREATE TRIGGER pulse_test_tracking_job_gate AFTER INSERT ON movie_upgrade_tracking_job FOR EACH ROW EXECUTE FUNCTION pulse_test_tracking_job_gate()`); e != nil {
		t.Fatal(e)
	}
	scheduled := make(chan error, 1)
	go func() {
		n, e := f.runtime.ScheduleMovieUpgradeTracking(ctx)
		if e == nil && n != 1 {
			e = ports.ErrMovieUpgradeTrackingReview
		}
		scheduled <- e
	}()
	drained := false
	defer func() {
		if !drained {
			cancel()
			c, stop := context.WithTimeout(context.Background(), 5*time.Second)
			defer stop()
			_, _ = holder.Exec(c, `SELECT pg_advisory_unlock($1)`, key)
			<-scheduled
		}
	}()
	waitPreparationBlock(t, f.p, holder.Conn().PgConn().PID())
	var writer uint32
	if e = f.p.QueryRow(ctx, `SELECT pid FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid))`, holder.Conn().PgConn().PID()).Scan(&writer); e != nil {
		t.Fatal(e)
	}
	down := make(chan error, 1)
	go func() {
		_, e := postgres.Rollback(ctx, f.dsn, filepath.Join("..", "..", "..", "migrations"))
		down <- e
	}()
	downDrained := false
	defer func() {
		if !downDrained {
			cancel()
			<-down
		}
	}()
	waitPreparationBlock(t, f.p, writer)
	if _, e = holder.Exec(ctx, `SELECT pg_advisory_unlock($1)`, key); e != nil {
		t.Fatal(e)
	}
	e = <-down
	downDrained = true
	if e == nil || !strings.Contains(e.Error(), "upgrade tracking job history prevents downgrade") {
		t.Fatal("downgrade lost bound job", e)
	}
	e = <-scheduled
	drained = true
	if e != nil {
		t.Fatal(e)
	}
	if e = postgres.CheckSchemaVersion(ctx, f.p, 73); e != nil {
		t.Fatal(e)
	}
}
func TestMovieUpgradeTrackingJobCooldownPG(t *testing.T) {
	f := nativeTrackingJobFixture(t)
	ctx := context.Background()
	f.complete.Store(false)
	if n, e := f.runtime.ScheduleMovieUpgradeTracking(ctx); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	req := f.claim(t, 2*time.Minute)
	first, e := f.jobRepo.TrackMovieUpgradeJob(ctx, f.actor, req)
	if e != nil || first.Outcome != "tracking" {
		t.Fatal(first, e)
	}
	if n, e := f.runtime.ScheduleMovieUpgradeTracking(ctx); e != nil || n != 0 {
		t.Fatal("early successor", n, e)
	}
	deadline := time.Now().Add(33 * time.Second)
	for {
		var ready bool
		if e = f.p.QueryRow(ctx, `SELECT recorded_at<=clock_timestamp()-interval '30 seconds' FROM movie_upgrade_tracking_result WHERE observation_id=$1`, first.ObservationID).Scan(&ready); e != nil {
			t.Fatal(e)
		}
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cooldown missing")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if n, e := f.runtime.ScheduleMovieUpgradeTracking(ctx); e != nil || n != 1 {
		t.Fatal("no successor", n, e)
	}
	f.complete.Store(true)
	next := f.claim(t, 2*time.Minute)
	if next.Lease.ID == req.Lease.ID {
		t.Fatal("reused original tracking job")
	}
	out, e := f.jobRepo.TrackMovieUpgradeJob(ctx, f.actor, next)
	if e != nil || out.Outcome != "completed" {
		t.Fatal(out, e)
	}
	f.counts(t, 2, 2, 2)
	if n, e := f.runtime.ScheduleMovieUpgradeTracking(ctx); e != nil || n != 0 {
		t.Fatal("completed successor", n, e)
	}
	if f.adds.Load() != 1 {
		t.Fatal("repeat Add")
	}
}
