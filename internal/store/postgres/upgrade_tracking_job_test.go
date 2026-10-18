package postgres_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/jobs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
)

type trackingJobFixture struct {
	*trackingFixture
	runtime *postgres.MovieUpgradeTrackingRuntimeStore
	jobRepo ports.MovieUpgradeNativeJobTrackingRepository
	queue   *jobs.Queue
}

func nativeTrackingJobFixture(t *testing.T, beforeFreeze ...func(*frozenUpgradeFixture)) *trackingJobFixture {
	t.Helper()
	f := &trackingJobFixture{trackingFixture: nativeTrackingFixture(t, "Ok.", beforeFreeze...)}
	if v, e := postgres.Migrate(context.Background(), f.dsn, tracking73MigrationDir(t)); e != nil || v != 73 {
		t.Fatal(v, e)
	}
	var e error
	f.runtime, e = postgres.NewMovieUpgradeTrackingRuntimeStore(f.preparation)
	if e != nil {
		t.Fatal(e)
	}
	f.jobRepo, e = postgres.NewMovieUpgradeNativeJobTrackingRepository(f.preparation, f.driver, f.issuer)
	if e != nil {
		t.Fatal(e)
	}
	f.queue = jobs.NewQueue(f.p, idgen.New(), nativeUpgradeClock{})
	return f
}
func (f *trackingJobFixture) claim(t *testing.T, ttl time.Duration) ports.MovieUpgradeTrackingJobRequest {
	t.Helper()
	j, e := f.queue.Claim(context.Background(), []string{ports.MovieUpgradeTrackingJobKind}, "tracking-job-test", ttl)
	if e != nil || j == nil {
		t.Fatal(j, e)
	}
	actor, req, e := f.runtime.MovieUpgradeTrackingClaim(context.Background(), ports.JobLease{ID: j.ID, Owner: j.LeaseOwner.String, Token: j.LeaseToken})
	if e != nil || actor.ID != f.actor.ID {
		t.Fatal(actor, e)
	}
	return req
}
func (f *trackingJobFixture) counts(t *testing.T, obs, results, succeeded int) {
	t.Helper()
	var o, r, j int
	if e := f.p.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM movie_upgrade_tracking_observation),(SELECT count(*) FROM movie_upgrade_tracking_result),(SELECT count(*) FROM movie_upgrade_tracking_job b JOIN job j ON j.id=b.job_id WHERE j.state='succeeded')`).Scan(&o, &r, &j); e != nil || o != obs || r != results || j != succeeded {
		t.Fatal(o, r, j, e)
	}
	if f.adds.Load() != 1 {
		t.Fatal("extra Add", f.adds.Load())
	}
}
func TestMovieUpgradeTrackingJobNativePG(t *testing.T) {
	for _, mode := range []string{"completed", "tracking", "manual_review"} {
		t.Run(mode, func(t *testing.T) {
			f := nativeTrackingJobFixture(t)
			ctx := context.Background()
			if mode == "tracking" {
				f.complete.Store(false)
			}
			if mode == "manual_review" {
				f.wrongHash.Store(true)
			}
			before := nativeProtectedGraph(t, f.preparedUpgradeFixture)
			var wg sync.WaitGroup
			var mu sync.Mutex
			sum := 0
			for range 6 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					runtime, e := postgres.NewMovieUpgradeTrackingRuntimeStore(f.preparation)
					if e != nil {
						t.Error(e)
						return
					}
					n, e := runtime.ScheduleMovieUpgradeTracking(ctx)
					if e != nil {
						t.Error(e)
						return
					}
					mu.Lock()
					sum += n
					mu.Unlock()
				}()
			}
			wg.Wait()
			if sum != 1 {
				t.Fatal("scheduled", sum)
			}
			if out, e := f.repo.TrackMovieUpgrade(ctx, f.actor, f.operation, idgen.New().NewUUID(), "bypass-job-admit", "bypass-job-result"); e == nil || out != nil || f.infos.Load() != 0 {
				t.Fatal("legacy bypass", out, e, f.infos.Load())
			}
			req := f.claim(t, 2*time.Minute)
			out, e := f.jobRepo.TrackMovieUpgradeJob(ctx, f.actor, req)
			if e != nil || out == nil || out.Outcome != mode || out.ExecutionAllowed {
				t.Fatal(out, e)
			}
			f.counts(t, 1, 1, 1)
			if after := nativeProtectedGraph(t, f.preparedUpgradeFixture); before != after {
				t.Fatal("original graph changed")
			}
			queries := f.infos.Load()
			again, e := f.jobRepo.TrackMovieUpgradeJob(ctx, f.actor, req)
			if e != nil || again == nil || *again != *out || f.infos.Load() != queries {
				t.Fatal("receipt replay", again, e)
			}
			runtime, e := postgres.NewMovieUpgradeTrackingRuntimeStore(f.preparation)
			if e != nil {
				t.Fatal(e)
			}
			if n, e := runtime.ScheduleMovieUpgradeTracking(ctx); e != nil || n != 0 {
				t.Fatal("immediate duplicate", n, e)
			}
			var atomic bool
			if e = f.p.QueryRow(ctx, `SELECT j.result->>'observation_id'=r.observation_id::text AND j.result->>'outcome'=r.outcome AND j.finished_at=r.recorded_at FROM movie_upgrade_tracking_job b JOIN job j ON j.id=b.job_id JOIN movie_upgrade_tracking_job_claim c USING(job_id) JOIN movie_upgrade_tracking_result r USING(observation_id)`).Scan(&atomic); e != nil || !atomic {
				t.Fatal("atomic result/job", atomic, e)
			}
		})
	}
}
func TestMovieUpgradeTrackingJobClaimGuardPG(t *testing.T) {
	f := nativeTrackingJobFixture(t)
	ctx := context.Background()
	if n, e := f.runtime.ScheduleMovieUpgradeTracking(ctx); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	var job string
	if e := f.p.QueryRow(ctx, `SELECT job_id::text FROM movie_upgrade_tracking_job`).Scan(&job); e != nil {
		t.Fatal(e)
	}
	for _, query := range []string{`UPDATE job SET priority=99 WHERE id=$1`, `UPDATE job SET payload='{}' WHERE id=$1`, `DELETE FROM job WHERE id=$1`, `DELETE FROM movie_upgrade_tracking_job WHERE job_id=$1`} {
		if _, e := f.p.Exec(ctx, query, job); e == nil {
			t.Fatal("queued mutation allowed", query)
		}
	}
	req := f.claim(t, 2*time.Minute)
	for _, query := range []string{`UPDATE job SET lease_owner='intruder' WHERE id=$1`, `UPDATE job SET lease_token = job.lease_token + (1::bigint) WHERE id=$1`, `UPDATE job SET lease_until=lease_until+interval '1 minute' WHERE id=$1`, `UPDATE job SET state='queued' WHERE id=$1`} {
		if _, e := f.p.Exec(ctx, query, job); e == nil {
			t.Fatal("claim mutation allowed", query)
		}
	}
	out, e := f.jobRepo.TrackMovieUpgradeJob(ctx, f.actor, req)
	if e != nil || out == nil {
		t.Fatal(out, e)
	}
	f.counts(t, 1, 1, 1)
}
