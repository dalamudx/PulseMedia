package postgres_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestMovieUpgradeTrackingJobTailRollbackPG(t *testing.T) {
	f := nativeTrackingJobFixture(t)
	ctx := context.Background()
	if n, e := f.runtime.ScheduleMovieUpgradeTracking(ctx); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	req := f.claim(t, 2*time.Minute)
	// Mutating a protected original row after job completion must roll back the
	// result AND the successful job update, while committed physical permit stays.
	ddl := fmt.Sprintf(`CREATE FUNCTION pulse_tracking_job_tail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN UPDATE library_root SET updated_at=clock_timestamp() WHERE id='%s';RETURN NEW;END $$;CREATE TRIGGER pulse_tracking_job_tail AFTER INSERT ON movie_upgrade_tracking_result FOR EACH ROW EXECUTE FUNCTION pulse_tracking_job_tail()`, f.root)
	if _, e := f.p.Exec(ctx, ddl); e != nil {
		t.Fatal(e)
	}
	before := nativeProtectedGraph(t, f.preparedUpgradeFixture)
	out, e := f.jobRepo.TrackMovieUpgradeJob(ctx, f.actor, req)
	if e == nil || out != nil {
		t.Fatal("tail accepted", out, e)
	}
	f.counts(t, 1, 0, 0)
	if nativeProtectedGraph(t, f.preparedUpgradeFixture) != before {
		t.Fatal("tail changed original graph")
	}
	queries := f.infos.Load()
	if _, e = f.p.Exec(ctx, `DROP TRIGGER pulse_tracking_job_tail ON movie_upgrade_tracking_result;DROP FUNCTION pulse_tracking_job_tail()`); e != nil {
		t.Fatal(e)
	}
	if out, e = f.jobRepo.TrackMovieUpgradeJob(ctx, f.actor, req); e == nil || out != nil || f.infos.Load() != queries {
		t.Fatal("requery after failed writeback", out, e)
	}
	if e = f.queue.Fail(ctx, req.Lease.ID, req.Lease.Owner, req.Lease.Token, "upgrade_tracking_manual_review", time.Second); e != nil {
		t.Fatal("safe dead termination", e)
	}
	if n, e := f.runtime.ScheduleMovieUpgradeTracking(ctx); e != nil || n != 0 {
		t.Fatal("replaced failed permit", n, e)
	}
	var permits int
	if e = f.p.QueryRow(ctx, `SELECT count(*) FROM movie_upgrade_tracking_permit`).Scan(&permits); e != nil || permits != 1 {
		t.Fatal(permits, e)
	}
}
func TestMovieUpgradeTrackingJobExpiryPG(t *testing.T) {
	f := nativeTrackingJobFixture(t)
	ctx := context.Background()
	if n, e := f.runtime.ScheduleMovieUpgradeTracking(ctx); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	req := f.claim(t, 5*time.Second)
	entered, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	var once sync.Once
	hook := func() { once.Do(func() { close(entered); <-release }) }
	f.onInfo.Store(&hook)
	done := make(chan error, 1)
	go func() { _, e := f.jobRepo.TrackMovieUpgradeJob(ctx, f.actor, req); done <- e }()
	select {
	case <-entered:
	case <-time.After(4 * time.Second):
		t.Fatal("native query missing")
	}
	deadline := time.Now().Add(8 * time.Second)
	for {
		var expired bool
		if e := f.p.QueryRow(ctx, `SELECT lease_until<=clock_timestamp() FROM job WHERE id=$1`, req.Lease.ID).Scan(&expired); e != nil {
			t.Fatal(e)
		}
		if expired {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("DB lease never expired")
		}
		time.Sleep(20 * time.Millisecond)
	}
	close(release)
	select {
	case e := <-done:
		if e == nil {
			t.Fatal("late job result accepted")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("late result hung")
	}
	f.counts(t, 1, 0, 0)
	queries := f.infos.Load()
	if n, e := f.runtime.ScheduleMovieUpgradeTracking(ctx); e != nil || n != 0 {
		t.Fatal("expired takeover", n, e)
	}
	if out, e := f.jobRepo.TrackMovieUpgradeJob(ctx, f.actor, req); e == nil || out != nil || f.infos.Load() != queries {
		t.Fatal("expired requery", out, e)
	}
}
func TestMovieUpgradeTrackingJobOriginalUserPG(t *testing.T) {
	f := nativeTrackingJobFixture(t)
	ctx := context.Background()
	if n, e := f.runtime.ScheduleMovieUpgradeTracking(ctx); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	req := f.claim(t, 2*time.Minute)
	if _, e := f.p.Exec(ctx, `UPDATE app_user SET is_active=false WHERE id=$1`, f.actor.ID); e != nil {
		t.Fatal(e)
	}
	if actor, _, e := f.runtime.MovieUpgradeTrackingClaim(ctx, req.Lease); e == nil || actor != nil {
		t.Fatal("revoked original actor", actor, e)
	}
	if out, e := f.jobRepo.TrackMovieUpgradeJob(ctx, f.actor, req); e == nil || out != nil || f.infos.Load() != 0 {
		t.Fatal("revoked query", out, e)
	}
	f.counts(t, 0, 0, 0)
	if n, e := f.runtime.ScheduleMovieUpgradeTracking(ctx); e != nil || n != 0 {
		t.Fatal(n, e)
	}
}

func TestMovieUpgradeTrackingJobFirstRowPG(t *testing.T) {
	f := nativeTrackingJobFixture(t)
	ctx := context.Background()
	if n, e := f.runtime.ScheduleMovieUpgradeTracking(ctx); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	var id string
	if e := f.p.QueryRow(ctx, `SELECT job_id::text FROM movie_upgrade_tracking_job`).Scan(&id); e != nil {
		t.Fatal(e)
	}
	for _, sql := range []string{`UPDATE job SET priority=priority+1 WHERE id=$1`, `UPDATE job SET payload=payload||'{"unexpected":true}'::jsonb WHERE id=$1`, `UPDATE job SET not_before=not_before+interval '1 second' WHERE id=$1`, `UPDATE job SET max_attempts=2 WHERE id=$1`, `INSERT INTO job SELECT (jsonb_populate_record(NULL::job,to_jsonb(j)||jsonb_build_object('id',gen_random_uuid(),'dedupe_key',j.dedupe_key||':unbound'))).* FROM job j WHERE j.id=$1`} {
		if _, e := f.p.Exec(ctx, sql, id); e == nil {
			t.Fatal("mutated queued binding or inserted unbound job", sql)
		}
	}
	req := f.claim(t, time.Minute)
	if out, e := f.jobRepo.TrackMovieUpgradeJob(ctx, f.actor, req); e != nil || out.Outcome != "completed" {
		t.Fatal(out, e)
	}
	f.counts(t, 1, 1, 1)
}
func TestMovieUpgradeTrackingJobAccountWaitPG(t *testing.T) {
	f := nativeTrackingJobFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
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
	if _, e = tx.Exec(ctx, `SELECT id FROM app_user WHERE id=$1 FOR UPDATE`, f.actor.ID); e != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() {
		n, e := f.runtime.ScheduleMovieUpgradeTracking(ctx)
		if e == nil && n != 0 {
			e = fmt.Errorf("revoked schedule count %d", n)
		}
		done <- e
	}()
	drained := false
	defer func() {
		cancel()
		c, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_ = tx.Rollback(c)
		if !drained {
			<-done
		}
	}()
	waitPreparationBlock(t, f.p, holder.Conn().PgConn().PID())
	if _, e = tx.Exec(ctx, `UPDATE app_user SET is_active=false WHERE id=$1`, f.actor.ID); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	e = <-done
	drained = true
	if e != nil {
		t.Fatal(e)
	}
	var n int
	if e = f.p.QueryRow(context.Background(), `SELECT count(*) FROM movie_upgrade_tracking_job`).Scan(&n); e != nil || n != 0 || f.infos.Load() != 0 {
		t.Fatal("revoked while waiting created work", n, e)
	}
	f.counts(t, 0, 0, 0)
}
