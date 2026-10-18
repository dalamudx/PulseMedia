package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/google/uuid"
)

func trackingCounts(t *testing.T, f *trackingFixture, wantObservation, wantPermit, wantResult int) {
	t.Helper()
	var o, p, r int
	if e := f.p.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM movie_upgrade_tracking_observation),(SELECT count(*) FROM movie_upgrade_tracking_permit),(SELECT count(*) FROM movie_upgrade_tracking_result)`).Scan(&o, &p, &r); e != nil {
		t.Fatal(e)
	}
	if o != wantObservation || p != wantPermit || r != wantResult {
		t.Fatalf("tracking counts %d/%d/%d want %d/%d/%d", o, p, r, wantObservation, wantPermit, wantResult)
	}
	if f.adds.Load() != 1 {
		t.Fatal("tracking sent Add", f.adds.Load())
	}
}
func trackingEntered(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatal("native tracking did not enter")
	}
}

func TestMovieUpgradeTrackingConcurrencyRecoveryPG(t *testing.T) {
	f := nativeTrackingFixture(t, "Ok.")
	f.complete.Store(false)
	ctx := context.Background()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	var hookOnce sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	hook := func() { hookOnce.Do(func() { close(entered); <-release }) }
	f.onInfo.Store(&hook)
	type returned struct {
		result *ports.MovieUpgradeTrackingResult
		err    error
	}
	done := make(chan returned, 1)
	go func() {
		v, e := f.repo.TrackMovieUpgrade(ctx, f.actor, f.operation, uuid.New(), "concurrent-admit", "concurrent-result")
		done <- returned{v, e}
	}()
	trackingEntered(t, entered)
	errs := make(chan error, 6)
	var workers sync.WaitGroup
	for range 6 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			v, e := f.repo.TrackMovieUpgrade(ctx, f.actor, f.operation, uuid.New(), "concurrent-admit", "concurrent-result")
			if v != nil || e == nil {
				errs <- fmt.Errorf("accepted unresolved concurrent call: %v/%v", v, e)
			}
		}()
	}
	workers.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	if f.infos.Load() != 1 {
		t.Fatal("parallel native query", f.infos.Load())
	}
	unblock()
	var first returned
	select {
	case first = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("query did not drain")
	}
	if first.err != nil || first.result.Outcome != "tracking" {
		t.Fatal(first.result, first.err)
	}
	replay, e := f.repo.TrackMovieUpgrade(ctx, f.actor, f.operation, uuid.New(), "concurrent-admit", "concurrent-result")
	if e != nil || *replay != *first.result {
		t.Fatal("changed historical receipt", replay, e)
	}
	if _, e = f.repo.TrackMovieUpgrade(ctx, f.actor, f.operation, uuid.New(), "concurrent-admit", "substituted-result"); !errors.Is(e, ports.ErrUpgradeReservationConflict) {
		t.Fatal("accepted substituted result key", e)
	}
	if _, e = f.repo.TrackMovieUpgrade(ctx, f.actor, f.operation, uuid.New(), "too-soon-admit", "too-soon-result"); !errors.Is(e, ports.ErrMovieUpgradeTrackingReview) {
		t.Fatal("ignored persisted cooldown", e)
	}
	trackingCounts(t, f, 1, 1, 1)
	if f.infos.Load() != 2 {
		t.Fatal("recovery or cooldown repeated query", f.infos.Load())
	}
	if _, e = f.p.Exec(ctx, `UPDATE app_user SET is_active=FALSE WHERE id=$1`, f.actor.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = f.repo.TrackMovieUpgrade(ctx, f.actor, f.operation, uuid.New(), "concurrent-admit", "concurrent-result"); e == nil {
		t.Fatal("revoked user recovered receipt")
	}
	if f.infos.Load() != 2 {
		t.Fatal("revocation started query")
	}
}

func TestMovieUpgradeTrackingOrphanPermitPG(t *testing.T) {
	f := nativeTrackingFixture(t, "Ok.")
	ctx := context.Background()
	before := nativeProtectedGraph(t, f.preparedUpgradeFixture)
	var once sync.Once
	hook := func() {
		once.Do(func() {
			var pid int32
			if e := f.p.QueryRow(ctx, `SELECT backend_pid FROM movie_upgrade_tracking_observation`).Scan(&pid); e != nil {
				t.Error(e)
				return
			}
			if _, e := f.p.Exec(ctx, `SELECT pg_terminate_backend($1)`, pid); e != nil {
				t.Error(e)
			}
		})
	}
	f.onInfo.Store(&hook)
	if _, e := f.repo.TrackMovieUpgrade(ctx, f.actor, f.operation, uuid.New(), "orphan-admit", "orphan-result"); e == nil {
		t.Fatal("lost physical session committed result")
	}
	trackingCounts(t, f, 1, 1, 0)
	calls := f.infos.Load()
	for _, key := range []string{"orphan-admit", "fresh-orphan-admit"} {
		if _, e := f.repo.TrackMovieUpgrade(ctx, f.actor, f.operation, uuid.New(), key, "orphan-result"); !errors.Is(e, ports.ErrMovieUpgradeTrackingReview) {
			t.Fatal("orphan permit was taken over", e)
		}
	}
	if f.infos.Load() != calls || nativeProtectedGraph(t, f.preparedUpgradeFixture) != before {
		t.Fatal("orphan recovery queried or changed original graph")
	}
	verifyTrackingOrphanRestore(t, f)
}

func TestMovieUpgradeTrackingTailRollbackPG(t *testing.T) {
	for _, table := range []string{"movie_upgrade_tracking_observation", "movie_upgrade_tracking_result"} {
		t.Run(table, func(t *testing.T) {
			f := nativeTrackingFixture(t, "Ok.")
			ctx := context.Background()
			before := nativeProtectedGraph(t, f.preparedUpgradeFixture)
			sql := fmt.Sprintf(`CREATE FUNCTION pulse_test_tracking_tail() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN UPDATE library_root SET updated_at=clock_timestamp() WHERE id='%s'::uuid;RETURN NEW;END$$;CREATE TRIGGER pulse_test_tracking_tail AFTER INSERT ON %s FOR EACH ROW EXECUTE FUNCTION pulse_test_tracking_tail()`, f.root, table)
			if _, e := f.p.Exec(ctx, sql); e != nil {
				t.Fatal(e)
			}
			if _, e := f.repo.TrackMovieUpgrade(ctx, f.actor, f.operation, uuid.New(), "tail-admit", "tail-result"); e == nil {
				t.Fatal("tail mutation committed")
			}
			if nativeProtectedGraph(t, f.preparedUpgradeFixture) != before {
				t.Fatal("tail rollback changed original graph")
			}
			if table == "movie_upgrade_tracking_observation" {
				trackingCounts(t, f, 0, 0, 0)
				if f.infos.Load() != 0 {
					t.Fatal("uncommitted admission queried")
				}
			} else {
				trackingCounts(t, f, 1, 1, 0)
				if f.infos.Load() != 2 {
					t.Fatal("result was not reached")
				}
			}
			if _, e := f.p.Exec(ctx, fmt.Sprintf(`DROP TRIGGER pulse_test_tracking_tail ON %s;DROP FUNCTION pulse_test_tracking_tail()`, table)); e != nil {
				t.Fatal(e)
			}
			_, e := f.repo.TrackMovieUpgrade(ctx, f.actor, f.operation, uuid.New(), "tail-admit", "tail-result")
			if table == "movie_upgrade_tracking_observation" {
				if e != nil {
					t.Fatal("rolled-back admission blocked explicit fresh call", e)
				}
				trackingCounts(t, f, 1, 1, 1)
			} else if !errors.Is(e, ports.ErrMovieUpgradeTrackingReview) {
				t.Fatal("result rollback released physical permit", e)
			}
		})
	}
}

func TestMovieUpgradeTrackingUnknownRejectedPG(t *testing.T) {
	f := nativeTrackingFixture(t, "ambiguous")
	if _, e := f.repo.TrackMovieUpgrade(context.Background(), f.actor, f.operation, uuid.New(), "unknown-admit", "unknown-result"); e == nil {
		t.Fatal("unknown source entered confirmed tracking")
	}
	trackingCounts(t, f, 0, 0, 0)
	if f.infos.Load() != 0 {
		t.Fatal("unknown source was queried")
	}
}

func TestMovieUpgradeTrackingSuccessorPG(t *testing.T) {
	f := nativeTrackingFixture(t, "Ok.")
	f.complete.Store(false)
	ctx := context.Background()
	first, e := f.repo.TrackMovieUpgrade(ctx, f.actor, f.operation, uuid.New(), "first-admit", "first-result")
	if e != nil || first.Outcome != "tracking" {
		t.Fatal(first, e)
	}
	wait, stop := context.WithTimeout(ctx, 35*time.Second)
	defer stop()
	if _, e = f.p.Exec(wait, `SELECT pg_sleep(GREATEST(0,EXTRACT(EPOCH FROM (recorded_at+interval '30 seconds'-clock_timestamp()))+0.02)) FROM movie_upgrade_tracking_result WHERE observation_id=$1`, first.ObservationID); e != nil {
		t.Fatal(e)
	}
	f.complete.Store(true)
	next, e := f.repo.TrackMovieUpgrade(ctx, f.actor, f.operation, uuid.New(), "next-admit", "next-result")
	if e != nil || next.Outcome != "completed" || next.Disposition != "organization_review_required" || next.ObservationID == first.ObservationID {
		t.Fatal(next, e)
	}
	trackingCounts(t, f, 2, 2, 2)
	if _, e = f.repo.TrackMovieUpgrade(ctx, f.actor, f.operation, uuid.New(), "after-complete", "after-complete-result"); !errors.Is(e, ports.ErrMovieUpgradeTrackingReview) {
		t.Fatal("completed chain queried again", e)
	}
	if f.infos.Load() != 4 {
		t.Fatal("unexpected native query count", f.infos.Load())
	}
}

type trackingDelayedReturnDriver struct {
	ports.MovieUpgradeNativeTrackingDriver
	entered chan<- struct{}
	release <-chan struct{}
}
type trackingDelayedReturnAttempt struct {
	ports.MovieUpgradeTrackingAttempt
	entered chan<- struct{}
	release <-chan struct{}
}

func (d trackingDelayedReturnDriver) OpenMovieUpgradeNativeTracking(ctx context.Context, b ports.MovieUpgradeTrackingDescriptor) (ports.MovieUpgradeTrackingAttempt, error) {
	a, e := d.MovieUpgradeNativeTrackingDriver.OpenMovieUpgradeNativeTracking(ctx, b)
	if e != nil {
		return nil, e
	}
	return trackingDelayedReturnAttempt{a, d.entered, d.release}, nil
}
func (a trackingDelayedReturnAttempt) QueryMovieUpgradeTracking(ctx context.Context, p ports.MovieUpgradeTrackingQueryProof) (ports.MovieUpgradeTrackingProof, error) {
	proof, e := a.MovieUpgradeTrackingAttempt.QueryMovieUpgradeTracking(ctx, p)
	if e != nil {
		return nil, e
	}
	close(a.entered)
	<-a.release
	return proof, nil
}
func TestMovieUpgradeTrackingRealLeaseExpiryPG(t *testing.T) {
	f := nativeTrackingFixture(t, "Ok.")
	ctx := context.Background()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	repo, e := postgres.NewMovieUpgradeNativeTrackingRepository(f.preparation, trackingDelayedReturnDriver{f.driver, entered, release}, f.issuer)
	if e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() {
		_, e := repo.TrackMovieUpgrade(ctx, f.actor, f.operation, uuid.New(), "expired-admit", "expired-result")
		done <- e
	}()
	drained := false
	defer func() {
		unblock()
		if !drained {
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Error("delayed query did not drain")
			}
		}
	}()
	trackingEntered(t, entered)
	wait, stop := context.WithTimeout(ctx, 70*time.Second)
	defer stop()
	if _, e = f.p.Exec(wait, `SELECT pg_sleep(GREATEST(0,EXTRACT(EPOCH FROM (lease_until-clock_timestamp()))+0.03)) FROM movie_upgrade_tracking_observation`); e != nil {
		t.Fatal(e)
	}
	var expired bool
	if e = f.p.QueryRow(wait, `SELECT clock_timestamp()>=lease_until FROM movie_upgrade_tracking_observation`).Scan(&expired); e != nil || !expired {
		t.Fatal("database lease did not expire", expired, e)
	}
	if f.infos.Load() != 2 || f.files.Load() != 1 || f.exports.Load() != 1 {
		t.Fatal("native query had not completed before delay")
	}
	unblock()
	select {
	case e = <-done:
		drained = true
	case <-time.After(10 * time.Second):
		t.Fatal("expired query did not drain")
	}
	if e == nil {
		t.Fatal("expired lease committed returned result")
	}
	trackingCounts(t, f, 1, 1, 0)
	calls := f.infos.Load()
	if _, e = f.repo.TrackMovieUpgrade(ctx, f.actor, f.operation, uuid.New(), "expired-new-admit", "expired-new-result"); !errors.Is(e, ports.ErrMovieUpgradeTrackingReview) {
		t.Fatal("expired permit was taken over", e)
	}
	if f.infos.Load() != calls {
		t.Fatal("expired native query replayed")
	}
}
