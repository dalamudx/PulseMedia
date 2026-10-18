package postgres_test

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/platform/jobs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/download"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/google/uuid"
	"net/http"
	"strings"
	"testing"
	"time"
)

type observationJobIDs struct{}

func (observationJobIDs) NewUUID() uuid.UUID    { return uuid.New() }
func (observationJobIDs) NewUUIDString() string { return uuid.NewString() }
func claimedObservationJob(t *testing.T, n *nativeUpgradeFixture) (ports.MovieUpgradeObservationJobRequest, *jobs.Queue) {
	t.Helper()
	ctx := context.Background()
	q := jobs.NewQueue(n.p, observationJobIDs{}, nativeUpgradeClock{})
	var task uuid.UUID
	if e := n.p.QueryRow(ctx, "SELECT task_id FROM movie_upgrade_preparation WHERE operation_id=$1", n.operation).Scan(&task); e != nil {
		t.Fatal(e)
	}
	payload := map[string]any{"operation_id": n.operation, "task_id": task, "actor_id": n.actor.ID}
	_, e := q.Enqueue(ctx, ports.MovieUpgradeObservationJobKind, "movie-upgrade-observation:"+n.operation.String(), 0, payload, time.Time{}, 1)
	if e != nil {
		t.Fatal(e)
	}
	j, e := q.Claim(ctx, []string{ports.MovieUpgradeObservationJobKind}, "upgrade-job-owner", 90*time.Second)
	if e != nil || j == nil {
		t.Fatalf("real claim: %v", e)
	}
	return ports.MovieUpgradeObservationJobRequest{Lease: ports.JobLease{ID: j.ID, Owner: j.LeaseOwner.String, Token: j.LeaseToken}, OperationID: n.operation, TaskID: task, AdmissionKey: "job-admit", ResultKey: "job-result"}, q
}
func nativeJobObserver(t *testing.T, n *nativeUpgradeFixture) ports.MovieUpgradeNativeJobObservationRepository {
	t.Helper()
	v := postgres.NewMovieUpgradeNativeQueryVerifier()
	d, e := download.NewMovieUpgradeNativeObservationDriver(n.manager, v, nativeUpgradeClock{})
	if e != nil {
		t.Fatal(e)
	}
	r, e := postgres.NewMovieUpgradeNativeJobObservationRepository(n.preparation, d, v)
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func TestMovieUpgradeObservationJobPG(t *testing.T) {
	for _, outcome := range []string{"exact_match", "absent", "ambiguous", "unknown"} {
		t.Run(outcome, func(t *testing.T) {
			n := r3JobNative(t)
			f := newNativeObservation(t, n, false)
			switch outcome {
			case "absent":
				f.body = "[]"
			case "ambiguous":
				f.body = "[" + strings.TrimSuffix(strings.TrimPrefix(f.body, "["), "]") + "," + strings.TrimSuffix(strings.TrimPrefix(f.body, "["), "]") + "]"
			case "unknown":
				f.status = http.StatusBadGateway
			}
			req, _ := claimedObservationJob(t, n)
			before := nativeProtectedGraph(t, n.preparedUpgradeFixture)
			old := n.oldState(t)
			out, e := nativeJobObserver(t, n).ObserveMovieUpgradeJob(context.Background(), n.actor, req)
			if e != nil || out == nil || out.Outcome != outcome || out.Disposition != "manual_review" || out.ExecutionAllowed {
				var obsCount, bindingCount, resultCount int
				queryErr := n.p.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM movie_upgrade_observation),(SELECT count(*) FROM movie_upgrade_observation_job),(SELECT count(*) FROM movie_upgrade_observation_result)`).Scan(&obsCount, &bindingCount, &resultCount)
				t.Fatalf("native job: %+v %v; queries=%d observation=%d binding=%d result=%d probe=%v", out, e, f.queries.Load(), obsCount, bindingCount, resultCount, queryErr)
			}
			var same bool
			e = n.p.QueryRow(context.Background(), `SELECT to_jsonb(j)=pulse_upgrade_observation_terminal_job(b.observation_id) AND j.state='succeeded' FROM job j JOIN movie_upgrade_observation_job b ON b.job_id=j.id WHERE j.id=$1`, req.Lease.ID).Scan(&same)
			if e != nil || !same {
				t.Fatalf("atomic terminal: %v", e)
			}
			if f.queries.Load() != 1 || n.adds.Load() != 1 || before != nativeProtectedGraph(t, n.preparedUpgradeFixture) || old != n.oldState(t) {
				t.Fatal("changed original or repeated I/O")
			}
			if _, e = nativeJobObserver(t, n).ObserveMovieUpgradeJob(context.Background(), n.actor, req); e == nil {
				t.Fatal("reentered finished job")
			}
			if f.queries.Load() != 1 {
				t.Fatal("query replay")
			}
		})
	}
}
func TestMovieUpgradeObservationJobLeaseFencesPG(t *testing.T) {
	for _, change := range []string{"token", "owner", "task", "token_actor"} {
		t.Run(change, func(t *testing.T) {
			n := r3JobNative(t)
			f := newNativeObservation(t, n, false)
			req, _ := claimedObservationJob(t, n)
			a := *n.actor
			switch change {
			case "token":
				req.Lease.Token++
			case "owner":
				req.Lease.Owner = "wrong"
			case "task":
				req.TaskID = uuid.New()
			case "token_actor":
				a.Scopes = []string{"read"}
			}
			_, e := nativeJobObserver(t, n).ObserveMovieUpgradeJob(context.Background(), &a, req)
			if e == nil {
				t.Fatal("invalid job accepted")
			}
			var count int
			if e = n.p.QueryRow(context.Background(), `SELECT count(*) FROM movie_upgrade_observation`).Scan(&count); e != nil || count != 0 || f.queries.Load() != 0 {
				t.Fatalf("created execution capability: %d %v", count, e)
			}
		})
	}
}
func TestMovieUpgradeObservationJobChangedDuringQueryPG(t *testing.T) {
	n := r3JobNative(t)
	f := newNativeObservation(t, n, true)
	req, _ := claimedObservationJob(t, n)
	r := nativeJobObserver(t, n)
	done := make(chan error, 1)
	go func() { _, e := r.ObserveMovieUpgradeJob(context.Background(), n.actor, req); done <- e }()
	select {
	case <-f.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("native query not reached")
	}
	// Privileged fault injection exercises the Go fence after bypassing the SQL fence.
	n.exec(t, `ALTER TABLE job DISABLE TRIGGER movie_upgrade_observation_job_terminal`)
	if _, e := n.p.Exec(context.Background(), `UPDATE job SET lease_owner = 'replacement', lease_token = lease_token + 1, updated_at = clock_timestamp() WHERE id=$1`, req.Lease.ID); e != nil {
		t.Fatal(e)
	}
	n.exec(t, `ALTER TABLE job ENABLE TRIGGER movie_upgrade_observation_job_terminal`)
	f.unblock()
	select {
	case e := <-done:
		if e == nil {
			t.Fatal("stale job wrote result")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("query not drained")
	}
	var count int
	if e := n.p.QueryRow(context.Background(), `SELECT count(*) FROM movie_upgrade_observation_result`).Scan(&count); e != nil || count != 0 {
		t.Fatalf("stale result %d %v", count, e)
	}
	req.Lease.Owner = "replacement"
	req.Lease.Token++
	if _, e := r.ObserveMovieUpgradeJob(context.Background(), n.actor, req); !errors.Is(e, ports.ErrMovieUpgradeObservationReview) {
		t.Fatalf("orphan permit adopted: %v", e)
	}
	if f.queries.Load() != 1 || n.adds.Load() != 1 {
		t.Fatal("physical replay")
	}
}
func TestMovieUpgradeObservationJobTailRollbackPG(t *testing.T) {
	n := r3JobNative(t)
	f := newNativeObservation(t, n, false)
	req, _ := claimedObservationJob(t, n)
	_, e := n.p.Exec(context.Background(), `CREATE SEQUENCE pulse_job_tail_reached; CREATE FUNCTION pulse_job_tail_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.state='succeeded' THEN PERFORM nextval('pulse_job_tail_reached'); NEW.updated_at:=NEW.updated_at+interval '1 microsecond'; END IF; RETURN NEW; END $$; CREATE TRIGGER pulse_job_tail_fault BEFORE UPDATE ON job FOR EACH ROW EXECUTE FUNCTION pulse_job_tail_fault()`)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = nativeJobObserver(t, n).ObserveMovieUpgradeJob(context.Background(), n.actor, req); e == nil {
		t.Fatal("tampered terminal accepted")
	}
	var reached bool
	var count int
	var state string
	if e = n.p.QueryRow(context.Background(), `SELECT is_called FROM pulse_job_tail_reached`).Scan(&reached); e != nil || !reached {
		t.Fatal("tail not reached", e)
	}
	if e = n.p.QueryRow(context.Background(), `SELECT count(*) FROM movie_upgrade_observation_result`).Scan(&count); e != nil || count != 0 {
		t.Fatal("partial result", e)
	}
	if e = n.p.QueryRow(context.Background(), `SELECT state FROM job WHERE id=$1`, req.Lease.ID).Scan(&state); e != nil || state != "running" {
		t.Fatal("partial job completion", e)
	}
	if f.queries.Load() != 1 || n.adds.Load() != 1 {
		t.Fatal("physical replay")
	}
}
