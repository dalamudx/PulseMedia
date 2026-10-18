package postgres_test

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/jobs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/google/uuid"
	"sync"
	"testing"
	"time"
)

func runtimeObservation(t *testing.T, n *nativeUpgradeFixture) *postgres.MovieUpgradeObservationRuntimeStore {
	t.Helper()
	r, e := postgres.NewMovieUpgradeObservationRuntimeStore(n.preparation)
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func runtimeObservationClaim(t *testing.T, n *nativeUpgradeFixture) ports.MovieUpgradeObservationJobRequest {
	t.Helper()
	j, e := jobs.NewQueue(n.p, idgen.New(), nativeUpgradeClock{}).Claim(context.Background(), []string{ports.MovieUpgradeObservationJobKind}, uuid.NewString(), 2*time.Minute)
	if e != nil || j == nil {
		t.Fatalf("claim: %v", e)
	}
	var task uuid.UUID
	if e = n.p.QueryRow(context.Background(), `SELECT task_id FROM movie_upgrade_preparation WHERE operation_id=$1`, n.operation).Scan(&task); e != nil {
		t.Fatal(e)
	}
	return ports.MovieUpgradeObservationJobRequest{OperationID: n.operation, TaskID: task, Lease: ports.JobLease{ID: j.ID, Owner: j.LeaseOwner.String, Token: j.LeaseToken}, AdmissionKey: "runtime-admit", ResultKey: "runtime-result"}
}
func TestMovieUpgradeObservationRuntimeSchedulesOnceAndRunsPG(t *testing.T) {
	n := r3JobNative(t)
	f := newNativeObservation(t, n, false)
	before := nativeProtectedGraph(t, n.preparedUpgradeFixture)
	var wg sync.WaitGroup
	counts := make(chan int, 8)
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, e := runtimeObservation(t, n).ScheduleMovieUpgradeObservations(context.Background())
			counts <- s
			errs <- e
		}()
	}
	wg.Wait()
	close(counts)
	close(errs)
	total := 0
	for count := range counts {
		total += count
	}
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	if total != 1 {
		t.Fatalf("inserted %d", total)
	}
	req := runtimeObservationClaim(t, n)
	a, e := runtimeObservation(t, n).MovieUpgradeObservationActor(context.Background(), req, uuid.MustParse(n.actor.ID))
	if e != nil {
		t.Fatal(e)
	}
	out, e := nativeJobObserver(t, n).ObserveMovieUpgradeJob(context.Background(), a, req)
	if e != nil || out == nil || out.Disposition != "manual_review" || out.ExecutionAllowed {
		t.Fatal(out, e)
	}
	var same bool
	if e = n.p.QueryRow(context.Background(), `SELECT j.state='succeeded' AND to_jsonb(j)=pulse_upgrade_observation_terminal_job(b.observation_id) FROM job j JOIN movie_upgrade_observation_job b ON b.job_id=j.id WHERE j.id=$1`, req.Lease.ID).Scan(&same); e != nil || !same {
		t.Fatal("terminal", e)
	}
	for range 2 {
		s, e := runtimeObservation(t, n).ScheduleMovieUpgradeObservations(context.Background())
		if e != nil || s != 0 {
			t.Fatal("rescheduled", s, e)
		}
	}
	if before != nativeProtectedGraph(t, n.preparedUpgradeFixture) || n.adds.Load() != 1 || f.queries.Load() != 1 {
		t.Fatal("original mutation or duplicate native I/O")
	}
}
func TestMovieUpgradeObservationRuntimeHistoricalJobsPG(t *testing.T) {
	for _, state := range []string{"queued", "running", "expired", "dead"} {
		t.Run(state, func(t *testing.T) {
			n := r3JobNative(t)
			if s, e := runtimeObservation(t, n).ScheduleMovieUpgradeObservations(context.Background()); e != nil || s != 1 {
				t.Fatal(s, e)
			}
			if state != "queued" {
				req := runtimeObservationClaim(t, n)
				if state == "expired" {
					n.exec(t, `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, req.Lease.ID)
				}
				if state == "dead" {
					if e := jobs.NewQueue(n.p, idgen.New(), nativeUpgradeClock{}).Fail(context.Background(), req.Lease.ID, req.Lease.Owner, req.Lease.Token, "manual review", time.Second); e != nil {
						t.Fatal(e)
					}
				}
			}
			if s, e := runtimeObservation(t, n).ScheduleMovieUpgradeObservations(context.Background()); e != nil || s != 0 {
				t.Fatal("adopted", s, e)
			}
			var count int
			if e := n.p.QueryRow(context.Background(), `SELECT count(*) FROM job`).Scan(&count); e != nil || count != 1 {
				t.Fatal("permanent dedupe", count, e)
			}
		})
	}
}
func TestMovieUpgradeObservationRuntimeCurrentAccountPG(t *testing.T) {
	for _, mode := range []string{"disabled_before_schedule", "disabled_after_claim", "wrong_original_actor", "tampered_insert"} {
		t.Run(mode, func(t *testing.T) {
			n := r3JobNative(t)
			r := runtimeObservation(t, n)
			if mode == "disabled_before_schedule" {
				n.exec(t, `UPDATE app_user SET is_active=false WHERE id=$1`, n.actor.ID)
				if s, e := r.ScheduleMovieUpgradeObservations(context.Background()); e != nil || s != 0 {
					t.Fatal(s, e)
				}
				return
			}
			if mode == "tampered_insert" {
				n.exec(t, `CREATE FUNCTION tamper_runtime_job() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN NEW.priority:=1;RETURN NEW;END $$;CREATE TRIGGER tamper_runtime_job BEFORE INSERT ON job FOR EACH ROW EXECUTE FUNCTION tamper_runtime_job()`)
				if s, e := r.ScheduleMovieUpgradeObservations(context.Background()); e != nil || s != 0 {
					t.Fatal(s, e)
				}
				var count int
				if e := n.p.QueryRow(context.Background(), `SELECT count(*) FROM job`).Scan(&count); e != nil || count != 0 {
					t.Fatal("forged job persisted", count, e)
				}
				return
			}
			if s, e := r.ScheduleMovieUpgradeObservations(context.Background()); e != nil || s != 1 {
				t.Fatal(s, e)
			}
			req := runtimeObservationClaim(t, n)
			actor := uuid.MustParse(n.actor.ID)
			if mode == "disabled_after_claim" {
				n.exec(t, `UPDATE app_user SET is_active=false WHERE id=$1`, n.actor.ID)
			} else {
				actor = uuid.New()
			}
			if _, e := r.MovieUpgradeObservationActor(context.Background(), req, actor); !errors.Is(e, ports.ErrForbidden) {
				t.Fatal(e)
			}
		})
	}
}
func TestMovieUpgradeObservationRuntimeAccountWaitPG(t *testing.T) {
	n := r3JobNative(t)
	r := runtimeObservation(t, n)
	ctx := context.Background()
	tx, e := n.p.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer rollbackFrozenUpgradeTest(t, tx)
	if _, e = tx.Exec(ctx, `SELECT id FROM app_user WHERE id=$1 FOR UPDATE`, n.actor.ID); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() {
		s, e := r.ScheduleMovieUpgradeObservations(ctx)
		if e == nil && s != 0 {
			e = errors.New("scheduled after revocation")
		}
		done <- e
	}()
	waitPreparationBlock(t, n.p, tx.Conn().PgConn().PID())
	if _, e = tx.Exec(ctx, `UPDATE app_user SET is_active=false WHERE id=$1`, n.actor.ID); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	select {
	case e = <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("blocked scheduler")
	}
}
func TestMovieUpgradeObservationRuntimeExcludesInteractivePG(t *testing.T) {
	n := r3JobNative(t)
	f := newNativeObservation(t, n, false)
	if _, e := f.repo.ObserveMovieUpgrade(context.Background(), n.actor, n.operation, uuid.New(), "interactive-admit", "interactive-result"); e != nil {
		t.Fatal(e)
	}
	if s, e := runtimeObservation(t, n).ScheduleMovieUpgradeObservations(context.Background()); e != nil || s != 0 {
		t.Fatal(s, e)
	}
	if f.queries.Load() != 1 || n.adds.Load() != 1 {
		t.Fatal("duplicate native I/O")
	}
}
